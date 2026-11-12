// Package connections stores named connection presets in the user's config
// directory. Passwords are never stored; a preset can name the environment
// variable that holds its password instead.
package connections

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"gopkg.in/yaml.v3"
)

// Connection is a saved preset.
type Connection struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	Address    string `yaml:"address" json:"address"`
	Insecure   bool   `yaml:"insecure,omitempty" json:"insecure,omitempty"`
	SkipVerify bool   `yaml:"skipVerify,omitempty" json:"skipVerify,omitempty"`
	CAFile     string `yaml:"caFile,omitempty" json:"caFile,omitempty"`
	CertFile   string `yaml:"certFile,omitempty" json:"certFile,omitempty"`
	KeyFile    string `yaml:"keyFile,omitempty" json:"keyFile,omitempty"`
	ServerName string `yaml:"serverName,omitempty" json:"serverName,omitempty"`
	Username   string `yaml:"username,omitempty" json:"username,omitempty"`
	// PasswordEnv names the environment variable with this device's password
	// (default GNMI_PASSWORD).
	PasswordEnv string `yaml:"passwordEnv,omitempty" json:"passwordEnv,omitempty"`
	TimeoutSec  int    `yaml:"timeoutSec,omitempty" json:"timeoutSec,omitempty"`

	Jumps         []string `yaml:"jumps,omitempty" json:"jumps,omitempty"`
	IdentityFiles []string `yaml:"identityFiles,omitempty" json:"identityFiles,omitempty"`

	// Working context
	Encoding string   `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	YangDirs []string `yaml:"yangDirs,omitempty" json:"yangDirs,omitempty"`
	Paths    []string `yaml:"paths,omitempty" json:"paths,omitempty"`

	// LastSeen is updated after each successful connection.
	LastSeen *Seen `yaml:"lastSeen,omitempty" json:"lastSeen,omitempty"`

	// Source is the file the preset came from; ReadOnly is set for presets from
	// extra files (CANOPY_CONNECTIONS), which Canopy never writes.
	Source   string `yaml:"-" json:"source,omitempty"`
	ReadOnly bool   `yaml:"-" json:"readOnly,omitempty"`
}

// Seen records what the device reported the last time.
type Seen struct {
	Time        time.Time `yaml:"time" json:"time"`
	GNMIVersion string    `yaml:"gnmiVersion,omitempty" json:"gnmiVersion,omitempty"`
	Encodings   []string  `yaml:"encodings,omitempty" json:"encodings,omitempty"`
	Models      int       `yaml:"models" json:"models"`
}

type file struct {
	Version     int          `yaml:"version"`
	Connections []Connection `yaml:"connections"`
}

// Store reads and writes the presets file. Extra files are read-only (for
// example a team or lab file in a repository); presets in Path win on equal
// names.
type Store struct {
	Path  string
	Extra []string
	mu    sync.Mutex
}

// DefaultPath is <user config dir>/canopy/connections.yaml, or
// $CANOPY_CONFIG_DIR/connections.yaml.
func DefaultPath() string {
	dir := os.Getenv("CANOPY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "canopy")
	}
	return filepath.Join(dir, "connections.yaml")
}

// Open returns a store for path (DefaultPath if empty) with the extra read-only
// files listed in $CANOPY_CONNECTIONS (separated like PATH).
func Open(path string) *Store {
	if path == "" {
		path = DefaultPath()
	}
	s := &Store{Path: path}
	for _, p := range filepath.SplitList(os.Getenv("CANOPY_CONNECTIONS")) {
		if p = strings.TrimSpace(p); p != "" {
			s.Extra = append(s.Extra, p)
		}
	}
	return s
}

func (s *Store) read() (*file, error) {
	return readFile(s.Path)
}

func readFile(path string) (*file, error) {
	f := &file{Version: 1}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "%s: %v", path, err)
	}
	return f, nil
}

// readExtra reads a read-only file. Relative file paths in it (YANG folders, CA,
// certificates, keys) are resolved against the file's folder.
func readExtra(path string) ([]Connection, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "connections file %s (from CANOPY_CONNECTIONS) not found", path)
	}
	f, err := readFile(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") || strings.HasPrefix(p, `\\`) {
			return p
		}
		return filepath.Join(dir, p)
	}
	for i := range f.Connections {
		c := &f.Connections[i]
		c.Source, c.ReadOnly = path, true
		c.CAFile, c.CertFile, c.KeyFile = abs(c.CAFile), abs(c.CertFile), abs(c.KeyFile)
		for j := range c.YangDirs {
			c.YangDirs[j] = abs(c.YangDirs[j])
		}
		for j := range c.IdentityFiles {
			c.IdentityFiles[j] = abs(c.IdentityFiles[j])
		}
	}
	return f.Connections, nil
}

func (s *Store) write(f *file) error {
	sort.Slice(f.Connections, func(i, j int) bool { return f.Connections[i].Name < f.Connections[j].Name })
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(f)
	enc.Close()
	data := buf.Bytes()
	if err != nil {
		return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	tmp := s.Path + ".tmp"
	hdr := "# Canopy connection presets. Passwords are never stored here.\n"
	if err := os.WriteFile(tmp, append([]byte(hdr), data...), 0o600); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		os.Remove(tmp)
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return nil
}

// List returns all presets sorted by name.
func (s *Store) List() ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := []Connection{}
	seen := map[string]bool{}
	for _, c := range f.Connections {
		c.Source = s.Path
		out = append(out, c)
		seen[strings.ToLower(c.Name)] = true
	}
	for _, x := range s.Extra {
		cs, err := readExtra(x)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if !seen[strings.ToLower(c.Name)] {
				seen[strings.ToLower(c.Name)] = true
				out = append(out, c)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns a preset by name.
func (s *Store) Get(name string) (*Connection, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.EqualFold(list[i].Name, name) {
			return &list[i], nil
		}
	}
	return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no saved connection %q (see 'canopy conn list')", name)
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,63}$`)
var validEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks a preset.
func (c *Connection) Validate() error {
	if !validName.MatchString(c.Name) {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage,
			"connection name %q: use 1-64 letters, digits, space, '.', '_', '/', '-'", c.Name)
	}
	if c.Address == "" {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "connection %q needs an address", c.Name)
	}
	if c.PasswordEnv != "" && !validEnv.MatchString(c.PasswordEnv) {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "passwordEnv %q is not a valid variable name", c.PasswordEnv)
	}
	return nil
}

// Put saves c, replacing a preset with the same name (case-insensitive). The
// previous LastSeen is kept unless c has one.
func (s *Store) Put(c Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	c.Source, c.ReadOnly = "", false
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	for i := range f.Connections {
		if strings.EqualFold(f.Connections[i].Name, c.Name) {
			if c.LastSeen == nil {
				c.LastSeen = f.Connections[i].LastSeen
			}
			f.Connections[i] = c
			return s.write(f)
		}
	}
	f.Connections = append(f.Connections, c)
	return s.write(f)
}

// Delete removes a preset.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	for i := range f.Connections {
		if strings.EqualFold(f.Connections[i].Name, name) {
			f.Connections = append(f.Connections[:i], f.Connections[i+1:]...)
			return s.write(f)
		}
	}
	for _, x := range s.Extra {
		cs, _ := readExtra(x)
		for _, c := range cs {
			if strings.EqualFold(c.Name, name) {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "connection %q comes from the read-only file %s; edit that file instead", name, x)
			}
		}
	}
	return apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no saved connection %q", name)
}

// Touch records a successful connection.
func (s *Store) Touch(name string, seen Seen) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	for i := range f.Connections {
		if strings.EqualFold(f.Connections[i].Name, name) {
			f.Connections[i].LastSeen = &seen
			return s.write(f)
		}
	}
	return nil // read-only presets keep no history
}
