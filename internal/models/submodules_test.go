package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves a tiny YangModels/yang whose vendor/acme is a submodule of
// acme/yang, which in turn has a nested submodule "models" -> acme/models.
type fakeGitHub struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path+"?"+r.URL.RawQuery)
	f.mu.Unlock()
	ref := r.URL.Query().Get("ref")
	dir := func(names ...string) []map[string]string {
		var out []map[string]string
		for _, n := range names {
			out = append(out, map[string]string{"name": n, "type": "dir", "sha": "tree-" + n})
		}
		return out
	}
	var v any
	switch r.URL.Path + "@" + ref {
	case "/api/repos/YangModels/yang/contents/vendor@main":
		v = []map[string]string{{"name": "acme", "type": "file", "sha": "c1"}} // how GitHub lists a submodule
	case "/api/repos/YangModels/yang/contents/vendor/acme@main":
		v = map[string]string{"name": "acme", "path": "vendor/acme", "type": "submodule", "sha": "c1", "submodule_git_url": "https://github.com/acme/yang.git"}
	case "/api/repos/acme/yang/contents/models@c1":
		v = map[string]string{"name": "models", "path": "models", "type": "submodule", "sha": "c2", "submodule_git_url": "git@github.com:acme/models.git"}
	case "/api/repos/acme/models/contents/os@c2":
		v = dir("1.0", "2.0")
	case "/api/repos/acme/models/contents/os/2.0@c2":
		v = dir("2.0R1", "2.0R2")
	case "/api/repos/acme/models/contents/os/1.0@c2":
		v = dir("1.0R1")
	case "/api/repos/acme/models/contents/os/2.0/2.0R2@c2":
		v = dir("conf")
	case "/api/repos/acme/models/git/trees/tree-conf@":
		v = map[string]any{"tree": []map[string]string{{"path": "acme-system.yang", "type": "blob", "sha": "b1"}, {"path": "README", "type": "blob", "sha": "b2"}}}
	case "/raw/acme/models/c2/os/2.0/2.0R2/conf/acme-system.yang@":
		w.Write([]byte("module acme-system { namespace \"urn:acme\"; prefix as; }"))
		return
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(v)
}

func TestSubmodules(t *testing.T) {
	fg := &fakeGitHub{}
	srv := httptest.NewServer(fg)
	defer srv.Close()
	m := &Manager{Dir: filepath.Join(t.TempDir(), "models"), gh: newGitHub(), locks: map[string]*sync.Mutex{}}
	defer m.CloseCatalog()
	m.gh.api, m.gh.raw = srv.URL+"/api", srv.URL+"/raw"
	junos, _ := SourceByID("junos")
	src := &Source{ID: "acme", Name: "Acme", Repo: "YangModels/yang", Kind: "folders", Ref: "main", Parent: "vendor/acme/models/os",
		Group: junos.Group, Pattern: junos.Pattern, Dirs: []string{"conf"}}
	ctx := context.Background()
	names, err := m.list(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "1.0/1.0R1,2.0/2.0R1,2.0/2.0R2" && strings.Join(names, ",") != "2.0/2.0R1,2.0/2.0R2,1.0/1.0R1" {
		t.Fatalf("versions %v", names)
	}
	Sources = append(Sources, src)
	defer func() { Sources = Sources[:len(Sources)-1] }()
	set, err := m.Fetch(ctx, "acme", "2.0/2.0R2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Dirs) != 1 || set.Files != 1 {
		t.Fatalf("set %+v", set)
	}
	if _, err := os.Stat(filepath.Join(set.Dirs[0], "acme-system.yang")); err != nil {
		t.Fatal(err)
	}
	// the submodules are remembered: a second manager doesn't look them up again
	m2 := &Manager{Dir: m.Dir, gh: m.gh, locks: map[string]*sync.Mutex{}}
	fg.calls = nil
	if _, err := m2.list(ctx, src); err != nil {
		t.Fatal(err)
	}
	for _, c := range fg.calls {
		if strings.Contains(c, "YangModels") || strings.Contains(c, "acme/yang") {
			t.Errorf("submodule looked up again: %s", c)
		}
	}
}

func TestGitHubRepo(t *testing.T) {
	for u, want := range map[string]string{
		"https://github.com/Juniper/yang.git": "Juniper/yang", "git@github.com:a/b.git": "a/b",
		"https://gitlab.com/x/y.git": "", "https://github.com/a/b/c": "",
	} {
		if got, _ := githubRepo(u); got != want {
			t.Errorf("%s: %q", u, got)
		}
	}
}
