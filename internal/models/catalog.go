package models

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/catalog"
)

var catOnce struct {
	sync.Mutex
	c   map[string]*catalog.Catalog
	err map[string]error
}

// Catalog returns the model catalog next to the cache (opened once per path).
func (m *Manager) Catalog() (*catalog.Catalog, error) {
	p := filepath.Join(filepath.Dir(m.Dir), "catalog.db")
	catOnce.Lock()
	defer catOnce.Unlock()
	if catOnce.c == nil {
		catOnce.c, catOnce.err = map[string]*catalog.Catalog{}, map[string]error{}
	}
	if c, ok := catOnce.c[p]; ok {
		return c, nil
	}
	if err, ok := catOnce.err[p]; ok {
		return nil, err
	}
	c, err := catalog.Open(p)
	if err != nil {
		catOnce.err[p] = err
		return nil, err
	}
	catOnce.c[p] = c
	return c, nil
}

// indexSet adds a downloaded set to the catalog (once; errors only matter to the
// catalog, so they don't fail the download).
func (m *Manager) indexSet(ctx context.Context, set *Set) {
	c, err := m.Catalog()
	if err != nil {
		return
	}
	if r, err := c.Release(set.Source, set.Version); err == nil && r != nil && r.Enriched {
		return
	}
	c.IndexDirs(ctx, set.Source, set.Version, set.Dirs)
}

// IndexResult is what Index did.
type IndexResult struct {
	Source   string `json:"source"`
	Version  string `json:"version"`
	Files    int    `json:"files"`
	Enriched bool   `json:"enriched"` // parsed from downloaded files; else from a listing
}

// Index adds a release to the catalog: from the downloaded files when cached,
// else from a listing of its file names on GitHub (one API call, nothing
// downloaded).
func (m *Manager) Index(ctx context.Context, id, version string) (*IndexResult, error) {
	s, err := SourceByID(id)
	if err != nil {
		return nil, err
	}
	if !s.Pattern.MatchString(version) || !safeRel(version) {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "invalid version %q for %s", version, s.Name)
	}
	c, err := m.Catalog()
	if err != nil {
		return nil, err
	}
	if m.isCached(id, version) {
		set, err := m.Fetch(ctx, id, version, nil)
		if err != nil {
			return nil, err
		}
		n, err := c.IndexDirs(ctx, id, version, set.Dirs)
		if err != nil {
			return nil, err
		}
		return &IndexResult{Source: id, Version: version, Files: n, Enriched: true}, nil
	}
	paths, err := m.listFiles(ctx, s, version)
	if err != nil {
		return nil, err
	}
	if err := c.IndexNames(id, version, paths); err != nil {
		return nil, err
	}
	n := 0
	for _, p := range paths {
		if strings.HasSuffix(p, ".yang") {
			n++
		}
	}
	return &IndexResult{Source: id, Version: version, Files: n}, nil
}

// listFiles lists the .yang files of a release without downloading them.
func (m *Manager) listFiles(ctx context.Context, s *Source, version string) ([]string, error) {
	var repo, treeRef string
	switch s.Kind {
	case "tags":
		repo, treeRef = s.Repo, version
	default:
		parent, name := pathSplit(joinPath(s.Parent, version))
		entries, at, err := m.listFolder(ctx, Folder{Repo: s.Repo, Ref: s.Ref, Path: parent})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Name == name && e.Type == "dir" {
				repo, treeRef = at.Repo, e.SHA
			}
		}
		if repo == "" {
			return nil, apperr.New(apperr.ClassSchema, apperr.CodeNotFound, "%s %s not found", s.Name, version)
		}
	}
	entries, err := m.gh.tree(ctx, repo, treeRef)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type != "blob" || !strings.HasSuffix(e.Path, ".yang") || !hasPrefix(e.Path, s.Dirs) {
			continue
		}
		out = append(out, e.Path)
	}
	if len(out) == 0 {
		return nil, apperr.New(apperr.ClassSchema, apperr.CodeSchema, "no YANG files listed in %s %s", s.Name, version)
	}
	return out, nil
}

// Prefetch downloads model sets ahead of time (for offline use) and indexes
// them. Versions may be given as the device reports them when the source's
// profile knows how to map them (10.4(3) for NX-OS).
func (m *Manager) Prefetch(ctx context.Context, id string, versions []string, progress func(string)) ([]*Set, error) {
	var out []*Set
	for _, v := range versions {
		folder := NormalizeVersion(id, v)
		if progress != nil && folder != v {
			progress(fmt.Sprintf("%s %s is %s", id, v, folder))
		}
		set, err := m.Fetch(ctx, id, folder, progress)
		if err != nil {
			return out, err
		}
		out = append(out, set)
	}
	return out, nil
}

// CloseCatalog closes the catalog this manager opened.
func (m *Manager) CloseCatalog() {
	p := filepath.Join(filepath.Dir(m.Dir), "catalog.db")
	catOnce.Lock()
	defer catOnce.Unlock()
	if c, ok := catOnce.c[p]; ok {
		c.Close()
		delete(catOnce.c, p)
	}
}
