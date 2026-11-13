package models

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// Git submodules: several vendor folders of YangModels/yang (vendor/juniper,
// vendor/huawei, vendor/nokia) are submodules that point to other repositories,
// pinned to a commit, and nokia/YangModels has submodules of its own. A folder
// is located by following them: the repository and commit of the submodule,
// then the rest of the path inside it.

// subLink maps a submodule path in one repository to the pinned commit of
// another.
type subLink struct {
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Path   string `json:"path"`
	Target string `json:"target"` // owner/name
	Commit string `json:"commit"`
}

const subTTL = 24 * time.Hour

func (m *Manager) subFile() string { return filepath.Join(m.Dir, "_submodules.json") }

func (m *Manager) loadSubs() {
	if m.subs != nil {
		return
	}
	m.subs = []subLink{}
	st, err := os.Stat(m.subFile())
	if err != nil || time.Since(st.ModTime()) > subTTL {
		return
	}
	if b, err := os.ReadFile(m.subFile()); err == nil {
		json.Unmarshal(b, &m.subs)
	}
}

func (m *Manager) addSub(l subLink) {
	for _, x := range m.subs {
		if x.Repo == l.Repo && x.Ref == l.Ref && x.Path == l.Path {
			return
		}
	}
	m.subs = append(m.subs, l)
	os.MkdirAll(m.Dir, 0o755)
	b, _ := json.MarshalIndent(m.subs, "", "  ")
	os.WriteFile(m.subFile(), b, 0o644)
}

// applySubs rewrites a folder through the submodules already known.
func (m *Manager) applySubs(f Folder) Folder {
	f.Path = strings.Trim(f.Path, "/")
	for changed := true; changed; {
		changed = false
		for _, s := range m.subs {
			if s.Repo == f.Repo && s.Ref == f.Ref && (f.Path == s.Path || strings.HasPrefix(f.Path, s.Path+"/")) {
				f = Folder{Repo: s.Target, Ref: s.Commit, Path: strings.TrimPrefix(strings.TrimPrefix(f.Path, s.Path), "/")}
				changed = true
				break
			}
		}
	}
	return f
}

// listFolder lists a folder, following submodules on the way. It returns the
// folder as located (repository and ref may differ from the request).
func (m *Manager) listFolder(ctx context.Context, f Folder) ([]dirEntry, Folder, error) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	m.loadSubs()
	for depth := 0; depth < 6; depth++ {
		l := m.applySubs(f)
		entries, err := m.gh.listDir(ctx, l.Repo, l.Ref, l.Path)
		if err == nil {
			return entries, l, nil
		}
		var se *submoduleError
		switch {
		case errors.As(err, &se):
			if err := m.link(l, l.Path, se.entry); err != nil {
				return nil, l, err
			}
		case notFound(err):
			// a submodule somewhere above: walk down from the top
			found, err2 := m.findSub(ctx, l)
			if err2 != nil {
				return nil, l, err2
			}
			if !found {
				return nil, l, err
			}
		default:
			return nil, l, err
		}
	}
	return nil, f, apperr.New(apperr.ClassSchema, apperr.CodeSchema, "too many nested submodules under %s", f.Path)
}

// findSub looks for the first submodule on the way to l.Path and records it.
func (m *Manager) findSub(ctx context.Context, l Folder) (bool, error) {
	parts := strings.Split(l.Path, "/")
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		_, one, err := m.gh.contents(ctx, l.Repo, l.Ref, prefix)
		if err != nil {
			if notFound(err) {
				return false, nil
			}
			return false, err
		}
		if one == nil {
			continue // a folder: go deeper
		}
		if one.Type != "submodule" {
			return false, nil
		}
		return true, m.link(l, prefix, *one)
	}
	return false, nil
}

func notFound(err error) bool {
	e := apperr.As(err)
	return e != nil && e.Code == apperr.CodeNotFound
}

func (m *Manager) link(l Folder, p string, e dirEntry) error {
	target, ok := githubRepo(e.SubmoduleURL)
	if !ok {
		return apperr.New(apperr.ClassSchema, apperr.CodeNotFound, "%s in github.com/%s is a submodule on another host (%s), which isn't supported yet", p, l.Repo, e.SubmoduleURL)
	}
	if e.SHA == "" {
		return apperr.New(apperr.ClassSchema, apperr.CodeSchema, "submodule %s in github.com/%s has no pinned commit", p, l.Repo)
	}
	m.addSub(subLink{Repo: l.Repo, Ref: l.Ref, Path: p, Target: target, Commit: e.SHA})
	return nil
}
