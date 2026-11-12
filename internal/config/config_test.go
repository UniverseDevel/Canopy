package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/UniverseDevel/Canopy/internal/schema"
)

func tree(t *testing.T) *schema.Tree {
	t.Helper()
	tr, err := schema.Load([]string{"../schema/testdata/ok"}, schema.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestBuild(t *testing.T) {
	req, err := Build(Request{Ops: []Op{
		{Op: "update", Path: "/system/hostname", Value: json.RawMessage(`"leaf1"`)},
		{Op: "replace", Path: "/interface[name=e1]", Value: json.RawMessage(`{"mtu":9000}`)},
		{Op: "delete", Path: "/interface[name=e2]"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Update) != 1 || len(req.Replace) != 1 || len(req.Delete) != 1 {
		t.Fatalf("req = %v", req)
	}
	p := Preview(req)
	if !strings.Contains(p, `"value": "leaf1"`) || !strings.Contains(p, `"/interface[name=e2]"`) {
		t.Errorf("preview:\n%s", p)
	}
	for _, bad := range []Request{{}, {Ops: []Op{{Op: "update", Path: "/a"}}}, {Ops: []Op{{Op: "update", Path: "/a[k=*]", Value: json.RawMessage(`1`)}}},
		{Ops: []Op{{Op: "merge", Path: "/a", Value: json.RawMessage(`1`)}}}, {Ops: []Op{{Op: "update", Path: "/a", Value: json.RawMessage(`{x`)}}}} {
		if _, err := Build(bad); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
	if !Destructive(Request{Ops: []Op{{Op: "delete", Path: "/a"}}}) || Destructive(Request{Ops: []Op{{Op: "update", Path: "/a"}}}) {
		t.Error("destructive")
	}
}

func TestValidate(t *testing.T) {
	tr := tree(t)
	ifc := tr.FindPath("/interface")
	cases := map[string]int{
		`[{"name":"e1","mtu":9000,"admin-state":"enable"}]`: 0,
		`[{"name":"e1","mtu":100}]`:                         1, // below range
		`[{"mtu":9000}]`:                                    1, // missing key
		`[{"name":"e1","admin-state":"up"}]`:                1, // bad enum
		`[{"name":"e1","bogus":1}]`:                         1, // unknown member
		`[{"name":"e1","statistics":{}}]`:                   1, // state
		`{"name":"e1"}`:                                     0, // single entry
	}
	for in, want := range cases {
		if p := Validate(tr, ifc.ID, json.RawMessage(in)); len(p) != want {
			t.Errorf("%s: got %v, want %d problems", in, p, want)
		}
	}
	host := tr.FindPath("/system/hostname")
	if p := Validate(tr, host.ID, json.RawMessage(`""`)); len(p) != 1 {
		t.Errorf("length: %v", p)
	}
}

func TestSkeleton(t *testing.T) {
	tr := tree(t)
	b, _ := json.Marshal(Skeleton(tr, tr.FindPath("/interface").ID))
	s := string(b)
	for _, want := range []string{`"name":""`, `"admin-state":"enable"`, `"mtu":1500`} {
		if !strings.Contains(s, want) {
			t.Errorf("skeleton %s lacks %s", s, want)
		}
	}
	if strings.Contains(s, "statistics") {
		t.Errorf("skeleton has state: %s", s)
	}
}
