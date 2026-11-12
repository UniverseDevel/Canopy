package conn

import (
	"sort"
	"strings"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

// Model is one model advertised in Capabilities.
type Model struct {
	Name         string `json:"name"`
	Organization string `json:"organization,omitempty"`
	Version      string `json:"version,omitempty"`
}

// Capabilities is the JSON form of a CapabilityResponse.
type Capabilities struct {
	GNMIVersion string   `json:"gnmiVersion"`
	Encodings   []string `json:"encodings"`
	Models      []Model  `json:"models"`
}

// ConvertCapabilities converts the response, sorting models by name.
func ConvertCapabilities(r *gpb.CapabilityResponse) Capabilities {
	c := Capabilities{GNMIVersion: r.GetGNMIVersion(), Encodings: []string{}, Models: []Model{}}
	for _, e := range r.GetSupportedEncodings() {
		c.Encodings = append(c.Encodings, EncodingName(e))
	}
	for _, m := range r.GetSupportedModels() {
		c.Models = append(c.Models, Model{Name: m.GetName(), Organization: m.GetOrganization(), Version: m.GetVersion()})
	}
	sort.Slice(c.Models, func(i, j int) bool { return c.Models[i].Name < c.Models[j].Name })
	return c
}

// EncodingName returns the lower-case name gNMIc uses ("json_ietf").
func EncodingName(e gpb.Encoding) string { return strings.ToLower(e.String()) }

// ParseEncoding parses an encoding name; ok is false if unknown.
func ParseEncoding(s string) (gpb.Encoding, bool) {
	v, ok := gpb.Encoding_value[strings.ToUpper(strings.ReplaceAll(s, "-", "_"))]
	return gpb.Encoding(v), ok
}

// PreferredEncoding picks the encoding to use: the requested one, else JSON_IETF,
// else JSON, else the first advertised.
func PreferredEncoding(c Capabilities, requested string) string {
	if requested != "" {
		return strings.ToLower(requested)
	}
	for _, want := range []string{"json_ietf", "json"} {
		for _, e := range c.Encodings {
			if e == want {
				return e
			}
		}
	}
	if len(c.Encodings) > 0 {
		return c.Encodings[0]
	}
	return "json_ietf"
}
