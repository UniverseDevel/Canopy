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
	// Models is the model set, SOURCE@VERSION (e.g. srlinux@v26.7.1); empty means
	// detect from the device (unless YangDirs is set).
	Models string `yaml:"models,omitempty" json:"models,omitempty"`
	// OperatorRepo is the network-operator checkout (a fork, for example)
	// whose test fixtures go with this device; empty: Canopy's default.
	OperatorRepo string `yaml:"operatorRepo,omitempty" json:"operatorRepo,omitempty"`

	// LastSeen is updated after each successful connection.
	LastSeen *Seen `yaml:"lastSeen,omitempty" json:"lastSeen,omitempty"`

	// Source is the file the preset came from (and is saved back to).
	Source string `yaml:"-" json:"source,omitempty"`
	// PasswordStored says where a password is kept for it, if anywhere
	// ("keychain" or "file"); filled in for the UI, never saved.
	PasswordStored string `yaml:"-" json:"passwordStored,omitempty"`
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

// Store reads and writes the presets files. Every preset is edited and
// deleted in the file it came from; new presets go to Path. On equal names the
// earlier file wins (Path first).
type Store struct {
	Path  string   // the main file: new presets are saved here
	Extra []string // more files, edited in place like Path
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

// ExtraFiles are the connections files given with --connections; Open puts
// them before those from $CANOPY_CONNECTIONS.
var ExtraFiles []string

// Open returns a store for path. With an empty path it uses the files from
// --connections and $CANOPY_CONNECTIONS (separated like PATH), the first one
// being where new presets go, or DefaultPath when there are none.
func Open(path string) *Store {
	files := append([]string{}, ExtraFiles...)
	for _, p := range filepath.SplitList(os.Getenv("CANOPY_CONNECTIONS")) {
		if p = strings.TrimSpace(p); p != "" {
			files = append(files, p)
		}
	}
	if path == "" {
		if len(files) == 0 {
			return &Store{Path: DefaultPath()}
		}
		path, files = files[0], files[1:]
	}
	return &Store{Path: path, Extra: files}
}

func (s *Store) files() []string { return append([]string{s.Path}, s.Extra...) }

// readFile reads a presets file (a missing one is empty). Relative file paths
// in it (YANG folders, CA, certificates, keys) are resolved against the
// file's folder, and Source is set to the file.
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
	dir := fileDir(path)
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") || strings.HasPrefix(p, `\\`) {
			return p
		}
		return filepath.Join(dir, p)
	}
	for i := range f.Connections {
		c := &f.Connections[i]
		c.Source = path
		mapPaths(c, abs)
	}
	return f, nil
}

func fileDir(path string) string {
	dir := filepath.Dir(path)
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	return dir
}

func mapPaths(c *Connection, f func(string) string) {
	c.CAFile, c.CertFile, c.KeyFile = f(c.CAFile), f(c.CertFile), f(c.KeyFile)
	c.OperatorRepo = f(c.OperatorRepo)
	for j := range c.YangDirs {
		c.YangDirs[j] = f(c.YangDirs[j])
	}
	for j := range c.IdentityFiles {
		c.IdentityFiles[j] = f(c.IdentityFiles[j])
	}
}

// writeFile saves f to path. Paths inside the file's folder are written
// relative to it, so a file kept next to its CA and keys can move with them.
func writeFile(path string, f *file) error {
	dir := fileDir(path)
	rel := func(p string) string {
		if !filepath.IsAbs(p) {
			return p
		}
		if r, err := filepath.Rel(dir, p); err == nil && r != "." && !strings.HasPrefix(r, "..") && !filepath.IsAbs(r) {
			return filepath.ToSlash(r)
		}
		return p
	}
	out := &file{Version: f.Version, Connections: make([]Connection, len(f.Connections))}
	for i, c := range f.Connections {
		c.Source = ""
		c.YangDirs = append([]string(nil), c.YangDirs...)
		c.IdentityFiles = append([]string(nil), c.IdentityFiles...)
		mapPaths(&c, rel)
		out.Connections[i] = c
	}
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].Name < out.Connections[j].Name })
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(out)
	enc.Close()
	data := buf.Bytes()
	if err != nil {
		return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	tmp := path + ".tmp"
	hdr := "# Canopy connection presets. Passwords are never stored here.\n"
	if err := os.WriteFile(tmp, append([]byte(hdr), data...), 0o600); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return nil
}

// find returns the file holding the preset name (the first one, as in List),
// the parsed file and the preset's index; i is -1 if none has it.
func (s *Store) find(name string) (path string, f *file, i int, err error) {
	for _, p := range s.files() {
		f, err := readFile(p)
		if err != nil {
			return "", nil, -1, err
		}
		for i := range f.Connections {
			if strings.EqualFold(f.Connections[i].Name, name) {
				return p, f, i, nil
			}
		}
	}
	return "", nil, -1, nil
}

// List returns all presets sorted by name.
func (s *Store) List() ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Connection{}
	seen := map[string]bool{}
	for k, p := range s.files() {
		if k > 0 {
			if _, err := os.Stat(p); err != nil {
				return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "connections file %s not found", p)
			}
		}
		f, err := readFile(p)
		if err != nil {
			return nil, err
		}
		for _, c := range f.Connections {
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

// Put saves c, replacing a preset with the same name (case-insensitive) in
// the file that has it. A new preset goes to c.Source if that is one of the
// store's files (a renamed preset stays in its file), else to Path. The
// previous LastSeen is kept unless c has one.
func (s *Store) Put(c Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, f, i, err := s.find(c.Name)
	if err != nil {
		return err
	}
	if i >= 0 {
		if c.LastSeen == nil {
			c.LastSeen = f.Connections[i].LastSeen
		}
		f.Connections[i] = c
		return writeFile(path, f)
	}
	path = s.Path
	for _, p := range s.files() {
		if c.Source != "" && sameFile(p, c.Source) {
			path = p
		}
	}
	if f, err = readFile(path); err != nil {
		return err
	}
	f.Connections = append(f.Connections, c)
	return writeFile(path, f)
}

func sameFile(a, b string) bool {
	x, err1 := filepath.Abs(a)
	y, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(x, y)
}

// Delete removes a preset from its file.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, f, i, err := s.find(name)
	if err != nil {
		return err
	}
	if i < 0 {
		return apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no saved connection %q", name)
	}
	f.Connections = append(f.Connections[:i], f.Connections[i+1:]...)
	return writeFile(path, f)
}

// Touch records a successful connection.
func (s *Store) Touch(name string, seen Seen) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, f, i, err := s.find(name)
	if err != nil || i < 0 {
		return err
	}
	f.Connections[i].LastSeen = &seen
	return writeFile(path, f)
}
