package snapshot

import (
	"testing"
	"time"

	"github.com/UniverseDevel/Canopy/internal/live"
)

func TestStoreAndDiff(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	a := &Snapshot{Meta: Meta{Name: "dev-1", Device: "dev", Time: time.Unix(100, 0)}, Flat: []live.Update{
		{Path: "/srl:system/name/host-name", Value: "a"},
		{Path: "/srl:system/banner/login-banner", Value: "hi"},
		{Path: "/srl:interface[name=e1/1]/statistics/in-octets", Value: "5"},
	}}
	b := &Snapshot{Meta: Meta{Name: "dev-2", Device: "dev", Time: time.Unix(200, 0)}, Flat: []live.Update{
		{Path: "/system/name/host-name", Value: "b"},
		{Path: "/system/ntp/admin-state", Value: "enable"},
		{Path: "/interface[name=e1/1]/statistics/in-octets", Value: "9"},
	}}
	for _, sn := range []*Snapshot{a, b} {
		if err := s.Save(sn); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(a); err == nil {
		t.Error("duplicate name accepted")
	}
	l, _ := s.List()
	if len(l) != 2 || l[0].Name != "dev-2" || l[0].Leaves != 3 {
		t.Errorf("list %+v", l)
	}
	a2, err := s.Load("dev-1")
	if err != nil || len(a2.Flat) != 3 {
		t.Fatalf("load %v %v", a2, err)
	}
	d := Diff(a2.Flat, b.Flat, []string{"statistics"})
	if d.Added != 1 || d.Removed != 1 || d.Changed != 1 || d.Unchanged != 0 {
		t.Errorf("diff %+v", d)
	}
	if d.Changes[0].Path != "/system/banner/login-banner" || d.Changes[0].Kind != "removed" {
		t.Errorf("first change %+v", d.Changes[0])
	}
	if err := s.Delete("dev-1"); err != nil {
		t.Error(err)
	}
}

func TestStripPrefixes(t *testing.T) {
	if got := stripPrefixes("/a:b[k=x:y/z]/c:d"); got != "/b[k=x:y/z]/d" {
		t.Errorf("got %s", got)
	}
}
