package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// Encrypted password file: <connections file>.secrets.yaml next to the file a
// saved connection comes from (for example connections.secrets.yaml). The
// passwords are encrypted with AES-256-GCM; the key is generated once per
// computer and kept in the OS keychain, so the file alone reveals nothing and
// can't be used on another computer. Canopy makes sure git ignores the file.

const (
	fileKeyAccount = "secrets-file-key"
	// GitIgnorePattern is what keeps the files out of version control.
	GitIgnorePattern = "*.secrets.yaml"
)

// Stores where a password can be kept.
const (
	StoreKeychain = "keychain"
	StoreFile     = "file"
)

type secretsFile struct {
	Version   int               `yaml:"version"`
	Passwords map[string]string `yaml:"passwords"`
}

var fileMu sync.Mutex

// FilePath returns the secrets file for a connections file.
func FilePath(connectionsFile string) string {
	ext := filepath.Ext(connectionsFile)
	return strings.TrimSuffix(connectionsFile, ext) + ".secrets.yaml"
}

func fileKey(create bool) ([]byte, error) {
	s, err := keyring.Get(service, fileKeyAccount)
	if err == nil {
		k, derr := base64.StdEncoding.DecodeString(s)
		if derr == nil && len(k) == 32 {
			return k, nil
		}
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "the password file key in the OS keychain is damaged")
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("the encrypted password file needs the OS keychain for its key")
	}
	if !create {
		return nil, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if err := keyring.Set(service, fileKeyAccount, base64.StdEncoding.EncodeToString(k)); err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("the encrypted password file needs the OS keychain for its key")
	}
	return k, nil
}

func readFile(path string) (*secretsFile, error) {
	f := &secretsFile{Version: 1, Passwords: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if err := yaml.Unmarshal(b, f); err != nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "%s: %v", path, err)
	}
	if f.Passwords == nil {
		f.Passwords = map[string]string{}
	}
	return f, nil
}

func writeFile(path string, f *secretsFile) error {
	var b strings.Builder
	b.WriteString("# Canopy device passwords, encrypted (AES-256-GCM). The key is in the OS keychain\n")
	b.WriteString("# of the computer that wrote them, so this file is useless elsewhere.\n")
	b.WriteString("# Keep it out of version control (" + GitIgnorePattern + ").\n")
	b.WriteString("version: 1\npasswords:\n")
	names := make([]string, 0, len(f.Passwords))
	for n := range f.Passwords {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		k, _ := yaml.Marshal(n)
		b.WriteString("  " + strings.TrimSpace(string(k)) + ": " + f.Passwords[n] + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return nil
}

func seal(key []byte, name, pw string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	// the connection name is authenticated: an entry can't be moved to another name
	out := gcm.Seal(nonce, nonce, []byte(pw), []byte(name))
	return base64.StdEncoding.EncodeToString(out), nil
}

func open(key []byte, name, enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("too short")
	}
	pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(name))
	return string(pt), err
}

// FileGet reads a password from the secrets file of a connections file. ok is
// false when there is none, or when it was written on another computer.
func FileGet(connectionsFile, name string) (string, bool) {
	if connectionsFile == "" {
		return "", false
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	f, err := readFile(FilePath(connectionsFile))
	if err != nil || f.Passwords[name] == "" {
		return "", false
	}
	key, err := fileKey(false)
	if err != nil || key == nil {
		return "", false
	}
	pw, err := open(key, name, f.Passwords[name])
	if err != nil {
		return "", false
	}
	return pw, true
}

// FileSet stores a password in the secrets file of a connections file and makes
// sure git ignores the file. It returns the file's path and a note when a
// .gitignore was changed.
func FileSet(connectionsFile, name, pw string) (string, string, error) {
	if connectionsFile == "" {
		return "", "", apperr.New(apperr.ClassUsage, apperr.CodeUsage, "connection %q has no file to keep a password next to", name)
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	path := FilePath(connectionsFile)
	key, err := fileKey(true)
	if err != nil {
		return "", "", err
	}
	note, err := ensureIgnored(path)
	if err != nil {
		return "", "", err
	}
	f, err := readFile(path)
	if err != nil {
		return "", "", err
	}
	enc, err := seal(key, name, pw)
	if err != nil {
		return "", "", apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	f.Passwords[name] = enc
	return path, note, writeFile(path, f)
}

// FileDelete removes a password from the secrets file (none is fine).
func FileDelete(connectionsFile, name string) error {
	if connectionsFile == "" {
		return nil
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	path := FilePath(connectionsFile)
	f, err := readFile(path)
	if err != nil {
		return err
	}
	if _, ok := f.Passwords[name]; !ok {
		return nil
	}
	delete(f.Passwords, name)
	if len(f.Passwords) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		return nil
	}
	return writeFile(path, f)
}

// ensureIgnored makes sure a file inside a git working tree is ignored, adding
// the pattern to a .gitignore in the file's folder when it isn't yet.
func ensureIgnored(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", nil
	}
	dir := filepath.Dir(abs)
	if !inGitTree(dir) {
		return "", nil
	}
	if git, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command(git, "-C", dir, "check-ignore", "-q", "--no-index", filepath.Base(abs))
		if cmd.Run() == nil {
			return "", nil // already ignored
		}
	}
	os.MkdirAll(dir, 0o755)
	gi := filepath.Join(dir, ".gitignore")
	b, _ := os.ReadFile(gi)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == GitIgnorePattern {
			return "", nil
		}
	}
	text := string(b)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "# Canopy's encrypted device passwords\n" + GitIgnorePattern + "\n"
	if err := os.WriteFile(gi, []byte(text), 0o644); err != nil {
		return "", apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("add " + GitIgnorePattern + " to .gitignore yourself")
	}
	return "added " + GitIgnorePattern + " to " + gi, nil
}

func inGitTree(dir string) bool {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		p := filepath.Dir(d)
		if p == d {
			return false
		}
		d = p
	}
}

// Lookup finds a stored password for a saved connection: the OS keychain
// first, then the encrypted file next to its connections file. where is
// StoreKeychain, StoreFile or "".
func Lookup(name, connectionsFile string) (pw, where string) {
	if p, ok := Get(name); ok {
		return p, StoreKeychain
	}
	if p, ok := FileGet(connectionsFile, name); ok {
		return p, StoreFile
	}
	return "", ""
}

// Remember stores a password in the chosen store and removes it from the other.
// It returns a note for the user (where it went, a .gitignore change).
func Remember(store, name, connectionsFile, pw string) (string, error) {
	switch store {
	case StoreKeychain, "":
		if err := Set(name, pw); err != nil {
			return "", err
		}
		FileDelete(connectionsFile, name)
		return "stored in the OS keychain", nil
	case StoreFile:
		path, note, err := FileSet(connectionsFile, name, pw)
		if err != nil {
			return "", err
		}
		Delete(name)
		msg := "stored encrypted in " + path
		if note != "" {
			msg += "; " + note
		}
		return msg, nil
	}
	return "", apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown password store %q (keychain, file)", store)
}

// Forget removes a password from both stores.
func Forget(name, connectionsFile string) error {
	err1 := Delete(name)
	err2 := FileDelete(connectionsFile, name)
	if err1 != nil {
		return err1
	}
	return err2
}
