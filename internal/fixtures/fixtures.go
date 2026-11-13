// Package fixtures reads network-operator's gNMI test fixtures (.txtar files in
// test/gnmi/testdata/<provider>/) and derives the device leaves a resource is
// expected to produce.
package fixtures

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/snapshot"
	"golang.org/x/tools/txtar"
	"gopkg.in/yaml.v3"
)

// Fixture is one .txtar file.
type Fixture struct {
	File    string           `json:"file"`
	Title   string           `json:"title"`
	CRs     []map[string]any `json:"crs"` // all resources in the file
	Preload any              `json:"preload,omitempty"`
	Expect  any              `json:"expect,omitempty"`
	Delete  any              `json:"delete,omitempty"`
}

// Kinds returns the kinds of the fixture's resources.
func (f *Fixture) Kinds() []string {
	var out []string
	for _, cr := range f.CRs {
		if k, _ := cr["kind"].(string); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// CR returns the fixture's resource of a kind.
func (f *Fixture) CR(kind string) map[string]any {
	for _, cr := range f.CRs {
		if k, _ := cr["kind"].(string); strings.EqualFold(k, kind) {
			return cr
		}
	}
	return nil
}

// Dir finds the fixtures folder of a provider in an operator checkout.
func Dir(operatorRepo, provider string) string {
	return filepath.Join(operatorRepo, "test", "gnmi", "testdata", provider)
}

// FindRepo returns the operator checkout: explicit (--operator-repo), else the
// one chosen in the UI (Fixtures tab), else
// $CANOPY_OPERATOR_REPO, else a network-operator folder next to the working
// directory. A folder given or chosen must be a checkout: falling back to
// another one would quietly show the wrong fixtures.
func FindRepo(explicit string) (string, error) {
	if explicit == "" {
		explicit = Choice().Current
	}
	if explicit != "" {
		if !isCheckout(explicit) {
			return "", apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "%s is not a network-operator checkout (no test/gnmi/testdata)", explicit).
				WithHint("choose the repository's top folder")
		}
		abs, _ := filepath.Abs(explicit)
		return abs, nil
	}
	cands := []string{os.Getenv("CANOPY_OPERATOR_REPO"), filepath.Join("..", "network-operator")}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(c, "test", "gnmi", "testdata")); err == nil && st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs, nil
		}
	}
	return "", apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "network-operator checkout not found").
		WithHint("choose the checkout in the Fixtures tab, pass --operator-repo or set CANOPY_OPERATOR_REPO")
}

// Load reads all fixtures of a provider.
func Load(dir string) ([]*Fixture, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.txtar"))
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if len(files) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no fixtures in %s", dir)
	}
	sort.Strings(files)
	var out []*Fixture
	for _, file := range files {
		f, err := Parse(file)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// Parse reads one fixture file.
func Parse(file string) (*Fixture, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return ParseBytes(filepath.Base(file), b)
}

// ParseBytes parses fixture text.
func ParseBytes(name string, b []byte) (*Fixture, error) {
	a := txtar.Parse(b)
	f := &Fixture{File: name, Title: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(a.Comment)), "#"))}
	for _, af := range a.Files {
		body := strings.TrimSpace(string(af.Data))
		switch af.Name {
		case "state/preload", "state/expect", "state/delete":
			if body == "" {
				continue
			}
			d := json.NewDecoder(strings.NewReader(body))
			d.UseNumber()
			var v any
			if err := d.Decode(&v); err != nil {
				return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "%s %s: %v", f.File, af.Name, err)
			}
			switch af.Name {
			case "state/preload":
				f.Preload = v
			case "state/expect":
				f.Expect = v
			default:
				f.Delete = v
			}
		default:
			dec := yaml.NewDecoder(strings.NewReader(body))
			for {
				var cr map[string]any
				if err := dec.Decode(&cr); err != nil {
					break
				}
				if cr != nil {
					f.CRs = append(f.CRs, cr)
				}
			}
		}
	}
	return f, nil
}

func flat(v any) []live.Update {
	var out []live.Update
	if v == nil {
		return out
	}
	live.Flatten("/", v, func(p string, x any) { out = append(out, live.Update{Path: p, Value: x}) })
	return out
}

// Leaf is an expected device leaf.
type Leaf struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
	// From names the spec field the value (or a key in the path) came from.
	From string `json:"from,omitempty"`
}

// Touched returns the leaves the fixture's resources add or change on the
// device (expect minus preload), and the ones removed again on delete.
func (f *Fixture) Touched() (applied []Leaf, removed []string) {
	d := snapshot.Diff(flat(f.Preload), flat(f.Expect), nil)
	for _, c := range d.Changes {
		if c.Kind == "added" || c.Kind == "changed" {
			applied = append(applied, Leaf{Path: c.Path, Value: c.After})
		}
	}
	if f.Delete != nil {
		dd := snapshot.Diff(flat(f.Expect), flat(f.Delete), nil)
		for _, c := range dd.Changes {
			if c.Kind == "removed" {
				removed = append(removed, c.Path)
			}
		}
	}
	return applied, removed
}

// specValues flattens a spec to field path -> string value (scalars only).
func specValues(v any, prefix string, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			specValues(vv, join(prefix, k), out)
		}
	case []any:
		for i, vv := range x {
			specValues(vv, fmt.Sprintf("%s[%d]", prefix, i), out)
		}
	case bool, nil:
		// booleans are too ambiguous to map
	default:
		out[prefix] = fmt.Sprint(x)
	}
}

func join(p, k string) string {
	if p == "" {
		return k
	}
	return p + "." + k
}

// Expected maps the fixture's touched leaves onto a live resource: values (and
// list keys in paths) that equal a field of the fixture's resource spec are
// replaced by the live resource's value of the same field. Leaves that don't
// come from the spec keep the fixture's value.
func Expected(f *Fixture, kind string, liveSpec map[string]any) ([]Leaf, []string, error) {
	cr := f.CR(kind)
	if cr == nil {
		return nil, nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "fixture %s has no %s", f.File, kind)
	}
	fixSpec, _ := cr["spec"].(map[string]any)
	fv, lv := map[string]string{}, map[string]string{}
	specValues(fixSpec, "", fv)
	specValues(liveSpec, "", lv)
	// fixture value -> (field, live value), longest values first so they win
	type sub struct{ field, from, to string }
	var subs []sub
	for field, v := range fv {
		if l, ok := lv[field]; ok && len(v) >= 2 && !strings.HasPrefix(field, "deviceRef") {
			subs = append(subs, sub{field, v, l})
		}
	}
	sort.Slice(subs, func(i, j int) bool { return len(subs[i].from) > len(subs[j].from) })
	applied, removed := f.Touched()
	mapStr := func(s string) (string, string) {
		for _, x := range subs {
			if s == x.from {
				return x.to, x.field
			}
		}
		return s, ""
	}
	mapPath := func(p string) (string, string) {
		used := ""
		for _, x := range subs {
			old := "=" + x.from + "]"
			if strings.Contains(p, old) {
				p = strings.ReplaceAll(p, old, "="+x.to+"]")
				used = x.field
			}
		}
		return p, used
	}
	var out []Leaf
	for _, l := range applied {
		p, pf := mapPath(l.Path)
		leaf := Leaf{Path: p, Value: l.Value, From: pf}
		if s, ok := l.Value.(string); ok {
			if v, field := mapStr(s); field != "" {
				leaf.Value, leaf.From = v, field
			}
		} else if n, ok := l.Value.(json.Number); ok {
			if v, field := mapStr(string(n)); field != "" {
				leaf.Value, leaf.From = json.Number(v), field
			}
		}
		out = append(out, leaf)
	}
	var gone []string
	for _, p := range removed {
		mp, _ := mapPath(p)
		gone = append(gone, mp)
	}
	return out, gone, nil
}

// Export writes a fixture in network-operator's .txtar layout: the resource
// YAML (section "<kind plural>/<name>"), and state/preload, state/expect and
// state/delete as JSON trees (any may be nil).
func Export(title string, crYAML []byte, preload, expect, del any) ([]byte, error) {
	var cr map[string]any
	if err := yaml.Unmarshal(crYAML, &cr); err != nil || cr == nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "resource YAML: %v", err)
	}
	kind, _ := cr["kind"].(string)
	meta, _ := cr["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if kind == "" || name == "" {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "the resource needs kind and metadata.name")
	}
	a := &txtar.Archive{Comment: []byte("# " + title + "\n")}
	a.Files = append(a.Files, txtar.File{Name: strings.ToLower(kind) + "s/" + name, Data: append(bytesTrim(crYAML), '\n', '\n')})
	for _, s := range []struct {
		name string
		v    any
	}{{"state/preload", preload}, {"state/expect", expect}, {"state/delete", del}} {
		if s.v == nil {
			continue
		}
		b, err := json.MarshalIndent(s.v, "", "  ")
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
		}
		a.Files = append(a.Files, txtar.File{Name: s.name, Data: append(append([]byte("\n"), b...), '\n', '\n')})
	}
	return txtar.Format(a), nil
}

func bytesTrim(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }
