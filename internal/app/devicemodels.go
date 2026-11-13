package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/netconf"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/sshx"
)

// DeviceModelsRequest asks for the models straight from a connected device.
type DeviceModelsRequest struct {
	Session string `json:"session"`
	// Port is the NETCONF port (default 830) on the device's address.
	Port int `json:"port,omitempty"`
	// Host overrides the device's host (when NETCONF listens elsewhere).
	Host           string   `json:"host,omitempty"`
	AcceptHostKeys []string `json:"acceptHostKeys,omitempty"`
	SaveHostKeys   bool     `json:"saveHostKeys,omitempty"`
	// Refresh downloads again instead of using the copy from an earlier run.
	Refresh bool `json:"refresh,omitempty"`
}

// DeviceModelsResult is what was downloaded and loaded.
type DeviceModelsResult struct {
	Dir     string          `json:"dir"`
	Files   int             `json:"files"`
	Skipped []string        `json:"skipped,omitempty"`
	Cached  bool            `json:"cached"`
	Schema  *schema.Summary `json:"schema,omitempty"`
}

// DeviceModels downloads the YANG modules a device implements over NETCONF
// (get-schema, RFC 6022), with the session's credentials and jump hosts, and
// loads them. They are kept per device in the model cache.
func (s *Service) DeviceModels(ctx context.Context, req DeviceModelsRequest, progress func(string)) (*DeviceModelsResult, error) {
	sess, err := s.Session(req.Session)
	if err != nil {
		return nil, err
	}
	o := sess.conn.Opts
	host, _, _ := net.SplitHostPort(o.Address)
	if req.Host != "" {
		host = req.Host
	}
	if req.Port == 0 {
		req.Port = 830
	}
	addr := net.JoinHostPort(host, strconv.Itoa(req.Port))
	dir := filepath.Join(s.Models.Dir, "_device", sanitizeName(addr))
	res := &DeviceModelsResult{Dir: dir}
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil && !req.Refresh {
		res.Cached = true
	} else {
		s.Log.Info("models from device", "session", sess.ID, "netconf", addr)
		tcp, err := sess.conn.DialTCP(ctx, addr)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, err).
				WithHint("NETCONF must be enabled on the device and reachable on port " + strconv.Itoa(req.Port))
		}
		var cfg sshx.Config
		if o.SSH != nil {
			cfg = *o.SSH
		}
		cfg.Jumps = nil
		cfg.AcceptHostKeys = append(append([]string{}, cfg.AcceptHostKeys...), req.AcceptHostKeys...)
		cfg.SaveAcceptedHostKeys = req.SaveHostKeys
		nc, err := netconf.Dial(ctx, tcp, netconf.Options{Address: addr, Username: o.Username, Password: o.Password, SSH: cfg, Timeout: 20 * time.Second})
		if err != nil {
			return nil, err
		}
		defer nc.Close()
		tmp := dir + ".partial"
		os.RemoveAll(tmp)
		n, skipped, err := nc.Download(ctx, tmp, progress)
		if err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		os.RemoveAll(dir)
		if err := os.Rename(tmp, dir); err != nil {
			return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
		}
		os.WriteFile(filepath.Join(dir, ".complete"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
		res.Files, res.Skipped = n, skipped
	}
	if c, err := s.Models.Catalog(); err == nil {
		c.IndexDirs(ctx, "device", addr, []string{dir})
	}
	t, err := s.LoadSchema([]string{dir}, schema.LoadOptions{})
	if err != nil {
		return res, err
	}
	sum := t.Summary()
	res.Schema = &sum
	if res.Files == 0 {
		entries, _ := filepath.Glob(filepath.Join(dir, "*.yang"))
		res.Files = len(entries)
	}
	return res, nil
}

func sanitizeName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|[]`, r) {
			return '_'
		}
		return r
	}, s)
}
