package proxy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/shadow"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

func TestProxyForwardsAndRecords(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sh := shadow.New()
	sh.Username, sh.Password = "admin", "pw"
	sh.Load([]byte(`{"system":{"hostname":"a"}}`))
	up, err := sh.Serve(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Options{Listen: "127.0.0.1:0", Upstream: up})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := p.Serve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A normal gNMI client through the proxy: credentials pass through.
	c, caps, err := conn.Dial(ctx, conn.Options{Address: addr, Insecure: true, Username: "admin", Password: "pw", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer c.Close()
	if caps.GetGNMIVersion() == "" {
		t.Error("no capabilities")
	}
	_, err = c.Client.Set(ctx, &gpb.SetRequest{Update: []*gpb.Update{{
		Path: &gpb.Path{Elem: []*gpb.PathElem{{Name: "system"}, {Name: "hostname"}}},
		Val:  &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`"b"`)}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	sets := p.Records("/Set")
	if len(sets) != 2 || sets[0].Direction != "request" || !strings.Contains(string(sets[0].Message), "hostname") {
		t.Fatalf("set records %+v", sets)
	}
	var m map[string]any
	if err := json.Unmarshal(sets[0].Message, &m); err != nil || m["update"] == nil {
		t.Errorf("decoded %s", sets[0].Message)
	}
	if got := sh.Leaves(); got[0].Value != "b" {
		t.Errorf("upstream not changed: %+v", got)
	}
	// Wrong password: the upstream's error comes back through the proxy.
	_, _, err = conn.Dial(ctx, conn.Options{Address: addr, Insecure: true, Username: "admin", Password: "no", Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "invalid username") {
		t.Errorf("auth error through proxy: %v", err)
	}
}
