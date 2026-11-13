// Package models lists and downloads YANG model sets from GitHub without cloning
// whole repositories, caches them locally, and detects which set a device needs.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// Source is a model source: one repository, versioned by tags or by folders.
type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Repo        string `json:"repo"`
	Description string `json:"description,omitempty"`
	// Tags: versions are tags matching Pattern; the tarball is extracted, keeping
	// the .yang files under Dirs.
	// Folders: versions are subfolders of Parent (on branch Ref) matching Pattern.
	// With Group, versions are two levels down and named "group/release"
	// (Juniper: 23.4/23.4R2). Dirs, if set, limits the download to those
	// subfolders of the version folder. Parent may lie inside Git submodules.
	Kind    string         `json:"kind"` // "tags" or "folders"
	Pattern *regexp.Regexp `json:"-"`
	Group   *regexp.Regexp `json:"-"`
	Dirs    []string       `json:"dirs,omitempty"`
	Parent  string         `json:"parent,omitempty"`
	Ref     string         `json:"ref,omitempty"`
	// Extra shared folders loaded alongside (for example standard IETF modules).
	Extra []Folder `json:"extra,omitempty"`
}

// Folder is a folder in a repository branch.
type Folder struct {
	Repo, Ref, Path string
}

var ietfRFC = Folder{Repo: "YangModels/yang", Ref: "main", Path: "standard/ietf/RFC"}

// Sources are the built-in sources (docs/ARCHITECTURE.md 5.3, release layouts).
var Sources = []*Source{
	{ID: "srlinux", Name: "Nokia SR Linux", Repo: "nokia/srlinux-yang-models", Kind: "tags",
		Pattern: regexp.MustCompile(`^v\d+\.\d+\.\d+$`), Dirs: []string{"srlinux-yang-models"},
		Description: "native, OpenConfig and IETF models per SR Linux release"},
	{ID: "nxos", Name: "Cisco NX-OS", Repo: "YangModels/yang", Kind: "folders", Ref: "main",
		Parent: "vendor/cisco/nx", Pattern: regexp.MustCompile(`^\d+\.\d+-\d+`), Extra: []Folder{ietfRFC},
		Description: "YangModels/yang vendor/cisco/nx, folder per release (10.4(3) = 10.4-3)"},
	{ID: "iosxr", Name: "Cisco IOS XR", Repo: "YangModels/yang", Kind: "folders", Ref: "main",
		Parent: "vendor/cisco/xr", Pattern: regexp.MustCompile(`^\d{3,5}$`), Extra: []Folder{ietfRFC},
		Description: "YangModels/yang vendor/cisco/xr, folder per release without dots"},
	{ID: "iosxe", Name: "Cisco IOS XE", Repo: "YangModels/yang", Kind: "folders", Ref: "main",
		Parent: "vendor/cisco/xe", Pattern: regexp.MustCompile(`^\d{3,5}$`), Extra: []Folder{ietfRFC},
		Description: "YangModels/yang vendor/cisco/xe, folder per release without dots"},
	{ID: "openconfig", Name: "OpenConfig", Repo: "openconfig/public", Kind: "tags",
		Pattern: regexp.MustCompile(`^v\d+\.\d+\.\d+$`), Dirs: []string{"release/models", "third_party/ietf"},
		Description: "openconfig/public releases (vendor-neutral models)"},
	{ID: "junos", Name: "Juniper Junos", Repo: "YangModels/yang", Kind: "folders", Ref: "main",
		Parent: "vendor/juniper", Group: regexp.MustCompile(`^\d+\.\d+$`), Pattern: regexp.MustCompile(`^\d+\.\d+/\d+\.\d+R\d+(-S\d+)?(-EVO)?$`),
		Dirs: []string{"native/conf-and-rpcs/common", "native/conf-and-rpcs/junos/conf"}, Extra: []Folder{ietfRFC},
		Description: "Juniper/yang (submodule vendor/juniper of YangModels/yang), release folders like 23.4/23.4R2 (Junos) and 23.4/23.4R2-EVO"},
	{ID: "huawei", Name: "Huawei VRP", Repo: "YangModels/yang", Kind: "folders", Ref: "main",
		Parent: "vendor/huawei/network-router", Group: regexp.MustCompile(`^\d+\.\d+\.\d+$`), Pattern: regexp.MustCompile(`^\d+\.\d+\.\d+/[a-z0-9][a-z0-9-]*$`),
		Extra: []Folder{ietfRFC},
		Description: "Huawei/yang (submodule vendor/huawei of YangModels/yang), version/product like 8.22.0/ne8000-x"},
	{ID: "sros", Name: "Nokia SR OS", Repo: "nokia/7x50_YangModels", Kind: "tags",
		Pattern: regexp.MustCompile(`^sros_\d+\.\d+\.r\d+$`), Dirs: []string{"YANG"},
		Description: "nokia/7x50_YangModels release tags (sros_25.10.r1)"},
	{ID: "eos", Name: "Arista EOS", Repo: "aristanetworks/yang", Kind: "folders", Ref: "master",
		Parent: "", Pattern: regexp.MustCompile(`^EOS-\d+\.\d+\.\d+[A-Z]?$`),
		Dirs: []string{"release/openconfig/models", "openconfig/public/release/models"},
		Description: "aristanetworks/yang, folder per EOS release with Arista's OpenConfig models"},
}

// SourceByID returns a source.
func SourceByID(id string) (*Source, error) {
	for _, s := range Sources {
		if s.ID == id {
			return s, nil
		}
	}
	var ids []string
	for _, s := range Sources {
		ids = append(ids, s.ID)
	}
	return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "unknown model source %q (%s)", id, strings.Join(ids, ", "))
}

// Manager lists, downloads and caches model sets.
type Manager struct {
	Dir string // cache root
	gh  *github
	mu  sync.Mutex
	// fetch locks per set, so parallel requests download once
	locks map[string]*sync.Mutex
	// submodules followed so far (see submodules.go)
	subMu sync.Mutex
	subs  []subLink
}

// NewManager uses <user cache dir>/canopy/models (or $CANOPY_CACHE_DIR/models).
func NewManager() *Manager {
	base := os.Getenv("CANOPY_CACHE_DIR")
	if base == "" {
		if d, err := os.UserCacheDir(); err == nil {
			base = filepath.Join(d, "canopy")
		} else {
			base = ".canopy-cache"
		}
	}
	return &Manager{Dir: filepath.Join(base, "models"), gh: newGitHub(), locks: map[string]*sync.Mutex{}}
}

func (m *Manager) lock(key string) func() {
	m.mu.Lock()
	l, ok := m.locks[key]
	if !ok {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Version is an available version and whether it is cached.
type Version struct {
	Name   string `json:"name"`
	Cached bool   `json:"cached"`
}

const listTTL = 24 * time.Hour

// Versions lists the versions of a source, newest first. Listings are cached for
// a day; refresh forces a new listing.
func (m *Manager) Versions(ctx context.Context, id string, refresh bool) ([]Version, error) {
	s, err := SourceByID(id)
	if err != nil {
		return nil, err
	}
	defer m.lock("list:" + id)()
	cacheFile := filepath.Join(m.Dir, id, "versions.json")
	var names []string
	if st, err := os.Stat(cacheFile); err == nil && !refresh && time.Since(st.ModTime()) < listTTL {
		if b, err := os.ReadFile(cacheFile); err == nil {
			json.Unmarshal(b, &names)
		}
	}
	if len(names) == 0 {
		names, err = m.list(ctx, s)
		if err != nil {
			// offline: fall back to what's cached
			if cached := m.cachedVersions(id); len(cached) > 0 {
				names = cached
			} else {
				return nil, err
			}
		} else {
			os.MkdirAll(filepath.Dir(cacheFile), 0o755)
			b, _ := json.Marshal(names)
			os.WriteFile(cacheFile, b, 0o644)
		}
	}
	SortVersions(names)
	out := make([]Version, 0, len(names))
	for _, n := range names {
		out = append(out, Version{Name: n, Cached: m.isCached(id, n)})
	}
	return out, nil
}

func (m *Manager) list(ctx context.Context, s *Source) ([]string, error) {
	var names []string
	switch s.Kind {
	case "tags":
		tags, err := m.gh.tags(ctx, s.Repo)
		if err != nil {
			return nil, err
		}
		for _, t := range tags {
			if s.Pattern.MatchString(t) {
				names = append(names, t)
			}
		}
	case "folders":
		entries, _, err := m.listFolder(ctx, Folder{Repo: s.Repo, Ref: s.Ref, Path: s.Parent})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type != "dir" {
				continue
			}
			if s.Group == nil {
				if s.Pattern.MatchString(e.Name) {
					names = append(names, e.Name)
				}
				continue
			}
			if !s.Group.MatchString(e.Name) {
				continue
			}
			sub, _, err := m.listFolder(ctx, Folder{Repo: s.Repo, Ref: s.Ref, Path: joinPath(s.Parent, e.Name)})
			if err != nil {
				return nil, err
			}
			for _, x := range sub {
				if v := e.Name + "/" + x.Name; x.Type == "dir" && s.Pattern.MatchString(v) {
					names = append(names, v)
				}
			}
		}
	}
	return names, nil
}

func (m *Manager) setDir(id, version string) string {
	return filepath.Join(m.Dir, id, sanitize(version))
}

func (m *Manager) isCached(id, version string) bool {
	_, err := os.Stat(filepath.Join(m.setDir(id, version), ".complete"))
	return err == nil
}

func (m *Manager) cachedVersions(id string) []string {
	entries, _ := os.ReadDir(filepath.Join(m.Dir, id))
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !m.isCached(id, e.Name()) {
			continue
		}
		name := e.Name()
		if b, err := os.ReadFile(filepath.Join(m.Dir, id, name, ".version")); err == nil {
			name = strings.TrimSpace(string(b)) // nested versions contain "/"
		}
		out = append(out, name)
	}
	return out
}

// Set is a downloaded model set.
type Set struct {
	Source     string   `json:"source"`
	Version    string   `json:"version"`
	Dirs       []string `json:"dirs"`                 // folders to load
	SearchDirs []string `json:"searchDirs,omitempty"` // folders only for resolving imports
	Files      int      `json:"files"`
	Cached     bool     `json:"cached"` // already present, nothing downloaded
}

// Fetch downloads a model set (if not cached) and returns the folders to load.
func (m *Manager) Fetch(ctx context.Context, id, version string, progress func(string)) (*Set, error) {
	s, err := SourceByID(id)
	if err != nil {
		return nil, err
	}
	if version == "" || !s.Pattern.MatchString(version) || !safeRel(version) {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "invalid version %q for %s", version, s.Name)
	}
	defer m.lock("set:" + id + "@" + version)()
	dir := m.setDir(id, version)
	set := &Set{Source: id, Version: version}
	if !m.isCached(id, version) {
		tmp := dir + ".partial"
		os.RemoveAll(tmp)
		n := 0
		switch s.Kind {
		case "tags":
			if progress != nil {
				progress(fmt.Sprintf("downloading %s %s from github.com/%s", s.Name, version, s.Repo))
			}
			n, err = m.gh.extractTarball(ctx, s.Repo, version, s.Dirs, tmp)
		case "folders":
			base := joinPath(s.Parent, version)
			if len(s.Dirs) == 0 {
				n, err = m.fetchFolder(ctx, Folder{Repo: s.Repo, Ref: s.Ref, Path: base}, tmp, progress)
				break
			}
			for _, d := range s.Dirs {
				k, ferr := m.fetchFolder(ctx, Folder{Repo: s.Repo, Ref: s.Ref, Path: joinPath(base, d)}, filepath.Join(tmp, filepath.FromSlash(d)), progress)
				if notFound(ferr) {
					continue // not every release has every folder
				}
				if err = ferr; err != nil {
					break
				}
				n += k
			}
		}
		if err == nil && n == 0 {
			err = apperr.New(apperr.ClassSchema, apperr.CodeSchema, "no YANG files in %s %s", s.Name, version)
		}
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		os.RemoveAll(dir)
		if err := os.Rename(tmp, dir); err != nil {
			return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
		}
		os.WriteFile(filepath.Join(dir, ".version"), []byte(version), 0o644)
		os.WriteFile(filepath.Join(dir, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		set.Files = n
	} else {
		set.Cached = true
	}
	for _, d := range s.Dirs {
		if p := filepath.Join(dir, filepath.FromSlash(d)); exists(p) {
			set.Dirs = append(set.Dirs, p)
		}
	}
	if len(s.Dirs) == 0 {
		set.Dirs = []string{dir}
	}
	for _, x := range s.Extra {
		d, err := m.fetchShared(ctx, x, progress)
		if err != nil {
			return nil, err
		}
		set.SearchDirs = append(set.SearchDirs, d)
	}
	m.indexSet(ctx, set)
	return set, nil
}

// fetchShared downloads a shared folder once (refreshed weekly).
func (m *Manager) fetchShared(ctx context.Context, f Folder, progress func(string)) (string, error) {
	dir := filepath.Join(m.Dir, "_shared", strings.ReplaceAll(f.Repo, "/", "_"), filepath.FromSlash(f.Path))
	defer m.lock("shared:" + dir)()
	if st, err := os.Stat(filepath.Join(dir, ".complete")); err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
		return dir, nil
	}
	tmp := dir + ".partial"
	os.RemoveAll(tmp)
	if _, err := m.fetchFolder(ctx, f, tmp, progress); err != nil {
		os.RemoveAll(tmp)
		if _, statErr := os.Stat(filepath.Join(dir, ".complete")); statErr == nil {
			return dir, nil // stale but usable (offline)
		}
		return "", err
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	os.WriteFile(filepath.Join(dir, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
	return dir, nil
}

func (m *Manager) fetchFolder(ctx context.Context, f Folder, dest string, progress func(string)) (int, error) {
	parent, name := pathSplit(f.Path)
	entries, at, err := m.listFolder(ctx, Folder{Repo: f.Repo, Ref: f.Ref, Path: parent})
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if e.Name == name && e.Type == "dir" {
			return m.gh.downloadFolder(ctx, at.Repo, at.Ref, joinPath(at.Path, name), e.SHA, dest, progress)
		}
	}
	return 0, apperr.New(apperr.ClassSchema, apperr.CodeNotFound, "%s not found in github.com/%s", f.Path, f.Repo)
}

func joinPath(a, b string) string {
	if a == "" {
		return b
	}
	return a + "/" + b
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func pathSplit(p string) (string, string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, s)
}

// SortVersions sorts newest first, comparing numeric parts as numbers.
func SortVersions(v []string) {
	sort.SliceStable(v, func(i, j int) bool { return CompareVersions(v[i], v[j]) > 0 })
}

var numRe = regexp.MustCompile(`\d+|\D+`)

// CompareVersions compares versions part by part (numbers numerically).
func CompareVersions(a, b string) int {
	pa, pb := numRe.FindAllString(a, -1), numRe.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		default:
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
		}
	}
	return len(pa) - len(pb)
}

// Nearest returns want if available, else the closest lower version, else the
// lowest one above. ok is false when the list is empty.
func Nearest(available []string, want string) (string, bool) {
	if len(available) == 0 {
		return "", false
	}
	sorted := append([]string{}, available...)
	SortVersions(sorted)
	for _, v := range sorted {
		if v == want {
			return v, true
		}
	}
	for _, v := range sorted {
		if CompareVersions(v, want) < 0 {
			return v, true
		}
	}
	return sorted[len(sorted)-1], true
}
