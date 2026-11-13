package models

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func mkset(t *testing.T, dir, version string, files map[string]string) {
	t.Helper()
	for n, c := range files {
		p := filepath.Join(dir, n)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(c), 0o644)
	}
	if version != "" {
		os.WriteFile(filepath.Join(dir, ".version"), []byte(version), 0o644)
	}
	os.WriteFile(filepath.Join(dir, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}

func TestCacheListRemoveShare(t *testing.T) {
	m := &Manager{Dir: filepath.Join(t.TempDir(), "models"), gh: newGitHub(), locks: map[string]*sync.Mutex{}}
	defer m.CloseCatalog()
	common := "module ietf-x { namespace urn:x; prefix x; }"
	mkset(t, m.setDir("srlinux", "v26.7.1"), "v26.7.1", map[string]string{"a.yang": common, "srl.yang": "module srl {}"})
	mkset(t, m.setDir("srlinux", "v26.3.1"), "v26.3.1", map[string]string{"a.yang": common, "srl.yang": "module srl { }"})
	mkset(t, m.setDir("junos", "23.4/23.4R2"), "23.4/23.4R2", map[string]string{"sub/a.yang": common})
	mkset(t, filepath.Join(m.Dir, "_device", "10.0.0.1_830"), "", map[string]string{"d.yang": "module d {}"})
	mkset(t, filepath.Join(m.Dir, "_shared", "YangModels_yang", "standard", "ietf", "RFC"), "", map[string]string{"a.yang": common})

	info, err := m.Cached()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Sets) != 5 {
		t.Fatalf("sets %+v", info.Sets)
	}
	want := map[string]bool{"release/junos/23.4/23.4R2": true, "release/srlinux/v26.7.1": true, "device/device/10.0.0.1_830": true, "shared/shared/YangModels_yang/standard/ietf/RFC": true}
	for _, s := range info.Sets {
		delete(want, s.Kind+"/"+s.Source+"/"+s.Version)
	}
	if len(want) != 0 {
		t.Errorf("missing %v in %+v", want, info.Sets)
	}

	r, err := m.Share(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 7 || r.Linked != 3 || r.SavedBytes != int64(3*len(common)) {
		t.Errorf("share %+v", r)
	}
	a, _ := os.Stat(filepath.Join(m.setDir("srlinux", "v26.7.1"), "a.yang"))
	b, _ := os.Stat(filepath.Join(m.setDir("junos", "23.4/23.4R2"), "sub", "a.yang"))
	if !os.SameFile(a, b) {
		t.Error("identical files not shared")
	}
	if r2, _ := m.Share(nil); r2.SavedBytes != 0 || r2.Linked != 3 {
		t.Errorf("second share %+v", r2)
	}

	if _, err := m.Remove("release", "srlinux", "v26.7.1"); err != nil {
		t.Fatal(err)
	}
	if c, err := os.ReadFile(filepath.Join(m.setDir("junos", "23.4/23.4R2"), "sub", "a.yang")); err != nil || string(c) != common {
		t.Errorf("shared copy lost: %v", err)
	}
	if _, err := m.Remove("release", "srlinux", "v26.7.1"); err == nil {
		t.Error("removed twice")
	}
	info, _ = m.Cached()
	if len(info.Sets) != 4 || info.Share == nil {
		t.Errorf("after remove %+v", info)
	}
}
