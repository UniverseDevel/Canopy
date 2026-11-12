// Package app is the service layer shared by the CLI and the web UI.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/sshx"
)

// Target is a connection request as the UI and CLI send it.
type Target struct {
	Address    string `json:"address"`
	Insecure   bool   `json:"insecure,omitempty"`
	SkipVerify bool   `json:"skipVerify,omitempty"`
	CAFile     string `json:"caFile,omitempty"`
	CertFile   string `json:"certFile,omitempty"`
	KeyFile    string `json:"keyFile,omitempty"`
	ServerName string `json:"serverName,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"` // never logged or echoed
	TimeoutSec int    `json:"timeoutSec,omitempty"`

	Jumps          []string          `json:"jumps,omitempty"`
	IdentityFiles  []string          `json:"identityFiles,omitempty"`
	Passphrases    map[string]string `json:"passphrases,omitempty"` // key file -> passphrase
	Passphrase     string            `json:"passphrase,omitempty"`  // for any encrypted key
	SSHPassword    string            `json:"sshPassword,omitempty"`
	AcceptHostKeys []string          `json:"acceptHostKeys,omitempty"`
	SaveHostKeys   bool              `json:"saveHostKeys,omitempty"`
}

// Options converts the target into connection options.
func (t Target) Options() conn.Options {
	o := conn.Options{
		Address: t.Address, Insecure: t.Insecure, SkipVerify: t.SkipVerify,
		CAFile: t.CAFile, CertFile: t.CertFile, KeyFile: t.KeyFile, ServerName: t.ServerName,
		Username: t.Username, Password: t.Password,
		Timeout: time.Duration(t.TimeoutSec) * time.Second,
	}
	if len(t.Jumps) > 0 {
		o.SSH = &sshx.Config{
			Jumps: t.Jumps, IdentityFiles: t.IdentityFiles, Passphrases: t.Passphrases, DefaultPassphrase: t.Passphrase,
			Password: t.SSHPassword, AcceptHostKeys: t.AcceptHostKeys, SaveAcceptedHostKeys: t.SaveHostKeys,
		}
	}
	return o
}

// Session is an open connection.
type Session struct {
	ID           string            `json:"id"`
	Address      string            `json:"address"`
	Jumps        []string          `json:"jumps,omitempty"`
	Hops         []string          `json:"hops,omitempty"`
	Capabilities conn.Capabilities `json:"capabilities"`
	Encoding     string            `json:"encoding"` // default encoding for reads
	ConnectMs    int64             `json:"connectMs"`
	Opened       time.Time         `json:"opened"`
	conn         *conn.Conn
}

// Service holds sessions and the loaded schema.
type Service struct {
	Log *slog.Logger

	mu       sync.Mutex
	sessions map[string]*Session
	tree     *schema.Tree
}

// New creates a service.
func New(log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{Log: log, sessions: map[string]*Session{}}
}

// Connect opens a session.
func (s *Service) Connect(ctx context.Context, t Target) (*Session, error) {
	start := time.Now()
	s.Log.Info("connect", "address", t.Address, "jumps", t.Jumps, "insecure", t.Insecure, "skipVerify", t.SkipVerify)
	c, caps, err := conn.Dial(ctx, t.Options())
	if err != nil {
		ae := apperr.As(err)
		s.Log.Warn("connect failed", "address", t.Address, "code", ae.Code, "grpcCode", ae.GRPCCode, "error", ae.Message)
		return nil, err
	}
	cp := conn.ConvertCapabilities(caps)
	sess := &Session{
		ID: newID(), Address: t.Address, Jumps: t.Jumps, Capabilities: cp,
		Encoding: conn.PreferredEncoding(cp, ""), ConnectMs: time.Since(start).Milliseconds(),
		Opened: time.Now(), conn: c,
	}
	for _, h := range c.Hops {
		sess.Hops = append(sess.Hops, h.String())
	}
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	s.Log.Info("connected", "session", sess.ID, "address", t.Address, "models", len(cp.Models), "encodings", cp.Encodings, "ms", sess.ConnectMs)
	return sess, nil
}

// Session returns an open session.
func (s *Service) Session(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no session %q; connect first", id)
	}
	return sess, nil
}

// Sessions lists open sessions.
func (s *Service) Sessions() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, x := range s.sessions {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Opened.Before(out[j].Opened) })
	return out
}

// Disconnect closes a session.
func (s *Service) Disconnect(id string) {
	s.mu.Lock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if sess != nil {
		sess.conn.Close()
		s.Log.Info("disconnected", "session", id)
	}
}

// Close closes everything.
func (s *Service) Close() {
	for _, x := range s.Sessions() {
		s.Disconnect(x.ID)
	}
}

// Get reads paths in a session.
func (s *Service) Get(ctx context.Context, id string, r live.GetRequest) (*live.Result, error) {
	sess, err := s.Session(id)
	if err != nil {
		return nil, err
	}
	if r.Encoding == "" {
		r.Encoding = sess.Encoding
	}
	s.Log.Info("get", "session", id, "paths", r.Paths, "type", r.Type, "encoding", r.Encoding)
	res, err := live.Get(ctx, sess.conn, r)
	if err != nil {
		ae := apperr.As(err)
		s.Log.Warn("get failed", "session", id, "code", ae.Code, "grpcCode", ae.GRPCCode, "error", ae.Message)
		return nil, err
	}
	s.Log.Info("get done", "session", id, "updates", res.UpdateCount, "leaves", res.LeafCount, "bytes", res.SizeBytes, "ms", res.ElapsedMs)
	return res, nil
}

// Generate produces gNMIc commands for a session (or a target without session).
func (s *Service) Generate(id string, t *Target, r generate.Request) (*generate.Output, error) {
	if id != "" {
		sess, err := s.Session(id)
		if err != nil {
			return nil, err
		}
		r.Target = sess.conn.Opts
		r.Hops = sess.conn.Hops
	} else if t != nil {
		r.Target = t.Options()
		if len(t.Jumps) > 0 {
			hops, err := sshx.ResolveHops(t.Jumps)
			if err != nil {
				return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
			}
			r.Hops = hops
		}
	}
	r.Target.Password = ""
	return generate.Gnmic(r)
}

// LoadSchema loads YANG from directories and makes it the current schema.
func (s *Service) LoadSchema(dirs []string, opt schema.LoadOptions) (*schema.Tree, error) {
	s.Log.Info("load schema", "dirs", dirs, "modules", opt.Modules)
	t, err := schema.Load(dirs, opt)
	if err != nil {
		ae := apperr.As(err)
		s.Log.Warn("load schema failed", "code", ae.Code, "error", ae.Message)
		return nil, err
	}
	s.mu.Lock()
	s.tree = t
	s.mu.Unlock()
	s.Log.Info("schema loaded", "files", t.Files, "modules", len(t.Modules), "nodes", len(t.Nodes), "ms", t.LoadMs, "warnings", len(t.Warnings))
	return t, nil
}

// Tree returns the current schema.
func (s *Service) Tree() (*schema.Tree, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tree == nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no YANG models loaded")
	}
	return s.tree, nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
