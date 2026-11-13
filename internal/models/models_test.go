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

func TestProfileFolders(t *testing.T) {
	cases := []struct{ source, in, want string }{
		{"nxos", "10.4(3)", "10.4-3"}, {"nxos", "7.0(3)I5(1)", "7.0-3-I5-1"},
		{"iosxr", "7.9.2", "792"}, {"iosxr", "24.2.1", "2421"}, {"iosxe", "17.12.1", "17121"},
		{"junos", "23.4R2.13", "23.4/23.4R2"}, {"junos", "23.4R2-S1.5-EVO", "23.4/23.4R2-EVO"},
		{"sros", "25.10.R1", "sros_25.10.r1"}, {"eos", "4.31.0F", "EOS-4.31.0F"}, {"srlinux", "26.7.1", "v26.7.1"},
		{"nxos", "10.4-3", "10.4-3"}, // already a folder
	}
	for _, c := range cases {
		if got := NormalizeVersion(c.source, c.in); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.source, c.in, got, c.want)
		}
	}
}

func TestDetectProfiles(t *testing.T) {
	ctx := context.Background()
	read := func(v string) Reader {
		return func(_ context.Context, p string) (string, error) { return v, nil }
	}
	cases := []struct {
		caps    []conn.Model
		read    Reader
		profile string
		want    string
	}{
		{[]conn.Model{{Name: "Cisco-IOS-XR-ifmgr-cfg"}, {Name: "openconfig-interfaces"}}, read("7.9.2"), "cisco-iosxr", "792"},
		{[]conn.Model{{Name: "junos-conf-root", Organization: "Juniper Networks"}}, read("JUNOS 23.4R2.13"), "juniper-junos", "23.4/23.4R2"},
		{[]conn.Model{{Name: "nokia-conf"}}, read("B-25.10.R1"), "nokia-sros", "sros_25.10.r1"},
		{[]conn.Model{{Name: "openconfig-system", Organization: "Arista Networks"}}, read("4.31.0F"), "arista-eos", "EOS-4.31.0F"},
		{[]conn.Model{{Name: "huawei-ifm"}}, nil, "huawei-vrp", ""},
	}
	for _, c := range cases {
		d := Detect(ctx, conn.Capabilities{Models: c.caps}, c.read)
		if d.Profile != c.profile || d.Want != c.want {
			t.Errorf("%v: %+v", c.caps[0].Name, d)
		}
	}
}
