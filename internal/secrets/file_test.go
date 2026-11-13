package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestFileStore(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755) // a git working tree
	conns := filepath.Join(dir, "lab", "connections.yaml")

	msg, err := Remember(StoreFile, "leaf1", conns, "s3cret!")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lab", "connections.secrets.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "s3cret") || !strings.Contains(string(b), "leaf1:") {
		t.Fatalf("file content:\n%s", b)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, "lab", ".gitignore"))
	if !strings.Contains(string(gi), GitIgnorePattern) || !strings.Contains(msg, ".gitignore") {
		t.Errorf("gitignore %q, message %q", gi, msg)
	}
	if pw, where := Lookup("leaf1", conns); pw != "s3cret!" || where != StoreFile {
		t.Errorf("lookup %q %q", pw, where)
	}
	// an entry can't be moved to another name
	f, _ := readFile(path)
	f.Passwords["leaf2"] = f.Passwords["leaf1"]
	writeFile(path, f)
	if _, ok := FileGet(conns, "leaf2"); ok {
		t.Error("entry moved to another name decrypted")
	}
	// a second save doesn't add the pattern twice
	Remember(StoreFile, "leaf3", conns, "x")
	gi, _ = os.ReadFile(filepath.Join(dir, "lab", ".gitignore"))
	if strings.Count(string(gi), GitIgnorePattern) != 1 {
		t.Errorf("gitignore %q", gi)
	}
	// moving to the keychain removes it from the file
	if _, err := Remember(StoreKeychain, "leaf1", conns, "s3cret!"); err != nil {
		t.Fatal(err)
	}
	if _, ok := FileGet(conns, "leaf1"); ok {
		t.Error("still in the file")
	}
	if _, where := Lookup("leaf1", conns); where != StoreKeychain {
		t.Errorf("where %q", where)
	}
	Forget("leaf2", conns)
	Forget("leaf3", conns)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("empty file not removed: %v", err)
	}
}
