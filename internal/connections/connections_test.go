package connections

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "sub", "connections.yaml"))
	if l, err := s.List(); err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}
	if err := s.Put(Connection{Name: "lab srl1", Address: "127.0.0.1:57400", SkipVerify: true, Username: "admin", YangDirs: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Connection{Name: "a-dev", Address: "10.0.0.1:9339", PasswordEnv: "A_PW"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Touch("LAB SRL1", Seen{Time: time.Now(), Models: 384}); err != nil {
		t.Fatal(err)
	}
	// Re-saving keeps LastSeen.
	if err := s.Put(Connection{Name: "lab srl1", Address: "127.0.0.1:57401", Insecure: true}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get("lab srl1")
	if err != nil || c.Address != "127.0.0.1:57401" || c.LastSeen == nil || c.LastSeen.Models != 384 {
		t.Fatalf("get: %+v %v", c, err)
	}
	l, _ := s.List()
	if len(l) != 2 || l[0].Name != "a-dev" {
		t.Errorf("list order: %+v", l)
	}
	data, _ := os.ReadFile(s.Path)
	if !strings.Contains(string(data), "never stored") || strings.Contains(strings.ToLower(string(data)), "password:") {
		t.Errorf("file:\n%s", data)
	}
	if err := s.Delete("a-dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a-dev"); err == nil {
		t.Error("deleted preset still there")
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []Connection{{Name: "", Address: "x:1"}, {Name: "ok", Address: ""}, {Name: "ok", Address: "x:1", PasswordEnv: "1BAD"}, {Name: "../x", Address: "x:1"}} {
		if c.Validate() == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestExtraReadOnly(t *testing.T) {
	dir := t.TempDir()
	extra := filepath.Join(dir, "lab", "connections.yaml")
	os.MkdirAll(filepath.Dir(extra), 0o755)
	os.WriteFile(extra, []byte("version: 1\nconnections:\n  - name: lab\n    address: 127.0.0.1:57400\n    yangDirs: [models/x]\n  - name: shadowed\n    address: 1.1.1.1:1\n"), 0o644)
	s := &Store{Path: filepath.Join(dir, "user.yaml"), Extra: []string{extra}}
	s.Put(Connection{Name: "shadowed", Address: "2.2.2.2:2"})
	l, err := s.List()
	if err != nil || len(l) != 2 {
		t.Fatalf("list %+v %v", l, err)
	}
	c, _ := s.Get("lab")
	if !c.ReadOnly || c.YangDirs[0] != filepath.Join(dir, "lab", "models", "x") {
		t.Errorf("lab = %+v", c)
	}
	if c, _ := s.Get("shadowed"); c.Address != "2.2.2.2:2" || c.ReadOnly {
		t.Errorf("user preset should win: %+v", c)
	}
	if err := s.Delete("lab"); err == nil {
		t.Error("deleting a read-only preset should fail")
	}
	if err := s.Touch("lab", Seen{}); err != nil {
		t.Error(err)
	}
	if data, _ := os.ReadFile(extra); strings.Contains(string(data), "lastSeen") {
		t.Error("read-only file was written")
	}
}
