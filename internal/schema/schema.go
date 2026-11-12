// Package schema loads YANG modules with goyang and indexes the data tree for
// browsing, search and path generation.
package schema

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/openconfig/goyang/pkg/yang"
)

// Node is one data node (container, list, leaf, leaf-list, anydata). Choice and
// case are flattened: their children appear under the parent, and the choice/case
// names are kept in Choice.
type Node struct {
	ID          int       `json:"id"`
	Parent      int       `json:"parent"` // -1 for top-level nodes
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Module      string    `json:"module"`           // module that defines the node (augmenting module for augments)
	Prefix      string    `json:"prefix,omitempty"` // prefix of Module
	Path        string    `json:"path"`             // data path, list keys as wildcards
	QPath       string    `json:"qpath"`            // same, module-qualified where the module changes
	Config      bool      `json:"config"`           // false: state (read-only)
	Keys        []string  `json:"keys,omitempty"`
	HasChildren bool      `json:"hasChildren"`
	ChildCount  int       `json:"childCount"`
	Type        *TypeInfo `json:"type,omitempty"`
	Description string    `json:"description,omitempty"`
	Units       string    `json:"units,omitempty"`
	Default     []string  `json:"default,omitempty"`
	Mandatory   bool      `json:"mandatory,omitempty"`
	Presence    bool      `json:"presence,omitempty"`
	Choice      string    `json:"choice,omitempty"` // "choice/case" when under a choice
	Location    string    `json:"location,omitempty"`
	Children    []int     `json:"-"`
}

// TypeInfo describes a leaf's type.
type TypeInfo struct {
	Name           string     `json:"name"`
	Base           string     `json:"base"`
	Range          string     `json:"range,omitempty"`
	Length         string     `json:"length,omitempty"`
	Patterns       []string   `json:"patterns,omitempty"`
	Enums          []string   `json:"enums,omitempty"`
	Bits           []string   `json:"bits,omitempty"`
	IdentityBase   string     `json:"identityBase,omitempty"`
	LeafRef        string     `json:"leafref,omitempty"`
	FractionDigits int        `json:"fractionDigits,omitempty"`
	Union          []TypeInfo `json:"union,omitempty"`
}

// Module is a loaded module.
type Module struct {
	Name         string `json:"name"`
	Revision     string `json:"revision,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	Organization string `json:"organization,omitempty"`
	Version      string `json:"version,omitempty"` // oc-ext:openconfig-version
	File         string `json:"file,omitempty"`
	TopLevel     int    `json:"topLevel"`
}

// Tree is a loaded, indexed schema.
type Tree struct {
	Dir      string   `json:"dir"`
	Modules  []Module `json:"modules"`
	Roots    []int    `json:"roots"`
	Nodes    []*Node  `json:"-"`
	Warnings []string `json:"warnings,omitempty"`
	Files    int      `json:"files"`
	LoadMs   int64    `json:"loadMs"`
}

// Summary is the JSON-friendly description of a Tree.
type Summary struct {
	Dir      string   `json:"dir"`
	Files    int      `json:"files"`
	Modules  []Module `json:"modules"`
	Nodes    int      `json:"nodes"`
	Roots    int      `json:"roots"`
	Warnings []string `json:"warnings,omitempty"`
	LoadMs   int64    `json:"loadMs"`
}

// Summary returns the summary.
func (t *Tree) Summary() Summary {
	return Summary{Dir: t.Dir, Files: t.Files, Modules: t.Modules, Nodes: len(t.Nodes), Roots: len(t.Roots),
		Warnings: t.Warnings, LoadMs: t.LoadMs}
}

var revFile = regexp.MustCompile(`^(.+?)(@(\d{4}-\d{2}-\d{2}))?\.yang$`)

// LoadOptions control loading.
type LoadOptions struct {
	// Modules restricts the modules whose trees are shown; their imports are still
	// loaded. Empty means every module found.
	Modules []string
	// Force reloads even if the same folders are cached by the caller.
	Force bool `json:"force,omitempty"`
	// SearchDirs are only used to find imported modules (for example a folder
	// with all IETF RFC modules); their other modules aren't loaded.
	SearchDirs []string `json:"searchDirs,omitempty"`
}

// Load reads every .yang file under the given directories (recursively). When a
// module exists in several files, the newest revision is used.
func Load(dirs []string, opt LoadOptions) (*Tree, error) {
	start := time.Now()
	if len(dirs) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no YANG directory given")
	}
	type cand struct{ file, rev string }
	best := map[string]cand{}
	var searchDirs []string
	seenDir := map[string]bool{}
	files := 0
	for _, root := range dirs {
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "YANG directory %q not found", root)
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && p != root {
					return filepath.SkipDir
				}
				return nil
			}
			m := revFile.FindStringSubmatch(d.Name())
			if m == nil {
				return nil
			}
			files++
			dir := filepath.Dir(p)
			if !seenDir[dir] {
				seenDir[dir] = true
				searchDirs = append(searchDirs, dir)
			}
			if c, ok := best[m[1]]; !ok || m[3] > c.rev {
				best[m[1]] = cand{p, m[3]}
			}
			return nil
		})
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassSchema, apperr.CodeSchema, err)
		}
	}
	if len(best) == 0 {
		return nil, apperr.New(apperr.ClassSchema, apperr.CodeSchema, "no .yang files found in %s", strings.Join(dirs, ", "))
	}

	// Search-only folders resolve imports but aren't loaded as a whole.
	for _, root := range opt.SearchDirs {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && !seenDir[p] {
				seenDir[p] = true
				searchDirs = append(searchDirs, p)
			}
			return nil
		})
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names)

	// Load; modules whose imports can't be found (directly or through another
	// module) are skipped with a warning, so one broken module doesn't block the
	// rest. Only when nothing is left is it an error.
	skip := map[string]bool{}
	var skippedNote []string
	var ms *yang.Modules
	var warnings []string
	for round := 0; ; round++ {
		ms = yang.NewModules()
		ms.ParseOptions.IgnoreSubmoduleCircularDependencies = true
		ms.AddPath(searchDirs...)
		warnings = nil
		loaded := 0
		for _, n := range names {
			if skip[n] {
				continue
			}
			if err := ms.Read(best[n].file); err != nil {
				warnings = append(warnings, err.Error()) // a broken file doesn't stop the rest
				continue
			}
			loaded++
		}
		errs := ms.Process()
		missing := missingNames(errs)
		if len(missing) == 0 {
			for _, e := range errs {
				warnings = append(warnings, e.Error())
			}
			break
		}
		described := missingImports(ms, errs)
		affected := dependents(ms, missing)
		// Map module names to our file keys (a file name doesn't have to match the
		// module name, e.g. cisco-nx-openconfig-macsec-deviations.yang).
		keyOfFile := map[string]string{}
		for _, n := range names {
			keyOfFile[filepath.Clean(best[n].file)] = n
		}
		keyOf := func(m *yang.Module) string {
			src := yang.Source(m)
			if i := strings.LastIndex(src, ".yang:"); i >= 0 {
				src = src[:i+5]
			}
			return keyOfFile[filepath.Clean(src)]
		}
		newSkipSet := map[string]bool{}
		for _, group := range []map[string]*yang.Module{ms.Modules, ms.SubModules} {
			for _, m := range group {
				if k := keyOf(m); k != "" && affected[m.Name] && !skip[k] {
					newSkipSet[k] = true
				}
			}
		}
		var newSkips []string
		for _, n := range names {
			if newSkipSet[n] {
				newSkips = append(newSkips, n)
			}
		}
		if len(newSkips) == 0 || len(newSkips) >= loaded || round >= 4 {
			return nil, apperr.New(apperr.ClassSchema, apperr.CodeSchema,
				"missing YANG modules: %s", strings.Join(described, "; ")).
				WithHint("add the missing modules to the YANG folder").
				WithDetail("errors", joinErrs(errs, 20))
		}
		for _, n := range newSkips {
			skip[n] = true
		}
		skippedNote = append(skippedNote, fmt.Sprintf("skipped %s: missing %s", strings.Join(newSkips, ", "), strings.Join(described, "; ")))
	}
	warnings = append(skippedNote, warnings...)

	t := &Tree{Dir: strings.Join(dirs, string(os.PathListSeparator)), Files: files}
	want := map[string]bool{}
	for _, m := range opt.Modules {
		want[m] = true
	}
	// Modules keyed by name and name@revision; visit each once, by name.
	var mods []*yang.Module
	for k, m := range ms.Modules {
		if k == m.Name {
			mods = append(mods, m)
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Name < mods[j].Name })
	type top struct {
		e   *yang.Entry
		mod string
	}
	var tops []top
	for _, m := range mods {
		mi := Module{Name: m.Name, Revision: m.Current(), Prefix: m.GetPrefix(), File: relFile(dirs, m)}
		if m.Namespace != nil {
			mi.Namespace = m.Namespace.Name
		}
		if m.Organization != nil {
			mi.Organization = firstLine(m.Organization.Name)
		}
		mi.Version = ocVersion(m)
		e := yang.ToEntry(m)
		if errs := e.GetErrors(); len(errs) > 0 {
			for _, er := range errs {
				warnings = append(warnings, er.Error())
			}
		}
		if len(want) == 0 || want[m.Name] {
			for _, c := range sortedChildren(e) {
				if c.RPC != nil || c.Kind == yang.NotificationEntry {
					continue
				}
				tops = append(tops, top{c, m.Name})
				mi.TopLevel++
			}
		}
		t.Modules = append(t.Modules, mi)
	}
	for _, tp := range tops {
		t.add(tp.e, -1, "", "", "")
	}
	t.Warnings = dedupe(warnings)
	t.LoadMs = time.Since(start).Milliseconds()
	return t, nil
}

// add indexes e (and its subtree) under parent.
func (t *Tree) add(e *yang.Entry, parent int, path, qpath, choice string) {
	if e.IsChoice() || e.IsCase() {
		label := e.Name
		if choice != "" {
			label = choice + "/" + e.Name
		}
		for _, c := range sortedChildren(e) {
			t.add(c, parent, path, qpath, label)
		}
		return
	}
	n := &Node{ID: len(t.Nodes), Parent: parent, Name: e.Name, Choice: choice}
	n.Module, n.Prefix = moduleOf(e)
	n.Kind = kindOf(e)
	n.Config = !e.ReadOnly()
	n.Description = strings.TrimSpace(e.Description)
	n.Units = e.Units
	if l, ok := e.Node.(*yang.Leaf); ok && n.Units == "" && l.Units != nil {
		n.Units = l.Units.Name
	} else if ll, ok := e.Node.(*yang.LeafList); ok && n.Units == "" && ll.Units != nil {
		n.Units = ll.Units.Name
	}
	n.Default = e.DefaultValues()
	n.Mandatory = e.Mandatory == yang.TSTrue
	if e.Node != nil {
		n.Location = yang.Source(e.Node)
		if c, ok := e.Node.(*yang.Container); ok && c.Presence != nil {
			n.Presence = true
		}
	}
	seg := e.Name
	if n.Kind == "list" && e.Key != "" {
		n.Keys = strings.Fields(e.Key)
		for _, k := range n.Keys {
			seg += "[" + k + "=*]"
		}
	}
	n.Path = path + "/" + seg
	// Qualified where the module changes (the root, and augmented nodes), as in
	// JSON_IETF member names.
	if parent < 0 || t.Nodes[parent].Module != n.Module {
		n.QPath = qpath + "/" + n.Module + ":" + seg
	} else {
		n.QPath = qpath + "/" + seg
	}
	if e.Type != nil {
		ti := typeInfo(e.Type, 0)
		n.Type = &ti
	}
	t.Nodes = append(t.Nodes, n)
	if parent < 0 {
		t.Roots = append(t.Roots, n.ID)
	} else {
		t.Nodes[parent].Children = append(t.Nodes[parent].Children, n.ID)
	}
	if e.Dir != nil {
		for _, c := range sortedChildren(e) {
			if c.RPC != nil || c.Kind == yang.NotificationEntry {
				continue
			}
			t.add(c, n.ID, n.Path, n.QPath, "")
		}
	}
	n.ChildCount = len(n.Children)
	n.HasChildren = n.ChildCount > 0
}

func kindOf(e *yang.Entry) string {
	switch {
	case e.IsLeafList():
		return "leaf-list"
	case e.IsList():
		return "list"
	case e.IsLeaf():
		return "leaf"
	case e.IsContainer():
		return "container"
	case e.Kind == yang.AnyDataEntry:
		return "anydata"
	case e.Kind == yang.AnyXMLEntry:
		return "anyxml"
	}
	return "node"
}

// moduleOf returns the module (and its prefix) that defines e: the augmenting
// module for augmented nodes, otherwise the module of the node's statement.
func moduleOf(e *yang.Entry) (string, string) {
	if name, err := e.InstantiatingModule(); err == nil {
		prefix := ""
		if e.Prefix != nil {
			prefix = e.Prefix.Name
		}
		return name, prefix
	}
	if e.Node != nil {
		if m := yang.RootNode(e.Node); m != nil {
			return m.Name, m.GetPrefix()
		}
	}
	return "", ""
}

func sortedChildren(e *yang.Entry) []*yang.Entry {
	out := make([]*yang.Entry, 0, len(e.Dir))
	for _, c := range e.Dir {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func typeInfo(y *yang.YangType, depth int) TypeInfo {
	ti := TypeInfo{Name: y.Name, Base: y.Kind.String(), FractionDigits: y.FractionDigits, LeafRef: y.Path}
	if len(y.Range) > 0 {
		ti.Range = y.Range.String()
	}
	if len(y.Length) > 0 {
		ti.Length = y.Length.String()
	}
	ti.Patterns = append(ti.Patterns, y.POSIXPattern...)
	if len(ti.Patterns) == 0 {
		ti.Patterns = append(ti.Patterns, y.Pattern...)
	}
	if y.Enum != nil {
		ti.Enums = byValue(y.Enum)
	}
	if y.Bit != nil {
		ti.Bits = byValue(y.Bit)
	}
	if y.IdentityBase != nil {
		ti.IdentityBase = y.IdentityBase.Name
	}
	if depth < 4 {
		for _, u := range y.Type {
			ti.Union = append(ti.Union, typeInfo(u, depth+1))
		}
	}
	return ti
}

func ocVersion(m *yang.Module) string {
	for _, ext := range m.Extensions {
		if strings.HasSuffix(ext.Keyword, ":openconfig-version") {
			return ext.Argument
		}
	}
	return ""
}

func relFile(dirs []string, m *yang.Module) string {
	src := yang.Source(m)
	if i := strings.LastIndex(src, ".yang:"); i >= 0 {
		src = src[:i+5]
	}
	for _, d := range dirs {
		if r, err := filepath.Rel(d, src); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return src
}

var noModule = regexp.MustCompile(`no such (module|submodule): (\S+)`)

// missingImports finds "no such module" errors and names the modules that import
// (or include) each missing one.
func missingImports(ms *yang.Modules, errs []error) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range errs {
		m := noModule.FindStringSubmatch(e.Error())
		if m == nil || seen[m[2]] {
			continue
		}
		seen[m[2]] = true
		var by []string
		byFile := map[string]bool{}
		check := func(mod *yang.Module) {
			for _, im := range mod.Import {
				if im.Name == m[2] && !byFile[yang.Source(im)] {
					byFile[yang.Source(im)] = true
					by = append(by, mod.Name+" ("+yang.Source(im)+")")
				}
			}
			for _, in := range mod.Include {
				if in.Name == m[2] && !byFile[yang.Source(in)] {
					byFile[yang.Source(in)] = true
					by = append(by, mod.Name+" ("+yang.Source(in)+")")
				}
			}
		}
		for _, mod := range ms.Modules {
			check(mod)
		}
		for _, mod := range ms.SubModules {
			check(mod)
		}
		sort.Strings(by)
		s := m[2]
		if len(by) > 0 {
			verb := "imported by "
			if m[1] == "submodule" {
				verb = "included by "
			}
			s += ", " + verb + strings.Join(by, ", ")
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func joinErrs(errs []error, max int) string {
	var b []string
	for i, e := range errs {
		if i == max {
			b = append(b, fmt.Sprintf("... %d more", len(errs)-max))
			break
		}
		b = append(b, e.Error())
	}
	return strings.Join(b, "\n")
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// byValue returns enum or bit names in value order (declaration order unless
// values are assigned explicitly).
func byValue(e *yang.EnumType) []string {
	vals := e.Values()
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, e.Name(v))
	}
	return out
}

// missingNames returns the module names from "no such module" errors.
func missingNames(errs []error) map[string]bool {
	out := map[string]bool{}
	for _, e := range errs {
		if m := noModule.FindStringSubmatch(e.Error()); m != nil {
			out[m[2]] = true
		}
	}
	return out
}

// dependents returns the modules that import or include any of the given
// modules, directly or through other modules.
func dependents(ms *yang.Modules, missing map[string]bool) map[string]bool {
	rev := map[string][]string{}
	add := func(m *yang.Module) {
		for _, im := range m.Import {
			rev[im.Name] = append(rev[im.Name], m.Name)
		}
		for _, in := range m.Include {
			rev[in.Name] = append(rev[in.Name], m.Name)
		}
	}
	for _, m := range ms.Modules {
		add(m)
	}
	for _, m := range ms.SubModules {
		add(m)
	}
	out := map[string]bool{}
	var queue []string
	for n := range missing {
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, d := range rev[n] {
			if !out[d] {
				out[d] = true
				queue = append(queue, d)
			}
		}
	}
	return out
}
