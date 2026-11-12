// Package conn opens gNMI connections, directly or through SSH jump hosts.
package conn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/sshx"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Options describe a gNMI target.
type Options struct {
	Address    string `json:"address"`
	Insecure   bool   `json:"insecure,omitempty"`   // plaintext, no TLS
	SkipVerify bool   `json:"skipVerify,omitempty"` // TLS without certificate verification
	CAFile     string `json:"caFile,omitempty"`
	CertFile   string `json:"certFile,omitempty"`
	KeyFile    string `json:"keyFile,omitempty"`
	// CAPEM, CertPEM and KeyPEM give the CA and client certificate in memory
	// (for example from Kubernetes Secrets) instead of files.
	CAPEM      []byte        `json:"-"`
	CertPEM    []byte        `json:"-"`
	KeyPEM     []byte        `json:"-"`
	ServerName string        `json:"serverName,omitempty"`
	Username   string        `json:"username,omitempty"`
	Password   string        `json:"-"`
	Timeout    time.Duration `json:"timeout,omitempty"`
	// SSH jump hosts; nil or no Jumps means a direct connection.
	SSH *sshx.Config `json:"-"`
}

// Validate checks the options and fills defaults.
func (o *Options) Validate() error {
	if o.Address == "" {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "target address is required")
	}
	if _, _, err := net.SplitHostPort(o.Address); err != nil {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "target address %q must be host:port", o.Address)
	}
	if o.Insecure && (o.SkipVerify || o.CAFile != "" || o.CertFile != "" || len(o.CAPEM) > 0) {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--insecure (plaintext) can't be combined with TLS options")
	}
	if (o.CertFile == "") != (o.KeyFile == "") {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "client certificate and key must be given together")
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	return nil
}

// Jumped reports whether the connection goes through SSH jump hosts.
func (o *Options) Jumped() bool { return o.SSH != nil && len(o.SSH.Jumps) > 0 }

// Conn is an open gNMI connection.
type Conn struct {
	Opts   Options
	Client gpb.GNMIClient
	Hops   []sshx.Hop
	cc     *grpc.ClientConn
	chain  *sshx.Chain
}

// Dial opens the connection and verifies it with a Capabilities call, so errors
// surface here rather than on first use.
func Dial(ctx context.Context, o Options) (*Conn, *gpb.CapabilityResponse, error) {
	if err := o.Validate(); err != nil {
		return nil, nil, err
	}
	c := &Conn{Opts: o}
	dialOpts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256 << 20)),
	}
	if o.Insecure {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		tc, err := tlsConfig(o)
		if err != nil {
			return nil, nil, err
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
	}
	if o.Username != "" || o.Password != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(basicAuth{o.Username, o.Password}))
	}
	if o.Jumped() {
		sshCfg := *o.SSH
		if sshCfg.Timeout == 0 {
			sshCfg.Timeout = o.Timeout
		}
		dctx, cancel := context.WithTimeout(ctx, o.Timeout)
		chain, err := sshx.Dial(dctx, sshCfg)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		c.chain = chain
		c.Hops = chain.Hops
		dialOpts = append(dialOpts, grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return chain.DialContext(ctx, "tcp", addr)
		}))
	} else {
		dialOpts = append(dialOpts, grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: o.Timeout}
			return d.DialContext(ctx, "tcp", addr)
		}))
	}
	// passthrough: the address is resolved by the dialer (on the jump host, if any).
	cc, err := grpc.NewClient("passthrough:///"+o.Address, dialOpts...)
	if err != nil {
		c.Close()
		return nil, nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	c.cc = cc
	c.Client = gpb.NewGNMIClient(cc)

	cctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	// Not WaitForReady: a failed first connection attempt returns its real cause
	// (refused, TLS, ...) right away instead of waiting for the deadline.
	caps, err := c.Client.Capabilities(cctx, &gpb.CapabilityRequest{})
	if err != nil {
		c.Close()
		return nil, nil, classifyDialError(o, err)
	}
	return c, caps, nil
}

// Close closes the connection and any SSH chain.
func (c *Conn) Close() error {
	if c == nil {
		return nil
	}
	if c.cc != nil {
		c.cc.Close()
	}
	if c.chain != nil {
		c.chain.Close()
	}
	return nil
}

func tlsConfig(o Options) (*tls.Config, error) {
	tc := &tls.Config{
		InsecureSkipVerify: o.SkipVerify, //nolint:gosec // explicit user choice
		ServerName:         o.ServerName,
		MinVersion:         tls.VersionTLS12,
	}
	if tc.ServerName == "" && !o.SkipVerify {
		host, _, _ := net.SplitHostPort(o.Address)
		tc.ServerName = host
	}
	caPEM, caName := o.CAPEM, "the CA given"
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		caPEM, caName = pem, o.CAFile
	}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no certificates found in %s", caName)
		}
		tc.RootCAs = pool
	}
	switch {
	case o.CertFile != "":
		cert, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		tc.Certificates = []tls.Certificate{cert}
	case len(o.CertPEM) > 0:
		cert, err := tls.X509KeyPair(o.CertPEM, o.KeyPEM)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

// classifyDialError turns the first-RPC error into a connection error with a hint.
func classifyDialError(o Options, err error) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	st, _ := status.FromError(err)
	msg := st.Message()
	lower := strings.ToLower(msg)
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied:
		return apperr.FromDevice(err)
	case codes.DeadlineExceeded:
		e := &apperr.Error{Class: apperr.ClassConnection, Code: apperr.CodeTimeout, GRPCCode: st.Code().String(), Err: err,
			Message: fmt.Sprintf("no answer from %s within %s", o.Address, o.Timeout)}
		if o.Insecure {
			e.Hint = "if the device requires TLS, connect without --insecure"
		} else {
			e.Hint = "check the address, port and network path"
		}
		return e
	case codes.Unavailable:
		e := &apperr.Error{Class: apperr.ClassConnection, Code: apperr.CodeUnreachable, Message: msg, GRPCCode: st.Code().String(), Err: err}
		switch {
		case strings.Contains(lower, "certificate") || strings.Contains(lower, "x509") || strings.Contains(lower, "tls:"):
			e.Code = apperr.CodeTLS
			if strings.Contains(lower, "first record does not look like a tls handshake") {
				e.Hint = "the device speaks plaintext; use --insecure"
			} else {
				e.Hint = "certificate verification failed; use --tls-ca, --tls-server-name or --skip-verify"
			}
		case o.Insecure && (strings.Contains(lower, "eof") || strings.Contains(lower, "preface") ||
			strings.Contains(lower, "connection reset") || strings.Contains(lower, "http2")):
			e.Code = apperr.CodeTLS
			e.Hint = "the device closed the plaintext connection; it probably requires TLS (drop --insecure)"
		case strings.Contains(lower, "refused"):
			e.Hint = "nothing listens on " + o.Address + "; check the port and that gNMI is enabled"
		}
		return e
	}
	return apperr.FromDevice(err)
}

// basicAuth sends the gNMI username and password as per-RPC metadata.
type basicAuth struct{ user, pass string }

func (b basicAuth) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"username": b.user, "password": b.pass}, nil
}

func (basicAuth) RequireTransportSecurity() bool { return false }
