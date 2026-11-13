package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/secrets"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type targetFlags struct {
	address    string
	insecure   bool
	skipVerify bool
	caFile     string
	certFile   string
	keyFile    string
	serverName string
	username   string
	timeout    time.Duration

	jumps          []string
	identities     []string
	acceptHostKeys []string
	saveHostKey    bool

	preset string
	fs     *pflag.FlagSet
}

func addTargetFlags(cmd *cobra.Command, f *targetFlags) {
	fl := cmd.Flags()
	f.fs = fl
	fl.StringVarP(&f.preset, "conn", "c", "", "use a saved connection (see 'canopy conn list'); other flags override its settings")
	fl.StringVarP(&f.address, "target", "a", "", "gNMI target host:port")
	fl.BoolVar(&f.insecure, "insecure", false, "plaintext gRPC (no TLS)")
	fl.BoolVar(&f.skipVerify, "skip-verify", false, "TLS without certificate verification")
	fl.StringVar(&f.caFile, "tls-ca", "", "CA certificate file to verify the target")
	fl.StringVar(&f.certFile, "tls-cert", "", "client certificate file (mutual TLS)")
	fl.StringVar(&f.keyFile, "tls-key", "", "client key file (mutual TLS)")
	fl.StringVar(&f.serverName, "tls-server-name", "", "expected server name in the target certificate")
	fl.StringVarP(&f.username, "username", "u", "", "username (default $GNMI_USERNAME); the password comes from $GNMI_PASSWORD or a prompt")
	fl.DurationVar(&f.timeout, "timeout", 10*time.Second, "connection and request timeout")
	fl.StringSliceVarP(&f.jumps, "jump", "J", nil, "SSH jump host(s), [user@]host[:port] or ~/.ssh/config alias; repeat or comma-separate for a chain")
	fl.StringSliceVar(&f.identities, "ssh-identity", nil, "SSH private key file(s) for the jump hosts")
	fl.StringSliceVar(&f.acceptHostKeys, "accept-host-key", nil, "accept this SSH host key fingerprint (SHA256:...) if not in known_hosts")
	fl.BoolVar(&f.saveHostKey, "save-host-key", false, "add accepted host keys to known_hosts")
}

// target builds the connection request, reading secrets from the environment or a
// prompt. needPassword is false for commands that don't connect.
func (g *globals) target(f *targetFlags, needPassword bool) (app.Target, error) {
	var t app.Target
	passwordEnv := "GNMI_PASSWORD"
	presetSource := ""
	set := func(name string) bool { return f.preset == "" || f.fs.Changed(name) }
	if f.preset != "" {
		p, err := g.presets().Get(f.preset)
		if err != nil {
			return t, err
		}
		t = app.TargetFrom(p)
		if t.TimeoutSec == 0 {
			t.TimeoutSec = int(f.timeout / time.Second)
		}
		if p.PasswordEnv != "" {
			passwordEnv = p.PasswordEnv
		}
		presetSource = p.Source
	}
	if set("target") {
		t.Address = f.address
	}
	if set("insecure") || set("skip-verify") {
		t.Insecure, t.SkipVerify = f.insecure, f.skipVerify
	}
	if set("tls-ca") {
		t.CAFile = f.caFile
	}
	if set("tls-cert") || set("tls-key") {
		t.CertFile, t.KeyFile = f.certFile, f.keyFile
	}
	if set("tls-server-name") {
		t.ServerName = f.serverName
	}
	if set("username") {
		t.Username = f.username
	}
	if set("timeout") {
		t.TimeoutSec = int(f.timeout / time.Second)
	}
	if set("jump") {
		t.Jumps = f.jumps
	}
	if set("ssh-identity") {
		t.IdentityFiles = f.identities
	}
	t.AcceptHostKeys, t.SaveHostKeys = f.acceptHostKeys, f.saveHostKey
	t.Passphrase, t.SSHPassword = os.Getenv("CANOPY_SSH_PASSPHRASE"), os.Getenv("CANOPY_SSH_PASSWORD")
	if t.Address == "" {
		return t, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--target or --conn is required")
	}
	if t.TimeoutSec == 0 {
		t.TimeoutSec = 1
	}
	if t.Username == "" {
		t.Username = os.Getenv("GNMI_USERNAME")
	}
	if !needPassword {
		return t, nil
	}
	t.Password = os.Getenv(passwordEnv)
	if t.Password == "" && f.preset != "" && passwordEnv != "GNMI_PASSWORD" {
		t.Password = os.Getenv("GNMI_PASSWORD")
	}
	if f.preset != "" {
		// A stored password (keychain or encrypted file) comes before the generic
		// GNMI_PASSWORD (but after the preset's own variable).
		if pw, where := secrets.Lookup(t.Preset, presetSource); where != "" && (passwordEnv == "GNMI_PASSWORD" || os.Getenv(passwordEnv) == "") {
			t.Password = pw
		}
	}
	if t.Username == "" && g.interactive() {
		u, err := g.prompt("Username: ", false)
		if err != nil {
			return t, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		t.Username = u
	}
	if t.Password == "" && t.Username != "" && g.interactive() {
		p, err := g.prompt("Password for "+t.Username+": ", true)
		if err != nil {
			return t, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		t.Password = p
	}
	return t, nil
}

// connect opens a session, asking about unknown host keys and key passphrases
// when interactive.
func (g *globals) connect(ctx context.Context, svc *app.Service, t app.Target) (*app.Session, error) {
	for attempt := 0; attempt < 4; attempt++ {
		sess, err := svc.Connect(ctx, t)
		if err == nil {
			return sess, nil
		}
		ae := apperr.As(err)
		if !g.interactive() {
			return nil, err
		}
		switch ae.Code {
		case apperr.CodeHostKeyUnknown:
			fmt.Fprintf(g.stderr, "The host key of %s is not known.\n  %s %s\n", ae.Details["jumpHost"], ae.Details["keyType"], ae.Details["fingerprint"])
			ans, _ := g.prompt("Trust it and add it to known_hosts? [y/N]: ", false)
			if !strings.EqualFold(strings.TrimSpace(ans), "y") {
				return nil, err
			}
			t.AcceptHostKeys = append(t.AcceptHostKeys, ae.Details["fingerprint"])
			t.SaveHostKeys = true
		case apperr.CodePassphraseRequired:
			p, perr := g.prompt("Passphrase for "+ae.Details["keyFile"]+": ", true)
			if perr != nil {
				return nil, err
			}
			if t.Passphrases == nil {
				t.Passphrases = map[string]string{}
			}
			t.Passphrases[ae.Details["keyFile"]] = p
		default:
			return nil, err
		}
	}
	return nil, apperr.New(apperr.ClassConnection, apperr.CodeSSH, "giving up after repeated prompts")
}

func newConnectCmd(g *globals) *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect to a target and print its capabilities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := g.target(&f, true)
			if err != nil {
				return err
			}
			svc := g.service()
			defer svc.Close()
			sess, err := g.connect(ctx(cmd), svc, t)
			if err != nil {
				return err
			}
			return g.result(cmd, sess, func(w io.Writer) {
				fmt.Fprintf(w, "Connected to %s in %d ms", sess.Address, sess.ConnectMs)
				if len(sess.Hops) > 0 {
					fmt.Fprintf(w, " via %s", strings.Join(sess.Hops, " -> "))
				}
				c := sess.Capabilities
				fmt.Fprintf(w, "\ngNMI version: %s\nEncodings:    %s\nModels (%d):\n", c.GNMIVersion, strings.Join(c.Encodings, ", "), len(c.Models))
				for _, m := range c.Models {
					fmt.Fprintf(w, "  %-50s %-12s %s\n", m.Name, m.Version, m.Organization)
				}
			})
		},
	}
	addTargetFlags(cmd, &f)
	return cmd
}
