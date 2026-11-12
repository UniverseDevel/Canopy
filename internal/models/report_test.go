package models

import (
	"testing"

	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/schema"
)

func TestReport(t *testing.T) {
	caps := conn.Capabilities{Models: []conn.Model{
		{Name: "urn:x:srl_nokia-system", Version: "2026-07-31"},
		{Name: "openconfig-interfaces", Version: "3.5.0"},
		{Name: "ietf-inet-types", Version: "2013-07-15"},
		{Name: "vendor-thing"},
		{Name: "not-loaded", Version: "2020-01-01"},
	}}
	mods := []schema.Module{
		{Name: "srl_nokia-system", Revision: "2026-07-31"},
		{Name: "openconfig-interfaces", Revision: "2023-01-01", Version: "3.6.0"},
		{Name: "ietf-inet-types", Revision: "2013-07-15"},
		{Name: "vendor-thing", Revision: "2021-01-01"},
		{Name: "ietf-yang-types", Revision: "2013-07-15"},
	}
	r := Report(caps, mods)
	want := map[string]int{MatchOK: 2, MatchVersion: 1, MatchUnknown: 1, MatchMissing: 1, MatchNotAdv: 1}
	for k, v := range want {
		if r.Counts[k] != v {
			t.Errorf("%s: got %d want %d (%+v)", k, r.Counts[k], v, r.Entries)
		}
	}
	if r.Entries[0].Status != MatchMissing || r.Entries[0].Module != "not-loaded" {
		t.Errorf("missing first: %+v", r.Entries[0])
	}
}
