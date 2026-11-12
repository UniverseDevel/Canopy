package live

import (
	"encoding/json"
	"testing"

	"github.com/UniverseDevel/Canopy/internal/gpath"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

func mustPath(t *testing.T, s string) *gpb.Path {
	t.Helper()
	p, err := gpath.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConvertMergesKeyedUpdates(t *testing.T) {
	ns := []*gpb.Notification{{
		Timestamp: 1,
		Update: []*gpb.Update{
			{Path: mustPath(t, "/interface[name=ethernet-1/1]"), Val: &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`{"admin-state":"enable","statistics":{"in-octets":"18446744073709551615"}}`)}}},
			{Path: mustPath(t, "/interface[name=mgmt0]/admin-state"), Val: &gpb.TypedValue{Value: &gpb.TypedValue_StringVal{StringVal: "enable"}}},
			{Path: mustPath(t, "/system/information/version"), Val: &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`"v26.7.1"`)}}},
		},
	}}
	res, err := Convert(ns)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Data)
	want := `{"interface":[{"admin-state":"enable","name":"ethernet-1/1","statistics":{"in-octets":"18446744073709551615"}},{"admin-state":"enable","name":"mgmt0"}],"system":{"information":{"version":"v26.7.1"}}}`
	if string(b) != want {
		t.Errorf("data:\n got %s\nwant %s", b, want)
	}
	if res.LeafCount != 4 || res.Empty {
		t.Errorf("leafCount %d empty %v", res.LeafCount, res.Empty)
	}
	if res.Flat[0].Path != "/interface[name=ethernet-1/1]/admin-state" {
		t.Errorf("flat[0] = %s", res.Flat[0].Path)
	}
}

func TestFlattenGuessesKeys(t *testing.T) {
	var got []string
	v := map[string]any{"neighbor": []any{map[string]any{"peer-address": "192.0.2.1", "id": "x", "state": "up"}}}
	Flatten("/bgp", v, func(p string, _ any) { got = append(got, p) })
	if len(got) != 2 || got[0] != "/bgp/neighbor[id=x]/peer-address" {
		t.Errorf("got %v", got)
	}
}

func TestDecimal(t *testing.T) {
	cases := map[[2]int64]string{{12345, 2}: "123.45", {-5, 3}: "-0.005", {7, 0}: "7"}
	for in, want := range cases {
		if got := formatDecimal(in[0], uint32(in[1])); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
}

func TestBuildGetRequestErrors(t *testing.T) {
	if _, err := BuildGetRequest(GetRequest{}); err == nil {
		t.Error("expected error for no paths")
	}
	if _, err := BuildGetRequest(GetRequest{Paths: []string{"/a"}, Type: "bogus"}); err == nil {
		t.Error("expected error for bad type")
	}
	r, err := BuildGetRequest(GetRequest{Paths: []string{"/a"}, Type: "state", Encoding: "json_ietf"})
	if err != nil || r.Type != gpb.GetRequest_STATE || r.Encoding != gpb.Encoding_JSON_IETF {
		t.Errorf("got %v %v", r, err)
	}
}
