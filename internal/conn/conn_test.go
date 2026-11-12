package conn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/sshx"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeGNMI answers Capabilities and Get, and checks the credentials.
type fakeGNMI struct {
	gpb.UnimplementedGNMIServer
}

func auth(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md["username"]) == 0 || md["username"][0] != "admin" || len(md["password"]) == 0 || md["password"][0] != "pw" {
		return status.Error(codes.Unauthenticated, "bad credentials")
	}
	return nil
}

func (fakeGNMI) Capabilities(ctx context.Context, _ *gpb.CapabilityRequest) (*gpb.CapabilityResponse, error) {
	if err := auth(ctx); err != nil {
		return nil, err
	}
	return &gpb.CapabilityResponse{GNMIVersion: "0.10.0",
		SupportedEncodings: []gpb.Encoding{gpb.Encoding_JSON_IETF},
		SupportedModels:    []*gpb.ModelData{{Name: "ex-base", Organization: "Example", Version: "2026-01-01"}}}, nil
}

func (fakeGNMI) Get(ctx context.Context, r *gpb.GetRequest) (*gpb.GetResponse, error) {
	if err := auth(ctx); err != nil {
		return nil, err
	}
	return &gpb.GetResponse{Notification: []*gpb.Notification{{Update: []*gpb.Update{{Path: r.Path[0],
		Val: &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`"v1"`)}}}}}}}, nil
}

func startGNMI(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	gpb.RegisterGNMIServer(s, fakeGNMI{})
	go s.Serve(ln)
	t.Cleanup(s.Stop)
	return ln.Addr().String()
}

func TestDirect(t *testing.T) {
	addr := startGNMI(t)
	c, caps, err := Dial(context.Background(), Options{Address: addr, Insecure: true, Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if cc := ConvertCapabilities(caps); cc.GNMIVersion != "0.10.0" || cc.Encodings[0] != "json_ietf" || cc.Models[0].Name != "ex-base" {
		t.Errorf("caps = %+v", cc)
	}
}

func TestWrongPassword(t *testing.T) {
	addr := startGNMI(t)
	_, _, err := Dial(context.Background(), Options{Address: addr, Insecure: true, Username: "admin", Password: "nope"})
	ae := apperr.As(err)
	if ae.Code != apperr.CodeAuth || ae.Class != apperr.ClassConnection || ae.GRPCCode != "Unauthenticated" {
		t.Fatalf("err = %+v", ae)
	}
}

func TestTLSAgainstPlaintext(t *testing.T) {
	addr := startGNMI(t)
	_, _, err := Dial(context.Background(), Options{Address: addr, SkipVerify: true, Timeout: 3 * time.Second})
	ae := apperr.As(err)
	if ae.Code != apperr.CodeTLS || ae.Hint == "" {
		t.Fatalf("err = %+v", ae)
	}
}

// startSSH runs an SSH server that accepts password "jump" and forwards
// direct-tcpip channels. It returns its address and host key.
func startSSH(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == "jump" {
				return nil, nil
			}
			return nil, io.EOF
		},
		MaxAuthTries: 20,
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					if nch.ChannelType() != "direct-tcpip" {
						nch.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					// RFC 4254 7.2: host string, port uint32, origin string, port uint32
					d := nch.ExtraData()
					hl := binary.BigEndian.Uint32(d)
					host := string(d[4 : 4+hl])
					port := binary.BigEndian.Uint32(d[4+hl:])
					up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
					if err != nil {
						nch.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, rq, _ := nch.Accept()
					go ssh.DiscardRequests(rq)
					go func() { io.Copy(ch, up); ch.Close() }()
					go func() { io.Copy(up, ch); up.Close() }()
				}
			}()
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func TestThroughJumpHost(t *testing.T) {
	gnmiAddr := startGNMI(t)
	jump, hostKey := startSSH(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	base := sshx.Config{Jumps: []string{"tester@" + jump}, Password: "jump", KnownHostsFile: kh, NoAgent: true,
		IdentityFiles: []string{filepath.Join(t.TempDir(), "none")}}
	o := Options{Address: gnmiAddr, Insecure: true, Username: "admin", Password: "pw", Timeout: 5 * time.Second}

	// Unknown host key: refused, with the fingerprint.
	cfg := base
	o.SSH = &cfg
	_, _, err := Dial(context.Background(), o)
	ae := apperr.As(err)
	fp := ssh.FingerprintSHA256(hostKey)
	if ae.Code != apperr.CodeHostKeyUnknown || ae.Details["fingerprint"] != fp {
		t.Fatalf("err = %+v", ae)
	}

	// Accepted and saved: connects, then works from known_hosts alone.
	cfg = base
	cfg.AcceptHostKeys = []string{fp}
	cfg.SaveAcceptedHostKeys = true
	o.SSH = &cfg
	c, caps, err := Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if caps.GNMIVersion != "0.10.0" || len(c.Hops) != 1 {
		t.Errorf("caps %v hops %v", caps, c.Hops)
	}
	c.Close()

	cfg = base
	o.SSH = &cfg
	c, _, err = Dial(context.Background(), o)
	if err != nil {
		t.Fatalf("with saved key: %v", err)
	}
	c.Close()

	// Wrong SSH password: SSH auth error.
	cfg = base
	cfg.Password = "wrong"
	o.SSH = &cfg
	_, _, err = Dial(context.Background(), o)
	if ae := apperr.As(err); ae.Code != apperr.CodeSSHAuth {
		t.Fatalf("err = %+v", ae)
	}
}
