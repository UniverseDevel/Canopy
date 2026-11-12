package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/config"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/shadow"
)

func setup(t *testing.T) (*Service, *Session, *shadow.Server) {
	t.Helper()
	t.Setenv("CANOPY_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sh := shadow.New()
	sh.Username, sh.Password = "admin", "pw"
	if err := sh.Load([]byte(`{"system":{"hostname":"old","information":{"version":"1.0"}},"interface":[{"name":"e1","mtu":1500}]}`)); err != nil {
		t.Fatal(err)
	}
	addr, err := sh.Serve(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(nil)
	t.Cleanup(svc.Close)
	sess, err := svc.Connect(ctx, Target{Address: addr, Insecure: true, Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoadSchema([]string{"../schema/testdata/ok"}, schema.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	return svc, sess, sh
}

func TestSetIsGuarded(t *testing.T) {
	svc, sess, sh := setup(t)
	req := config.Request{Ops: []config.Op{{Op: "update", Path: "/system/hostname", Value: json.RawMessage(`"leaf1"`)}}}

	// Dry run: preview, validation, gnmic command; nothing sent.
	out, err := svc.Set(context.Background(), sess.ID, req, SetOptions{DryRun: true})
	if err != nil || out.Sent || len(out.Problems) != 0 || len(out.Gnmic) == 0 {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	// Without confirmation: refused.
	_, err = svc.Set(context.Background(), sess.ID, req, SetOptions{})
	if ae := apperr.As(err); ae == nil || ae.Code != "confirmation_required" {
		t.Fatalf("unconfirmed: %v", err)
	}
	if len(sh.Sets()) != 0 {
		t.Fatal("something was sent without confirmation")
	}
	// Confirmed: applied, with a before/after diff.
	out, err = svc.Set(context.Background(), sess.ID, req, SetOptions{Confirm: true})
	if err != nil || !out.Sent || out.Diff == nil || out.Diff.Changed != 1 {
		t.Fatalf("confirmed: %+v %v", out, err)
	}
	res, _ := svc.Get(context.Background(), sess.ID, live.GetRequest{Paths: []string{"/system/hostname"}})
	if len(res.Flat) != 1 || res.Flat[0].Value != "leaf1" {
		t.Errorf("after set: %+v", res.Flat)
	}
}

func TestSetValidation(t *testing.T) {
	svc, sess, _ := setup(t)
	out, err := svc.Set(context.Background(), sess.ID, config.Request{Ops: []config.Op{
		{Op: "update", Path: "/interface[name=e1]", Value: json.RawMessage(`{"name":"e1","mtu":100}`)},
	}}, SetOptions{DryRun: true})
	if err != nil || len(out.Problems) != 1 {
		t.Fatalf("problems %v %v", out.Problems, err)
	}
}

func TestSnapshots(t *testing.T) {
	svc, sess, sh := setup(t)
	ctx := context.Background()
	m, err := svc.TakeSnapshot(ctx, sess.ID, live.GetRequest{Paths: []string{"/system"}}, "", "")
	if err != nil || m.Leaves != 2 {
		t.Fatalf("snapshot %+v %v", m, err)
	}
	sh.Load([]byte(`{"system":{"hostname":"new","information":{"version":"1.0"}}}`))
	d, err := svc.CompareSnapshot(ctx, m.Name, "", sess.ID, nil)
	if err != nil || d.Changed != 1 || d.Changes[0].Path != "/system/hostname" {
		t.Fatalf("compare %+v %v", d, err)
	}
	list, _ := svc.Snapshots().List()
	if len(list) != 1 {
		t.Errorf("list %v", list)
	}
}
