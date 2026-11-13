package models

import (
	"context"
	"embed"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/conn"
)

// Platform profiles (docs/ARCHITECTURE.md 5.2): how a platform is recognized
// from Capabilities, where its software version is read, and how a version maps
// to a model release. Built-in profiles are in profiles/; a profile with the
// same id in <config dir>/profiles/*.yaml replaces it, and new ids are added.

// Profile is one platform.
type Profile struct {
	ID          string        `yaml:"id" json:"id"`
	Name        string        `yaml:"name" json:"name"`
	Vendor      string        `yaml:"vendor" json:"vendor,omitempty"`
	OS          string        `yaml:"os" json:"os,omitempty"`
	Source      string        `yaml:"source" json:"source,omitempty"` // model source id
	Fingerprint []Fingerprint `yaml:"fingerprint" json:"fingerprint"`
	Version     VersionRule   `yaml:"version" json:"version"`
	Operator    struct {
		Provider string `yaml:"provider" json:"provider,omitempty"`
	} `yaml:"operator" json:"operator"`
	File string `yaml:"-" json:"file,omitempty"` // where a user profile came from
	rank string
}

// Fingerprint matches Capabilities; any rule of a profile may match.
type Fingerprint struct {
	ModuleRegex       string `yaml:"capabilitiesModuleRegex" json:"capabilitiesModuleRegex,omitempty"`
	OrganizationRegex string `yaml:"organizationRegex" json:"organizationRegex,omitempty"`
}

// VersionRule finds the software version and maps it to a release folder.
type VersionRule struct {
	FromCapabilities *struct {
		Module    string            `yaml:"module" json:"module"`
		Revisions map[string]string `yaml:"revisions" json:"revisions"`
	} `yaml:"fromCapabilities" json:"fromCapabilities,omitempty"`
	GetPaths  []string    `yaml:"getPaths" json:"getPaths,omitempty"`
	Match     string      `yaml:"match" json:"match,omitempty"`
	Normalize []Normalize `yaml:"normalize" json:"normalize,omitempty"`
	Newest    bool        `yaml:"newest" json:"newest,omitempty"`
}

// Normalize rewrites a version into a release folder ($1 ... are groups).
type Normalize struct {
	Match  string `yaml:"match" json:"match"`
	Folder string `yaml:"folder" json:"folder"`
}

//go:embed profiles/*.yaml
var builtinProfiles embed.FS

var profMu sync.Mutex
var profCache []*Profile

// ProfilesDir is where user profiles are read from.
func ProfilesDir() string {
	dir := os.Getenv("CANOPY_CONFIG_DIR")
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "canopy")
	}
	return filepath.Join(dir, "profiles")
}

// Profiles returns the built-in and user profiles in matching order.
func Profiles() ([]*Profile, error) {
	profMu.Lock()
	defer profMu.Unlock()
	if profCache != nil {
		return profCache, nil
	}
	byID := map[string]*Profile{}
	entries, _ := builtinProfiles.ReadDir("profiles")
	for _, e := range entries {
		b, _ := builtinProfiles.ReadFile("profiles/" + e.Name())
		p, err := parseProfile(b, "")
		if err != nil {
			return nil, apperr.New(apperr.ClassInternal, apperr.CodeInternal, "built-in profile %s: %v", e.Name(), err)
		}
		p.rank = e.Name()
		byID[p.ID] = p
	}
	files, _ := filepath.Glob(filepath.Join(ProfilesDir(), "*.yaml"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		p, err := parseProfile(b, f)
		if err != nil {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "profile %s: %v", f, err)
		}
		p.rank = filepath.Base(f)
		if old, ok := byID[p.ID]; ok {
			p.rank = old.rank // keeps its place
		}
		byID[p.ID] = p
	}
	var out []*Profile
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rank < out[j].rank })
	profCache = out
	return out, nil
}

func parseProfile(b []byte, file string) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.ID == "" || len(p.Fingerprint) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "id and fingerprint are required")
	}
	for _, re := range append(p.regexps(), p.Version.Match) {
		if _, err := regexp.Compile(re); err != nil {
			return nil, err
		}
	}
	p.File = file
	return &p, nil
}

func (p *Profile) regexps() []string {
	var out []string
	for _, f := range p.Fingerprint {
		out = append(out, f.ModuleRegex, f.OrganizationRegex)
	}
	for _, n := range p.Version.Normalize {
		out = append(out, n.Match)
	}
	return out
}

func (p *Profile) matches(caps conn.Capabilities) (string, bool) {
	for _, f := range p.Fingerprint {
		for _, m := range caps.Models {
			if f.ModuleRegex != "" && regexp.MustCompile(f.ModuleRegex).MatchString(moduleName(m.Name)) {
				return "Capabilities list " + moduleName(m.Name), true
			}
			if f.OrganizationRegex != "" && m.Organization != "" && regexp.MustCompile(f.OrganizationRegex).MatchString(m.Organization) {
				return "Capabilities name the organization " + m.Organization, true
			}
		}
	}
	return "", false
}

// Folder maps a software version to a release folder with the profile's
// normalize rules; unchanged when none matches.
func (p *Profile) Folder(version string) string {
	v := strings.TrimSpace(version)
	for _, n := range p.Version.Normalize {
		re := regexp.MustCompile(n.Match)
		if m := re.FindStringSubmatchIndex(v); m != nil {
			return string(re.ExpandString(nil, n.Folder, v, m))
		}
	}
	return v
}

// NormalizeVersion maps a version as the device reports it (10.4(3)) to the
// release folder of a model source (10.4-3), using the source's profile.
func NormalizeVersion(source, version string) string {
	ps, _ := Profiles()
	for _, p := range ps {
		if p.Source == source {
			if f := p.Folder(version); f != version {
				return f
			}
		}
	}
	return version
}

// Detection is the platform guess for a device.
type Detection struct {
	Profile string `json:"profile,omitempty"` // profile id
	Vendor  string `json:"vendor,omitempty"`
	OS      string `json:"os,omitempty"`
	Source  string `json:"source,omitempty"` // model source id; empty if unknown
	// DeviceVersion is the software version as the device reports it.
	DeviceVersion string `json:"deviceVersion,omitempty"`
	// Want is the model version that matches the device exactly.
	Want string `json:"want,omitempty"`
	// Version is the model version to use: Want if available, else the nearest.
	Version string `json:"version,omitempty"`
	Exact   bool   `json:"exact"`
	Reason  string `json:"reason"`
	// Provider is network-operator's provider name for the platform.
	Provider string `json:"provider,omitempty"`
	newest   bool
}

// Reader reads a single string value from the device (a gNMI Get).
type Reader func(ctx context.Context, path string) (string, error)

// moduleName strips a namespace prefix: SR Linux reports "urn:...:srl_nokia-interfaces".
func moduleName(n string) string {
	if i := strings.LastIndex(n, ":"); i >= 0 {
		return n[i+1:]
	}
	return n
}

// Detect identifies the platform from Capabilities with the profiles and, where
// a profile says so, a version read from the device. It doesn't contact GitHub;
// Resolve picks the available version.
func Detect(ctx context.Context, caps conn.Capabilities, read Reader) Detection {
	ps, err := Profiles()
	if err != nil {
		return Detection{Reason: err.Error()}
	}
	for _, p := range ps {
		why, ok := p.matches(caps)
		if !ok {
			continue
		}
		d := Detection{Profile: p.ID, Vendor: p.Vendor, OS: p.OS, Source: p.Source, Provider: p.Operator.Provider, Reason: p.Name + ": " + why}
		v := p.Version
		if fc := v.FromCapabilities; fc != nil {
			for _, m := range caps.Models {
				if moduleName(m.Name) != fc.Module {
					continue
				}
				if rel, ok := fc.Revisions[m.Version]; ok {
					d.DeviceVersion = rel
				} else {
					d.Reason += "; unknown " + fc.Module + " revision " + m.Version
				}
			}
		}
		if d.DeviceVersion == "" && read != nil {
			var errs []string
			for _, path := range v.GetPaths {
				val, err := read(ctx, path)
				if err != nil {
					errs = append(errs, err.Error())
					continue
				}
				if v.Match != "" {
					if m := regexp.MustCompile(v.Match).FindStringSubmatch(val); len(m) > 1 {
						val = m[1]
					} else {
						errs = append(errs, "unrecognized version "+val)
						continue
					}
				}
				d.DeviceVersion = val
				break
			}
			if d.DeviceVersion == "" && len(errs) > 0 {
				d.Reason += "; version not readable: " + errs[0]
			}
		}
		if d.DeviceVersion != "" {
			d.Want = p.Folder(d.DeviceVersion)
		}
		d.newest = v.Newest
		switch {
		case v.Newest:
			d.Reason += "; using the newest release"
		case d.Want == "":
			d.Reason += "; pick the release"
		}
		return d
	}
	return Detection{Reason: "platform not recognized from Capabilities; pick a model source or a local folder"}
}

// Resolve fills Version (and Exact) from the available versions: the wanted one,
// the nearest lower one, or the newest when the profile says so.
func (m *Manager) Resolve(ctx context.Context, d *Detection) error {
	if d.Source == "" {
		return nil
	}
	vs, err := m.Versions(ctx, d.Source, false)
	if err != nil {
		return err
	}
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.Name
	}
	if len(names) == 0 {
		return nil
	}
	if d.Want == "" {
		if d.newest || d.Source == "openconfig" {
			d.Version = names[0]
		}
		return nil
	}
	v, _ := Nearest(names, d.Want)
	d.Version = v
	d.Exact = v == d.Want
	if !d.Exact {
		d.Reason += "; " + d.Want + " isn't published, using " + v
	}
	return nil
}
