package models

import (
	"regexp"
	"sort"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/schema"
)

// Match statuses.
const (
	MatchOK      = "match"    // same revision or OpenConfig version
	MatchVersion = "mismatch" // loaded, but another revision or version
	MatchUnknown = "loaded"   // loaded; the device gave no comparable version
	MatchMissing = "missing"  // advertised by the device, not loaded
	MatchNotAdv  = "extra"    // loaded, not advertised (imports, other releases)
)

// MatchEntry compares one module.
type MatchEntry struct {
	Module     string `json:"module"`
	Status     string `json:"status"`
	Advertised string `json:"advertised,omitempty"` // version from Capabilities
	Loaded     string `json:"loaded,omitempty"`     // revision (and OpenConfig version) loaded
	File       string `json:"file,omitempty"`
}

// MatchReport is the comparison between Capabilities and the loaded models.
type MatchReport struct {
	Counts  map[string]int `json:"counts"`
	Entries []MatchEntry   `json:"entries"`
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// Report compares the models advertised in Capabilities with the loaded modules.
// Capabilities may name a module bare ("openconfig-interfaces") or with a
// namespace ("urn:...:srl_nokia-interfaces"); the version is a revision date or,
// for OpenConfig, the openconfig-version.
func Report(caps conn.Capabilities, mods []schema.Module) MatchReport {
	loaded := map[string]schema.Module{}
	for _, m := range mods {
		loaded[m.Name] = m
	}
	r := MatchReport{Counts: map[string]int{}}
	seen := map[string]bool{}
	for _, cm := range caps.Models {
		name := moduleName(cm.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		e := MatchEntry{Module: name, Advertised: cm.Version}
		m, ok := loaded[name]
		switch {
		case !ok:
			e.Status = MatchMissing
		default:
			e.File = m.File
			e.Loaded = m.Revision
			if m.Version != "" {
				e.Loaded += " (" + m.Version + ")"
			}
			v := strings.TrimSpace(cm.Version)
			switch {
			case v == "":
				e.Status = MatchUnknown
			case dateRe.MatchString(v):
				e.Status = pick(v == m.Revision)
			case semverRe.MatchString(v) && m.Version != "":
				e.Status = pick(v == m.Version)
			default:
				e.Status = MatchUnknown
			}
		}
		r.Entries = append(r.Entries, e)
	}
	for _, m := range mods {
		if !seen[m.Name] {
			r.Entries = append(r.Entries, MatchEntry{Module: m.Name, Status: MatchNotAdv, Loaded: m.Revision, File: m.File})
		}
	}
	order := map[string]int{MatchMissing: 0, MatchVersion: 1, MatchUnknown: 2, MatchOK: 3, MatchNotAdv: 4}
	sort.SliceStable(r.Entries, func(i, j int) bool {
		a, b := r.Entries[i], r.Entries[j]
		if order[a.Status] != order[b.Status] {
			return order[a.Status] < order[b.Status]
		}
		return a.Module < b.Module
	})
	for _, e := range r.Entries {
		r.Counts[e.Status]++
	}
	return r
}

func pick(ok bool) string {
	if ok {
		return MatchOK
	}
	return MatchVersion
}
