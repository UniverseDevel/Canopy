package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// Managing the model cache: list what is downloaded, remove sets that aren't
// needed, and, on request, store identical files once (hard links).

// CachedSet is a downloaded set of models.
type CachedSet struct {
	Kind       string    `json:"kind"`   // release, device (NETCONF) or shared (imports only)
	Source     string    `json:"source"` // model source id, "device" or "shared"
	Version    string    `json:"version"`
	Dir        string    `json:"dir"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"` // size of its files (shared files count in every set)
	Downloaded time.Time `json:"downloaded"`
}

// CacheInfo is the whole cache.
type CacheInfo struct {
	Dir   string      `json:"dir"`
	Sets  []CachedSet `json:"sets"`
	Bytes int64       `json:"bytes"` // sum of the sets
	// Share is the result of the last Share, if any.
	Share *ShareResult `json:"share,omitempty"`
}

// ShareResult says what Share did.
type ShareResult struct {
	Files      int       `json:"files"`      // files looked at
	Linked     int       `json:"linked"`     // files now stored once with others
	SavedBytes int64     `json:"savedBytes"` // space freed by this run
	Time       time.Time `json:"time"`
}

func setStats(dir string) (files int, bytes int64) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yang") {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			files++
			bytes += fi.Size()
		}
		return nil
	})
	return
}

func completeTime(dir string) (time.Time, bool) {
	b, err := os.ReadFile(filepath.Join(dir, ".complete"))
	if err != nil {
		return time.Time{}, false
	}
	t, _ := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	return t, true
}

// Cached lists the downloaded sets.
func (m *Manager) Cached() (*CacheInfo, error) {
	info := &CacheInfo{Dir: m.Dir, Sets: []CachedSet{}}
	add := func(kind, source, version, dir string) {
		t, ok := completeTime(dir)
		if !ok {
			return
		}
		n, b := setStats(dir)
		info.Sets = append(info.Sets, CachedSet{Kind: kind, Source: source, Version: version, Dir: dir, Files: n, Bytes: b, Downloaded: t})
		info.Bytes += b
	}
	tops, err := os.ReadDir(m.Dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	for _, top := range tops {
		if !top.IsDir() {
			continue
		}
		base := filepath.Join(m.Dir, top.Name())
		switch top.Name() {
		case "_device":
			entries, _ := os.ReadDir(base)
			for _, e := range entries {
				if e.IsDir() && !strings.HasSuffix(e.Name(), ".partial") {
					add("device", "device", e.Name(), filepath.Join(base, e.Name()))
				}
			}
		case "_shared":
			// shared folders sit deeper (repo/path); each has its own .complete
			filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.IsDir() || p == base {
					return nil
				}
				if _, ok := completeTime(p); ok {
					rel, _ := filepath.Rel(base, p)
					add("shared", "shared", filepath.ToSlash(rel), p)
					return filepath.SkipDir
				}
				return nil
			})
		default:
			if strings.HasPrefix(top.Name(), "_") {
				continue
			}
			for _, v := range m.cachedVersions(top.Name()) {
				add("release", top.Name(), v, m.setDir(top.Name(), v))
			}
		}
	}
	sort.Slice(info.Sets, func(i, j int) bool {
		a, b := info.Sets[i], info.Sets[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return CompareVersions(a.Version, b.Version) > 0
	})
	if b, err := os.ReadFile(m.shareFile()); err == nil {
		var r ShareResult
		if json.Unmarshal(b, &r) == nil {
			info.Share = &r
		}
	}
	return info, nil
}

// Remove deletes a downloaded set; it is downloaded again when needed. The
// catalog keeps the release, so it stays searchable.
func (m *Manager) Remove(kind, source, version string) (*CachedSet, error) {
	info, err := m.Cached()
	if err != nil {
		return nil, err
	}
	for _, s := range info.Sets {
		if s.Kind != kind || s.Source != source || s.Version != version {
			continue
		}
		// only ever inside the cache
		if rel, err := filepath.Rel(m.Dir, s.Dir); err != nil || strings.HasPrefix(rel, "..") || rel == "." {
			return nil, apperr.New(apperr.ClassInternal, apperr.CodeInternal, "%s is outside the model cache", s.Dir)
		}
		if err := os.RemoveAll(s.Dir); err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("close programs that use files in " + s.Dir)
		}
		return &s, nil
	}
	return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "%s %s %s is not downloaded", kind, source, version)
}

func (m *Manager) shareFile() string { return filepath.Join(m.Dir, "_share.json") }

// Share stores identical files of the downloaded sets once: duplicates become
// hard links to one copy (the cache never changes a file in place, so this is
// safe). Removing a set later only frees what no other set shares.
func (m *Manager) Share(progress func(string)) (*ShareResult, error) {
	res := &ShareResult{Time: time.Now().UTC()}
	type key struct {
		size int64
		hash string
	}
	first := map[key]string{}
	var errs []string
	err := filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasSuffix(p, ".partial") {
				return filepath.SkipDir // a download in progress
			}
			return nil
		}
		if !strings.HasSuffix(p, ".yang") {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		res.Files++
		h, err := fileHash(p)
		if err != nil {
			return nil
		}
		k := key{fi.Size(), h}
		orig, seen := first[k]
		if !seen {
			first[k] = p
			return nil
		}
		ofi, err := os.Stat(orig)
		if err != nil {
			return nil
		}
		if os.SameFile(fi, ofi) {
			res.Linked++ // already shared
			return nil
		}
		tmp := p + ".link"
		os.Remove(tmp)
		if err := os.Link(orig, tmp); err != nil {
			errs = append(errs, err.Error())
			return nil
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(tmp)
			errs = append(errs, err.Error())
			return nil
		}
		res.Linked++
		res.SavedBytes += fi.Size()
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if res.Linked == 0 && len(errs) > 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "files could not be shared: %s", errs[0]).
			WithHint("the file system may not support hard links")
	}
	if progress != nil {
		progress("shared " + itoa(res.Linked) + " of " + itoa(res.Files) + " files")
	}
	b, _ := json.Marshal(res)
	os.WriteFile(m.shareFile(), b, 0o644)
	return res, nil
}

func fileHash(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
