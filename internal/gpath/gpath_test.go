package gpath

import "testing"

func TestRoundTrip(t *testing.T) {
	cases := map[string]string{
		"/":                              "/",
		"/system/information/version":    "/system/information/version",
		"interface[name=ethernet-1/1]/x": "/interface[name=ethernet-1/1]/x",
		"/a[k2=2][k1=1]/b":               "/a[k1=1][k2=2]/b",
		"openconfig:/interfaces":         "openconfig:/interfaces",
		"/oc-if:interfaces/interface":    "/oc-if:interfaces/interface",
		`/a[k=x\]y]`:                     `/a[k=x\]y]`,
		"/interface[name=*]/state":       "/interface[name=*]/state",
	}
	for in, want := range cases {
		p, err := Parse(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := String(p); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

func TestOrigin(t *testing.T) {
	p, _ := Parse("rfc7951:/a/b")
	if p.Origin != "rfc7951" || len(p.Elem) != 2 {
		t.Fatalf("got %v", p)
	}
	p, _ = Parse("/oc-if:interfaces")
	if p.Origin != "" || p.Elem[0].Name != "oc-if:interfaces" {
		t.Fatalf("got %v", p)
	}
}

func TestErrors(t *testing.T) {
	for _, in := range []string{"/a[k=1", "/a]", "/a[k]"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("%s: expected error", in)
		}
	}
}
