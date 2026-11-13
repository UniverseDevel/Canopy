package models

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// WebURL returns the GitHub page of a downloaded YANG file at the line given
// (0: none), built from the model source, the release and the file's place in
// the set: for tag releases the tag, for folder releases the branch the
// folder was listed on, and for folders inside Git submodules the pinned
// commit of the other repository. ok is false for files that didn't come from
// GitHub (local folders, NETCONF downloads).
func (m *Manager) WebURL(file string, line int) (string, bool) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", false
	}
	root, _ := filepath.Abs(m.Dir)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		return "", false
	}
	var loc Folder
	switch {
	case parts[0] == "_shared":
		// _shared/<owner_repo>/<path of the shared folder>/<file...>
		inShared := strings.Join(parts[1:], "/")
		found := false
		for _, s := range Sources {
			for _, x := range s.Extra {
				prefix := strings.ReplaceAll(x.Repo, "/", "_") + "/" + x.Path + "/"
				if strings.HasPrefix(inShared, prefix) {
					loc = Folder{Repo: x.Repo, Ref: x.Ref, Path: x.Path + "/" + strings.TrimPrefix(inShared, prefix)}
					found = true
				}
			}
		}
		if !found {
			return "", false
		}
	case strings.HasPrefix(parts[0], "_"):
		return "", false // device downloads and other local folders
	default:
		s, err := SourceByID(parts[0])
		if err != nil {
			return "", false
		}
		version := parts[1]
		if b, err := os.ReadFile(filepath.Join(root, parts[0], parts[1], ".version")); err == nil {
			version = strings.TrimSpace(string(b)) // nested versions (23.4/23.4R2) are stored as 23.4_23.4R2
		}
		inSet := strings.Join(parts[2:], "/")
		switch s.Kind {
		case "tags":
			// the archive keeps the repository's own paths
			loc = Folder{Repo: s.Repo, Ref: version, Path: inSet}
		default:
			loc = Folder{Repo: s.Repo, Ref: s.Ref, Path: joinPath(joinPath(s.Parent, version), inSet)}
		}
	}
	// follow submodules with what is known, however old (a link, not a download)
	m.subMu.Lock()
	m.loadSubs()
	if len(m.subs) == 0 {
		if b, err := os.ReadFile(m.subFile()); err == nil {
			json.Unmarshal(b, &m.subs)
		}
	}
	loc = m.applySubs(loc)
	m.subMu.Unlock()
	u := "https://github.com/" + loc.Repo + "/blob/" + url.PathEscape(loc.Ref) + "/" + escapePath(loc.Path)
	if line > 0 {
		u += "#L" + strconv.Itoa(line)
	}
	return u, true
}
