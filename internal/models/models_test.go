package models

import (
	"context"
	"reflect"
	"testing"

	"github.com/UniverseDevel/Canopy/internal/conn"
)

func TestSortAndNearest(t *testing.T) {
	v := []string{"v25.3.1", "v26.7.1", "v25.10.3", "v26.3.1", "v9.1.0"}
	SortVersions(v)
	if !reflect.DeepEqual(v, []string{"v26.7.1", "v26.3.1", "v25.10.3", "v25.3.1", "v9.1.0"}) {
		t.Errorf("sorted %v", v)
	}
	cases := map[string]string{"v26.7.1": "v26.7.1", "v26.5.0": "v26.3.1", "v1.0.0": "v9.1.0", "v99.0.0": "v26.7.1"}
	for want, exp := range cases {
		if got, _ := Nearest(v, want); got != exp {
			t.Errorf("Nearest(%s) = %s, want %s", want, got, exp)
		}
	}
	nx := []string{"10.4-3", "10.4-10", "10.5-1", "9.3-12"}
	SortVersions(nx)
	if nx[0] != "10.5-1" || nx[1] != "10.4-10" {
		t.Errorf("nx sorted %v", nx)
	}
}

func TestDetect(t *testing.T) {
	read := func(_ context.Context, p string) (string, error) { return "v26.7.1-554-g78ed635f70a", nil }
	d := Detect(context.Background(), conn.Capabilities{Models: []conn.Model{{Name: "urn:nokia.com:srlinux:chassis:interfaces:srl_nokia-interfaces"}}}, read)
	if d.Source != "srlinux" || d.Want != "v26.7.1" {
		t.Errorf("srl %+v", d)
	}
	d = Detect(context.Background(), conn.Capabilities{Models: []conn.Model{{Name: "Cisco-NX-OS-device", Version: "2024-03-26"}}}, nil)
	if d.Source != "nxos" || d.Want != "10.4-3" {
		t.Errorf("nxos %+v", d)
	}
	d = Detect(context.Background(), conn.Capabilities{Models: []conn.Model{{Name: "openconfig-interfaces"}}}, nil)
	if d.Source != "openconfig" {
		t.Errorf("oc %+v", d)
	}
	if d := Detect(context.Background(), conn.Capabilities{}, nil); d.Source != "" {
		t.Errorf("empty %+v", d)
	}
}

func TestSafeRel(t *testing.T) {
	for p, ok := range map[string]bool{"a/b.yang": true, "../x": false, "/abs": false, "a/../../b": false, `a\b`: false, "c:/x": false} {
		if safeRel(p) != ok {
			t.Errorf("safeRel(%q) != %v", p, ok)
		}
	}
}
