// Package sshx dials TCP connections through a chain of SSH jump hosts, without
// opening local ports or starting an external ssh process.
package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Config describes how to reach the jump hosts.
type Config struct {
	// Jumps are the hops in order, each "[user@]host[:port]" or a Host alias from
	// ~/.ssh/config. A ProxyJump in ~/.ssh/config for the first hop is expanded.
	Jumps []string
	// IdentityFiles are private key files tried in addition to the agent and the
	// files from ~/.ssh/config and the default key names.
	IdentityFiles []string
	// Passphrases maps key files to their passphrase. Keys with a passphrase that
	// isn't known here are skipped; if authentication then fails, the error is
	// passphrase_required naming the file.
	Passphrases map[string]string
	// DefaultPassphrase is tried for encrypted keys not in Passphrases.
	DefaultPassphrase string
	// Password for password and keyboard-interactive authentication, if any.
	Password string
	// KnownHostsFile defaults to ~/.ssh/known_hosts.
	KnownHostsFile string
	// AcceptHostKeys lists SHA256 fingerprints ("SHA256:...") of host keys the user
	// accepted although they aren't in known_hosts.
	AcceptHostKeys []string
	// SaveAcceptedHostKeys appends accepted keys to KnownHostsFile.
	SaveAcceptedHostKeys bool
	// NoAgent disables the SSH agent.
	NoAgent bool
	Timeout time.Duration
}

// Hop is one resolved jump host.
type Hop struct {
	Alias         string
	User          string
	Host          string
	Port          int
	IdentityFiles []string
}

func (h Hop) addr() string { return net.JoinHostPort(h.Host, strconv.Itoa(h.Port)) }

// String formats the hop as user@host:port.
func (h Hop) String() string {
	return h.User + "@" + h.addr()
}

// Chain is an established chain of SSH connections.
type Chain struct {
	Hops    []Hop
	clients []*ssh.Client
}

// sshConfig is the user's ~/.ssh/config, or nil if there is none.
var sshConfig = loadUserConfig()

func loadUserConfig() *ssh_config.Config {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	defer f.Close()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil
	}
	return cfg
}

func cfgGet(alias, key string) string {
	if sshConfig == nil {
		return ""
	}
	v, _ := sshConfig.Get(alias, key)
	return v
}

func cfgGetAll(alias, key string) []string {
	if sshConfig == nil {
		return nil
	}
	v, _ := sshConfig.GetAll(alias, key)
	return v
}

// ParseHop resolves "[user@]host[:port]" with ~/.ssh/config.
func ParseHop(s string) (Hop, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Hop{}, errors.New("empty jump host")
	}
	h := Hop{}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		h.User, s = s[:i], s[i+1:]
	}
	host, port := s, ""
	if hh, pp, err := net.SplitHostPort(s); err == nil {
		host, port = hh, pp
	}
	h.Alias = host
	h.Host = host
	if hn := cfgGet(host, "HostName"); hn != "" {
		h.Host = strings.ReplaceAll(hn, "%h", host)
	}
	if port == "" {
		port = cfgGet(host, "Port")
	}
	if port == "" {
		port = "22"
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return Hop{}, fmt.Errorf("jump host %q: bad port %q", s, port)
	}
	h.Port = p
	if h.User == "" {
		h.User = cfgGet(host, "User")
	}
	if h.User == "" {
		h.User = currentUser()
	}
	for _, f := range cfgGetAll(host, "IdentityFile") {
		if f != "" && !isDefaultIdentity(f) {
			h.IdentityFiles = append(h.IdentityFiles, expandHome(f))
		}
	}
	return h, nil
}

// ResolveHops expands the jump list, including a ProxyJump of the first hop.
func ResolveHops(jumps []string) ([]Hop, error) {
	var out []Hop
	seen := map[string]bool{}
	var expand func(j string, depth int) error
	expand = func(j string, depth int) error {
		if depth > 8 {
			return fmt.Errorf("ProxyJump loop at %q", j)
		}
		h, err := ParseHop(j)
		if err != nil {
			return err
		}
		if pj := cfgGet(h.Alias, "ProxyJump"); pj != "" && pj != "none" && !seen[h.Alias] {
			seen[h.Alias] = true
			for _, p := range strings.Split(pj, ",") {
				if err := expand(p, depth+1); err != nil {
					return err
				}
			}
		}
		out = append(out, h)
		return nil
	}
	for i, j := range jumps {
		if i == 0 {
			if err := expand(j, 0); err != nil {
				return nil, err
			}
			continue
		}
		h, err := ParseHop(j)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// Dial connects through every hop. Close the chain when done.
func Dial(ctx context.Context, cfg Config) (*Chain, error) {
	hops, err := ResolveHops(cfg.Jumps)
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if len(hops) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no jump hosts given")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	ch := &Chain{Hops: hops}
	for i, h := range hops {
		var conn net.Conn
		if i == 0 {
			d := net.Dialer{Timeout: cfg.Timeout}
			conn, err = d.DialContext(ctx, "tcp", h.addr())
		} else {
			conn, err = dialVia(ctx, ch.clients[i-1], h.addr())
		}
		if err != nil {
			ch.Close()
			return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable,
				fmt.Errorf("jump host %s: %w", h, err)).WithDetail("jumpHost", h.String())
		}
		client, err := handshake(ctx, conn, h, cfg)
		if err != nil {
			conn.Close()
			ch.Close()
			return nil, err
		}
		ch.clients = append(ch.clients, client)
	}
	return ch, nil
}

func dialVia(ctx context.Context, c *ssh.Client, addr string) (net.Conn, error) {
	return c.DialContext(ctx, "tcp", addr)
}

// DialContext opens a TCP connection to addr from the last jump host.
func (c *Chain) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if len(c.clients) == 0 {
		return nil, errors.New("ssh chain closed")
	}
	return c.clients[len(c.clients)-1].DialContext(ctx, network, addr)
}

// Close closes all connections, last hop first.
func (c *Chain) Close() error {
	for i := len(c.clients) - 1; i >= 0; i-- {
		c.clients[i].Close()
	}
	c.clients = nil
	return nil
}

func handshake(ctx context.Context, conn net.Conn, h Hop, cfg Config) (*ssh.Client, error) {
	hk, algos, err := hostKeyCallback(cfg, h)
	if err != nil {
		return nil, err
	}
	auth, enc := authMethods(cfg, h)
	cc := &ssh.ClientConfig{
		User:              h.User,
		Auth:              auth,
		HostKeyCallback:   hk.check,
		HostKeyAlgorithms: algos,
		Timeout:           cfg.Timeout,
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(cfg.Timeout))
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, h.addr(), cc)
	if err != nil {
		if hk.err != nil {
			return nil, hk.err
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			if len(enc) > 0 {
				return nil, apperr.New(apperr.ClassConnection, apperr.CodePassphraseRequired,
					"jump host %s: authentication failed; key %s needs a passphrase", h, enc[0]).
					WithDetail("jumpHost", h.String()).WithDetail("keyFile", enc[0]).
					WithHint("set CANOPY_SSH_PASSPHRASE or enter it when asked")
			}
			return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeSSHAuth,
				fmt.Errorf("jump host %s: %w", h, err)).WithDetail("jumpHost", h.String()).
				WithHint("load the key into the SSH agent, pass --ssh-identity, or set CANOPY_SSH_PASSWORD")
		}
		return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeSSH, fmt.Errorf("jump host %s: %w", h, err)).
			WithDetail("jumpHost", h.String())
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

// authMethods returns the auth methods for a hop, and the encrypted key files that
// were skipped because their passphrase isn't known.
func authMethods(cfg Config, h Hop) ([]ssh.AuthMethod, []string) {
	var methods []ssh.AuthMethod
	var signers []ssh.Signer
	if !cfg.NoAgent {
		if ag, err := dialAgent(); err == nil {
			if s, err := agent.NewClient(ag).Signers(); err == nil {
				signers = append(signers, s...)
			}
		}
	}
	var encrypted []string
	files := append(append([]string{}, cfg.IdentityFiles...), h.IdentityFiles...)
	files = append(files, defaultIdentities()...)
	seen := map[string]bool{}
	for _, f := range files {
		f = expandHome(f)
		if seen[f] {
			continue
		}
		seen[f] = true
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s, err := ssh.ParsePrivateKey(data)
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			pp, ok := cfg.Passphrases[f]
			if !ok && cfg.DefaultPassphrase != "" {
				pp, ok = cfg.DefaultPassphrase, true
			}
			if ok {
				s, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(pp))
				if err != nil {
					encrypted = append(encrypted, f) // wrong passphrase
					continue
				}
			} else {
				encrypted = append(encrypted, f)
				continue
			}
		}
		if err == nil {
			signers = append(signers, s)
		}
	}
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if cfg.Password != "" {
		pw := cfg.Password
		methods = append(methods, ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}))
	}
	return methods, encrypted
}

// hostKeyChecker records why a host key was rejected, so the caller gets a
// structured error instead of the ssh library's generic one.
type hostKeyChecker struct {
	kh   ssh.HostKeyCallback
	cfg  Config
	file string
	hop  Hop
	err  *apperr.Error
}

func (c *hostKeyChecker) check(hostname string, remote net.Addr, key ssh.PublicKey) error {
	fp := ssh.FingerprintSHA256(key)
	err := c.kh(hostname, remote, key)
	if err == nil {
		return nil
	}
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) && len(ke.Want) > 0 {
		c.err = apperr.New(apperr.ClassConnection, apperr.CodeHostKeyMismatch,
			"jump host %s: host key %s does not match known_hosts", c.hop, fp).
			WithDetail("jumpHost", c.hop.String()).WithDetail("fingerprint", fp).
			WithDetail("keyType", key.Type()).
			WithHint("the host key changed; verify it out of band and update " + c.file)
		return c.err
	}
	for _, a := range c.cfg.AcceptHostKeys {
		if a == fp {
			if c.cfg.SaveAcceptedHostKeys {
				appendKnownHost(c.file, hostname, key)
			}
			return nil
		}
	}
	c.err = apperr.New(apperr.ClassConnection, apperr.CodeHostKeyUnknown,
		"jump host %s: unknown host key %s %s", c.hop, key.Type(), fp).
		WithDetail("jumpHost", c.hop.String()).WithDetail("fingerprint", fp).
		WithDetail("keyType", key.Type()).
		WithHint("verify the fingerprint, then accept it (--accept-host-key " + fp + ")")
	return c.err
}

func hostKeyCallback(cfg Config, h Hop) (*hostKeyChecker, []string, error) {
	file := cfg.KnownHostsFile
	if file == "" {
		home, _ := os.UserHomeDir()
		file = filepath.Join(home, ".ssh", "known_hosts")
	}
	var kh ssh.HostKeyCallback
	if _, err := os.Stat(file); err == nil {
		kh, err = knownhosts.New(file)
		if err != nil {
			return nil, nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, fmt.Errorf("known_hosts %s: %w", file, err))
		}
	} else {
		kh = func(string, net.Addr, ssh.PublicKey) error { return &knownhosts.KeyError{} }
	}
	return &hostKeyChecker{kh: kh, cfg: cfg, file: file, hop: h}, knownAlgorithms(kh, h), nil
}

// knownAlgorithms returns the host key algorithms of the keys known for h, so the
// server is asked for a key type we can verify. It asks the callback with a dummy
// key; the KeyError lists the known keys.
func knownAlgorithms(kh ssh.HostKeyCallback, h Hop) []string {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dummy, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil
	}
	addr := &net.TCPAddr{IP: net.ParseIP(h.Host), Port: h.Port}
	err = kh(h.addr(), addr, dummy.PublicKey())
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) || len(ke.Want) == 0 {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	add := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				algos = append(algos, x)
			}
		}
	}
	for _, k := range ke.Want {
		switch t := k.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return algos
}

func appendKnownHost(file, hostname string, key ssh.PublicKey) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
}

func defaultIdentities() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		out = append(out, filepath.Join(home, ".ssh", n))
	}
	return out
}

func isDefaultIdentity(f string) bool {
	// ssh_config returns the OpenSSH default "~/.ssh/identity" when none is set.
	return f == "~/.ssh/identity"
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func currentUser() string {
	for _, k := range []string{"USER", "USERNAME"} {
		if u := os.Getenv(k); u != "" {
			return u
		}
	}
	return "root"
}
