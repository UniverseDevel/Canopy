package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// The cluster settings of the network-operator workspace (kubeconfig, context,
// SSH jump hosts and key to reach device addresses), kept in Canopy's config
// folder so they survive restarts: the UI gets a new port on every start, and
// the browser keeps its storage per port. Requests may still override them,
// and 'canopy ui --kubeconfig ...' gives the defaults under them.

type opSettings struct {
	Kubeconfig    string   `json:"kubeconfig,omitempty"`
	Context       string   `json:"context,omitempty"`
	Jumps         []string `json:"jumps,omitempty"`
	IdentityFiles []string `json:"identityFiles,omitempty"`
	// Direct: reach devices without a jump host, even when 'canopy ui' was
	// started with --operator-jump (an empty Jumps falls back to that).
	Direct bool `json:"direct,omitempty"`
}

var opSettingsMu sync.Mutex

func opSettingsFile() string {
	dir := os.Getenv("CANOPY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "canopy")
	}
	return filepath.Join(dir, "operator.json")
}

func readOpSettings() opSettings {
	opSettingsMu.Lock()
	defer opSettingsMu.Unlock()
	var o opSettings
	if b, err := os.ReadFile(opSettingsFile()); err == nil {
		json.Unmarshal(b, &o)
	}
	return o
}

func writeOpSettings(o opSettings) error {
	opSettingsMu.Lock()
	defer opSettingsMu.Unlock()
	p := opSettingsFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	b, _ := json.MarshalIndent(o, "", "  ")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return nil
}

func (s *Server) opSettingsGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, readOpSettings())
}

func (s *Server) opSettingsPut(w http.ResponseWriter, r *http.Request) {
	var o opSettings
	if err := decode(r, &o); err != nil {
		writeErr(w, err, 0)
		return
	}
	if err := writeOpSettings(o); err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, o)
}
