// Package buildinfo holds version information set at build time with -ldflags.
package buildinfo

import "runtime/debug"

// Set with -ldflags "-X github.com/UniverseDevel/Canopy/internal/buildinfo.Version=...".
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Info is the version information of this binary.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
	Modified  bool   `json:"modified,omitempty"`
}

// Get returns the build information, filling commit and date from the Go build
// info (VCS stamping) when they weren't set with -ldflags.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, Date: Date}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	return info
}
