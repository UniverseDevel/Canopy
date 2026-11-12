package models

import (
	"context"
	"regexp"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/conn"
)

// Detection is the platform guess for a device.
type Detection struct {
	Source string `json:"source,omitempty"` // model source id; empty if unknown
	// DeviceVersion is the software version as the device reports it.
	DeviceVersion string `json:"deviceVersion,omitempty"`
	// Want is the model version that matches the device exactly.
	Want string `json:"want,omitempty"`
	// Version is the model version to use: Want if available, else the nearest.
	Version string `json:"version,omitempty"`
	Exact   bool   `json:"exact"`
	Reason  string `json:"reason"`
}

// Reader reads a single string value from the device (a gNMI Get).
type Reader func(ctx context.Context, path string) (string, error)

// NX-OS: the revision of Cisco-NX-OS-device identifies the release. This is how
// network-operator detects it (internal/provider/cisco/nxos/version.go).
var nxosRevisions = map[string]string{
	"2024-03-26": "10.4-3", "2024-10-17": "10.4-4", "2025-03-01": "10.4-5", "2025-08-30": "10.4-6",
	"2024-07-25": "10.5-1", "2024-11-26": "10.5-2", "2025-04-23": "10.5-3",
	"2025-08-12": "10.6-1", "2025-12-12": "10.6-2", "2026-04-24": "10.6-3",
}

var srlVersion = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)

// moduleName strips a namespace prefix: SR Linux reports "urn:...:srl_nokia-interfaces".
func moduleName(n string) string {
	if i := strings.LastIndex(n, ":"); i >= 0 {
		return n[i+1:]
	}
	return n
}

// Detect identifies the platform from Capabilities and, where needed, a version
// read from the device. It doesn't contact GitHub; ResolveDetection picks the
// available version.
func Detect(ctx context.Context, caps conn.Capabilities, read Reader) Detection {
	has := func(pred func(name, org string) bool) (conn.Model, bool) {
		for _, m := range caps.Models {
			if pred(moduleName(m.Name), m.Organization) {
				return m, true
			}
		}
		return conn.Model{}, false
	}
	if _, ok := has(func(n, _ string) bool { return strings.HasPrefix(n, "srl_nokia") }); ok {
		d := Detection{Source: "srlinux", Reason: "Capabilities list srl_nokia models"}
		if read != nil {
			if v, err := read(ctx, "/system/information/version"); err == nil {
				d.DeviceVersion = v
				if m := srlVersion.FindStringSubmatch(v); m != nil {
					d.Want = "v" + m[1]
				}
			} else {
				d.Reason += "; version not readable: " + err.Error()
			}
		}
		return d
	}
	if m, ok := has(func(n, _ string) bool { return n == "Cisco-NX-OS-device" }); ok {
		d := Detection{Source: "nxos", Reason: "Capabilities list Cisco-NX-OS-device", DeviceVersion: "revision " + m.Version}
		if f, ok := nxosRevisions[m.Version]; ok {
			d.Want = f
		} else {
			d.Reason += " with an unknown revision " + m.Version + "; pick the release"
		}
		return d
	}
	if _, ok := has(func(n, _ string) bool { return strings.HasPrefix(n, "Cisco-IOS-XR-") }); ok {
		return Detection{Source: "iosxr", Reason: "Capabilities list Cisco-IOS-XR models; pick the release"}
	}
	if _, ok := has(func(n, _ string) bool { return strings.HasPrefix(n, "Cisco-IOS-XE-") }); ok {
		return Detection{Source: "iosxe", Reason: "Capabilities list Cisco-IOS-XE models; pick the release"}
	}
	if _, ok := has(func(n, _ string) bool { return strings.HasPrefix(n, "openconfig-") }); ok {
		return Detection{Source: "openconfig", Reason: "Capabilities list OpenConfig models; using the newest OpenConfig release"}
	}
	return Detection{Reason: "platform not recognized from Capabilities; pick a model source or a local folder"}
}

// Resolve fills Version (and Exact) from the available versions: the wanted one,
// the nearest lower one, or the newest when no version is known.
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
		if d.Source == "openconfig" {
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
