package fixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// The network-operator checkout chosen in the UI, kept in Canopy's config
// folder with the recently used ones, so that
// switching between, say, a fork and the public repository is one click. It
// applies to everything Canopy does with fixtures, whatever device is used.

// RepoChoice is the saved choice.
type RepoChoice struct {
	Current string   `json:"current,omitempty"` // empty: the default lookup
	Recent  []string `json:"recent"`
}

const maxRecent = 8

var choiceMu sync.Mutex

func choiceFile() string {
	dir := os.Getenv("CANOPY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "canopy")
	}
	return filepath.Join(dir, "operator-repo.json")
}

// Choice reads the saved choice.
func Choice() RepoChoice {
	choiceMu.Lock()
	defer choiceMu.Unlock()
	return readChoice()
}

func readChoice() RepoChoice {
	c := RepoChoice{Recent: []string{}}
	if b, err := os.ReadFile(choiceFile()); err == nil {
		json.Unmarshal(b, &c)
	}
	if c.Recent == nil {
		c.Recent = []string{}
	}
	return c
}

func writeChoice(c RepoChoice) error {
	p := choiceFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return nil
}

func isCheckout(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "test", "gnmi", "testdata"))
	return err == nil && st.IsDir()
}

// Choose makes path the checkout to use (it must be one) and puts it first in
// the recent list; an empty path goes back to the default lookup.
func Choose(path string) (RepoChoice, error) {
	choiceMu.Lock()
	defer choiceMu.Unlock()
	c := readChoice()
	if path == "" {
		c.Current = ""
		return c, writeChoice(c)
	}
	if !isCheckout(path) {
		return c, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "%s is not a network-operator checkout (no test/gnmi/testdata)", path).
			WithHint("choose the repository's top folder")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return c, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	c.Current = abs
	c.Recent = append([]string{abs}, without(c.Recent, abs)...)
	if len(c.Recent) > maxRecent {
		c.Recent = c.Recent[:maxRecent]
	}
	return c, writeChoice(c)
}

// Forget removes path from the recent list (and as the current choice).
func Forget(path string) (RepoChoice, error) {
	choiceMu.Lock()
	defer choiceMu.Unlock()
	c := readChoice()
	c.Recent = without(c.Recent, path)
	if strings.EqualFold(c.Current, path) {
		c.Current = ""
	}
	return c, writeChoice(c)
}

func without(list []string, p string) []string {
	out := []string{}
	for _, x := range list {
		if !strings.EqualFold(filepath.Clean(x), filepath.Clean(p)) {
			out = append(out, x)
		}
	}
	return out
}
