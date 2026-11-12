// Package snapshot stores device reads with their provenance and compares them.
package snapshot

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/live"
)

// Meta is a snapshot's provenance.
type Meta struct {
	Name     string    `json:"name"`
	Device   string    `json:"device"`  // preset name or address
	Address  string    `json:"address"` // host:port
	Time     time.Time `json:"time"`
	Paths    []string  `json:"paths"`
	Type     string    `json:"type"`
	Encoding string    `json:"encoding"`
	Models   string    `json:"models,omitempty"` // e.g. srlinux@v26.7.1
	Canopy   string    `json:"canopy"`
	Leaves   int       `json:"leaves"`
	Note     string    `json:"note,omitempty"`
}

// Snapshot is a stored read.
type Snapshot struct {
	Meta Meta          `json:"meta"`
	Flat []live.Update `json:"flat"`
	Data any           `json:"data"`
}

// Store keeps snapshots as gzipped JSON files.
type Store struct{ Dir string }

// DefaultDir is <user config dir>/canopy/snapshots (or $CANOPY_CONFIG_DIR/snapshots).
func DefaultDir() string {
	dir := os.Getenv("CANOPY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "canopy")
	}
	return filepath.Join(dir, "snapshots")
}

var nameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// NewName builds a default name: <device>-<yyyymmdd-hhmmss>.
func NewName(device string, t time.Time) string {
	d := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(device, "_")
	return strings.Trim(d, "_.-") + "-" + t.Format("20060102-150405")
}

func (s Store) file(name string) string { return filepath.Join(s.Dir, name+".json.gz") }

// Save writes a snapshot. The name must be unique.
func (s Store) Save(sn *Snapshot) error {
	if !nameOK.MatchString(sn.Meta.Name) {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "snapshot name %q: use letters, digits, '.', '_', '-'", sn.Meta.Name)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	f, err := os.OpenFile(s.file(sn.Meta.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "a snapshot named %q already exists", sn.Meta.Name)
	}
	if err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	defer f.Close()
	sn.Meta.Leaves = len(sn.Flat)
	gz := gzip.NewWriter(f)
	if err := json.NewEncoder(gz).Encode(sn); err != nil {
		return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	return gz.Close()
}

// Load reads a snapshot by name, or from a file path.
func (s Store) Load(nameOrFile string) (*Snapshot, error) {
	p := nameOrFile
	if nameOK.MatchString(nameOrFile) {
		if _, err := os.Stat(s.file(nameOrFile)); err == nil {
			p = s.file(nameOrFile)
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "snapshot %q not found", nameOrFile)
	}
	defer f.Close()
	var r interface{ Read([]byte) (int, error) } = f
	if strings.HasSuffix(p, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		r = gz
	}
	d := json.NewDecoder(r)
	d.UseNumber()
	sn := &Snapshot{}
	if err := d.Decode(sn); err != nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "snapshot %s: %v", nameOrFile, err)
	}
	return sn, nil
}

// List returns the metadata of all snapshots, newest first.
func (s Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return []Meta{}, nil
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	out := []Meta{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json.gz")
		if !ok {
			continue
		}
		sn, err := s.Load(name)
		if err != nil {
			continue
		}
		out = append(out, sn.Meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

// Delete removes a snapshot.
func (s Store) Delete(name string) error {
	if !nameOK.MatchString(name) {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "bad snapshot name %q", name)
	}
	if err := os.Remove(s.file(name)); err != nil {
		return apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "snapshot %q not found", name)
	}
	return nil
}

// Change is one difference.
type Change struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"` // added, removed, changed
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// DiffResult compares two sets of leaves.
type DiffResult struct {
	Added     int      `json:"added"`
	Removed   int      `json:"removed"`
	Changed   int      `json:"changed"`
	Unchanged int      `json:"unchanged"`
	Changes   []Change `json:"changes"`
}

// Diff compares leaves by path. Module prefixes are ignored so data read with
// and without them compares, and volatile leaves can be skipped with ignore
// (substring match on the path, e.g. "statistics", "current-datetime").
func Diff(a, b []live.Update, ignore []string) DiffResult {
	norm := func(us []live.Update) (map[string]any, map[string]string) {
		m := map[string]any{}
		disp := map[string]string{}
		for _, u := range us {
			k := stripPrefixes(u.Path)
			if skip(k, ignore) {
				continue
			}
			m[k] = u.Value
			disp[k] = u.Path
		}
		return m, disp
	}
	am, _ := norm(a)
	bm, _ := norm(b)
	r := DiffResult{Changes: []Change{}}
	for k, av := range am {
		bv, ok := bm[k]
		switch {
		case !ok:
			r.Changes = append(r.Changes, Change{Path: k, Kind: "removed", Before: av})
			r.Removed++
		case fmt.Sprint(av) != fmt.Sprint(bv):
			r.Changes = append(r.Changes, Change{Path: k, Kind: "changed", Before: av, After: bv})
			r.Changed++
		default:
			r.Unchanged++
		}
	}
	for k, bv := range bm {
		if _, ok := am[k]; !ok {
			r.Changes = append(r.Changes, Change{Path: k, Kind: "added", After: bv})
			r.Added++
		}
	}
	sort.Slice(r.Changes, func(i, j int) bool { return r.Changes[i].Path < r.Changes[j].Path })
	return r
}

func skip(p string, ignore []string) bool {
	for _, x := range ignore {
		if x != "" && strings.Contains(p, x) {
			return true
		}
	}
	return false
}

// stripPrefixes removes module prefixes from path elements ("/a:b/c" -> "/b/c"),
// leaving list keys untouched.
func stripPrefixes(p string) string {
	var b strings.Builder
	depth := 0
	seg := strings.Builder{}
	flush := func() {
		s := seg.String()
		if i := strings.IndexByte(s, ':'); i >= 0 && !strings.Contains(s[:i], "[") {
			s = s[i+1:]
		}
		b.WriteString(s)
		seg.Reset()
	}
	for _, r := range p {
		switch {
		case r == '[':
			depth++
			seg.WriteRune(r)
		case r == ']':
			depth--
			seg.WriteRune(r)
		case r == '/' && depth == 0:
			flush()
			b.WriteRune('/')
		default:
			seg.WriteRune(r)
		}
	}
	flush()
	return b.String()
}
