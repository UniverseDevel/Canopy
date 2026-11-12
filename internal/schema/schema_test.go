package schema

import (
	"strings"
	"testing"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

func load(t *testing.T) *Tree {
	t.Helper()
	tr, err := Load([]string{"testdata/ok"}, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestLoadPicksNewestRevisionAndIndexes(t *testing.T) {
	tr := load(t)
	if tr.FindPath("/old") != nil {
		t.Error("older revision of ex-base was loaded")
	}
	var roots []string
	for _, n := range tr.Children(-1) {
		roots = append(roots, n.Path)
	}
	if strings.Join(roots, ",") != "/interface[name=*],/system" {
		t.Errorf("roots = %v", roots)
	}
	ifc := tr.FindPath("/interface")
	if ifc == nil || ifc.Kind != "list" || len(ifc.Keys) != 1 || ifc.QPath != "/ex-base:interface[name=*]" {
		t.Fatalf("interface = %+v", ifc)
	}
}

func TestLeafDetails(t *testing.T) {
	tr := load(t)
	mtu := tr.FindPath("/interface/mtu")
	if mtu == nil || mtu.Type.Base != "uint16" || mtu.Type.Range != "1500..9500" || mtu.Units != "bytes" || !mtu.Config {
		t.Fatalf("mtu = %+v %+v", mtu, mtu.Type)
	}
	as := tr.FindPath("/interface/admin-state")
	if as.Type.Name != "admin-state" || strings.Join(as.Type.Enums, ",") != "enable,disable" || as.Default[0] != "enable" {
		t.Errorf("admin-state = %+v %+v", as, as.Type)
	}
	v := tr.FindPath("/system/information/version")
	if v.Config || v.Description != "Software version." || v.Path != "/system/information/version" {
		t.Errorf("version = %+v", v)
	}
	ip := tr.FindPath("/interface/ip")
	if ip == nil || ip.Choice != "mode/routed" {
		t.Errorf("choice not flattened: %+v", ip)
	}
	d := tr.FindPath("/interface/description")
	if d == nil || d.Module != "ex-aug" {
		t.Errorf("augment: %+v", d)
	}
}

func TestSearch(t *testing.T) {
	tr := load(t)
	res := tr.Search("in-octets", 0)
	if len(res) != 1 || res[0].Path != "/interface[name=*]/statistics/in-octets" {
		t.Errorf("search = %v", res)
	}
	res = tr.Search("name", 0)
	if len(res) < 2 || res[0].Name != "name" {
		t.Errorf("exact match not first: %v", res)
	}
}

func TestSource(t *testing.T) {
	tr := load(t)
	n := tr.FindPath("/interface/mtu")
	_, _, src, err := tr.Source(n.ID, 50)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(src), "leaf mtu") {
		t.Errorf("source = %q, %v", src, err)
	}
}

func TestMissingImport(t *testing.T) {
	_, err := Load([]string{"testdata/missing"}, LoadOptions{})
	e := apperr.As(err)
	if e == nil || e.Code != apperr.CodeSchema || !strings.Contains(e.Message, "ex-base") || !strings.Contains(e.Message, "ex-aug") {
		t.Fatalf("err = %v", err)
	}
}

func TestFindPathWithKeysAndPrefix(t *testing.T) {
	tr := load(t)
	n := tr.FindPath("/ex-base:interface[name=ethernet-1/1]/statistics/in-octets")
	if n == nil || n.Name != "in-octets" {
		t.Fatalf("got %+v", n)
	}
}
