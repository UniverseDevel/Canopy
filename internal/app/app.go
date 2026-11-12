// Package app is the service layer shared by the CLI and the web UI.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/connections"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/models"
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

	// Preset is the saved connection this target came from, if any. Its
	// passwordEnv, encoding and YANG folders apply, and its lastSeen is updated.
	Preset string `json:"preset,omitempty"`

	// Set server-side only (operator mode): CA and client certificate from
	// Kubernetes Secrets, and the label shown for the session.
	CAPEM   []byte `json:"-"`
	CertPEM []byte `json:"-"`
	KeyPEM  []byte `json:"-"`
	Label   string `json:"-"`
}

// TargetFrom converts a preset into a target (without secrets).
func TargetFrom(c *connections.Connection) Target {
	return Target{
		Address: c.Address, Insecure: c.Insecure, SkipVerify: c.SkipVerify,
		CAFile: c.CAFile, CertFile: c.CertFile, KeyFile: c.KeyFile, ServerName: c.ServerName,
		Username: c.Username, TimeoutSec: c.TimeoutSec,
		Jumps: c.Jumps, IdentityFiles: c.IdentityFiles, Preset: c.Name,
	}
}

// PresetFrom converts a target into a preset (secrets are dropped).
func PresetFrom(name string, t Target) connections.Connection {
	return connections.Connection{
		Name: name, Address: t.Address, Insecure: t.Insecure, SkipVerify: t.SkipVerify,
		CAFile: t.CAFile, CertFile: t.CertFile, KeyFile: t.KeyFile, ServerName: t.ServerName,
		Username: t.Username, TimeoutSec: t.TimeoutSec, Jumps: t.Jumps, IdentityFiles: t.IdentityFiles,
	}
}

// Options converts the target into connection options.
func (t Target) Options() conn.Options {
	o := conn.Options{
		Address: t.Address, Insecure: t.Insecure, SkipVerify: t.SkipVerify,
		CAFile: t.CAFile, CertFile: t.CertFile, KeyFile: t.KeyFile, ServerName: t.ServerName,
		CAPEM: t.CAPEM, CertPEM: t.CertPEM, KeyPEM: t.KeyPEM,
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
	Label        string            `json:"label"`            // preset name or address
	Preset       string            `json:"preset,omitempty"` // saved connection name
	YangDirs     []string          `json:"yangDirs,omitempty"`
	Paths        []string          `json:"paths,omitempty"`  // saved paths of the preset
	Models       string            `json:"models,omitempty"` // SOURCE@VERSION from the preset
	Device       *DeviceRef        `json:"device,omitempty"` // set when connected through the operator
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

	// Presets are the saved connections.
	Presets *connections.Store
	// Models downloads YANG model sets from GitHub.
	Models *models.Manager

	mu       sync.Mutex
	sessions map[string]*Session
	tree     *schema.Tree
	trees    map[string]*schema.Tree // by folder set, so switching devices is instant
}

// New creates a service with the default presets store.
func New(log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{Log: log, sessions: map[string]*Session{}, trees: map[string]*schema.Tree{},
		Presets: connections.Open(""), Models: models.NewManager()}
}

// Connect opens a session. Several sessions can be open at once. A missing
// username or password is taken from the environment: the preset's passwordEnv,
// else GNMI_USERNAME and GNMI_PASSWORD.
func (s *Service) Connect(ctx context.Context, t Target) (*Session, error) {
	start := time.Now()
	var preset *connections.Connection
	if t.Preset != "" {
		p, err := s.Presets.Get(t.Preset)
		if err != nil {
			return nil, err
		}
		preset = p
	}
	if t.Username == "" {
		t.Username = os.Getenv("GNMI_USERNAME")
	}
	if t.Password == "" {
		env := "GNMI_PASSWORD"
		if preset != nil && preset.PasswordEnv != "" {
			env = preset.PasswordEnv
		}
		t.Password = os.Getenv(env)
	}
	s.Log.Info("connect", "address", t.Address, "preset", t.Preset, "jumps", t.Jumps, "insecure", t.Insecure, "skipVerify", t.SkipVerify)
	c, caps, err := conn.Dial(ctx, t.Options())
	if err != nil {
		ae := apperr.As(err)
		s.Log.Warn("connect failed", "address", t.Address, "code", ae.Code, "grpcCode", ae.GRPCCode, "error", ae.Message)
		return nil, err
	}
	cp := conn.ConvertCapabilities(caps)
	sess := &Session{
		ID: newID(), Label: firstNonEmpty(t.Label, t.Address), Address: t.Address, Jumps: t.Jumps, Capabilities: cp,
		Encoding: conn.PreferredEncoding(cp, ""), ConnectMs: time.Since(start).Milliseconds(),
		Opened: time.Now(), conn: c,
	}
	if preset != nil {
		sess.Label, sess.Preset, sess.YangDirs, sess.Paths = preset.Name, preset.Name, preset.YangDirs, preset.Paths
		sess.Models = preset.Models
		if preset.Encoding != "" {
			sess.Encoding = preset.Encoding
		}
		if err := s.Presets.Touch(preset.Name, connections.Seen{Time: time.Now().UTC().Truncate(time.Second),
			GNMIVersion: cp.GNMIVersion, Encodings: cp.Encodings, Models: len(cp.Models)}); err != nil {
			s.Log.Warn("could not update preset", "preset", preset.Name, "error", err.Error())
		}
	}
	for _, h := range c.Hops {
		sess.Hops = append(sess.Hops, h.String())
	}
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	s.Log.Info("connected", "session", sess.ID, "address", t.Address, "preset", t.Preset, "models", len(cp.Models), "encodings", cp.Encodings, "ms", sess.ConnectMs)
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
	key := strings.Join(dirs, "\x00") + "\x01" + strings.Join(opt.Modules, "\x00") + "\x01" + strings.Join(opt.SearchDirs, "\x00")
	s.mu.Lock()
	if t, ok := s.trees[key]; ok && !opt.Force {
		s.tree = t
		s.mu.Unlock()
		s.Log.Info("schema from cache", "dirs", dirs)
		return t, nil
	}
	s.mu.Unlock()
	s.Log.Info("load schema", "dirs", dirs, "modules", opt.Modules)
	t, err := schema.Load(dirs, opt)
	if err != nil {
		ae := apperr.As(err)
		s.Log.Warn("load schema failed", "code", ae.Code, "error", ae.Message)
		return nil, err
	}
	s.mu.Lock()
	s.tree = t
	s.trees[key] = t
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

// ModelResult is the outcome of loading a model set.
type ModelResult struct {
	Detection *models.Detection `json:"detection,omitempty"`
	Set       *models.Set       `json:"set,omitempty"`
	Schema    *schema.Summary   `json:"schema,omitempty"`
}

// LoadModels downloads (or reuses) a model set and makes it the current schema.
func (s *Service) LoadModels(ctx context.Context, source, version string, progress func(string)) (*ModelResult, error) {
	s.Log.Info("load models", "source", source, "version", version)
	set, err := s.Models.Fetch(ctx, source, version, progress)
	if err != nil {
		return nil, err
	}
	t, err := s.LoadSchema(set.Dirs, schema.LoadOptions{SearchDirs: set.SearchDirs})
	if err != nil {
		return nil, err
	}
	sum := t.Summary()
	return &ModelResult{Set: set, Schema: &sum}, nil
}

// DetectModels works out the model source and version for a session.
func (s *Service) DetectModels(ctx context.Context, id string) (*models.Detection, error) {
	sess, err := s.Session(id)
	if err != nil {
		return nil, err
	}
	read := func(ctx context.Context, path string) (string, error) {
		res, err := live.Get(ctx, sess.conn, live.GetRequest{Paths: []string{path}, Encoding: sess.Encoding})
		if err != nil {
			return "", err
		}
		for _, u := range res.Flat {
			if v, ok := u.Value.(string); ok {
				return v, nil
			}
		}
		return "", apperr.New(apperr.ClassDevice, apperr.CodeNotFound, "no value at %s", path)
	}
	d := models.Detect(ctx, sess.Capabilities, read)
	if err := s.Models.Resolve(ctx, &d); err != nil {
		d.Reason += "; listing versions failed: " + apperr.As(err).Message
	}
	s.Log.Info("models detected", "session", id, "source", d.Source, "deviceVersion", d.DeviceVersion, "version", d.Version, "exact", d.Exact)
	return &d, nil
}

// AutoModels detects the model set for a session and loads it when a version is
// known.
func (s *Service) AutoModels(ctx context.Context, id string, progress func(string)) (*ModelResult, error) {
	d, err := s.DetectModels(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.Source == "" || d.Version == "" {
		return &ModelResult{Detection: d}, nil
	}
	res, err := s.LoadModels(ctx, d.Source, d.Version, progress)
	if err != nil {
		return &ModelResult{Detection: d}, err
	}
	res.Detection = d
	return res, nil
}

// MatchReport compares a session's Capabilities with the current schema.
func (s *Service) MatchReport(id string) (*models.MatchReport, error) {
	sess, err := s.Session(id)
	if err != nil {
		return nil, err
	}
	t, err := s.Tree()
	if err != nil {
		return nil, err
	}
	r := models.Report(sess.Capabilities, t.Modules)
	return &r, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
