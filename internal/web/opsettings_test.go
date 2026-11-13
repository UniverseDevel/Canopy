package web

import (
	"testing"

	"github.com/UniverseDevel/Canopy/internal/app"
)

func TestOptsDirectOverridesJumpDefault(t *testing.T) {
	t.Setenv("CANOPY_CONFIG_DIR", t.TempDir())
	s := &Server{OpDefaults: app.OperatorOptions{Jumps: []string{"canopy@127.0.0.1:2222"}, IdentityFiles: []string{"lab.key"}}}

	// nothing saved: the defaults Canopy was started with
	if o := s.opts(app.OperatorOptions{}); len(o.Jumps) != 1 {
		t.Fatalf("default jumps = %v", o.Jumps)
	}
	// "connect directly": no jump, even though a default exists
	if err := writeOpSettings(opSettings{Direct: true}); err != nil {
		t.Fatal(err)
	}
	if o := s.opts(app.OperatorOptions{}); len(o.Jumps) != 0 || len(o.IdentityFiles) != 0 {
		t.Fatalf("direct: jumps = %v, keys = %v", o.Jumps, o.IdentityFiles)
	}
	// a jump given in the request still wins
	if o := s.opts(app.OperatorOptions{Jumps: []string{"me@bastion"}}); len(o.Jumps) != 1 || o.Jumps[0] != "me@bastion" {
		t.Fatalf("request jumps = %v", o.Jumps)
	}
	// saved jumps win over the default
	if err := writeOpSettings(opSettings{Jumps: []string{"me@work-jump"}}); err != nil {
		t.Fatal(err)
	}
	if o := s.opts(app.OperatorOptions{}); len(o.Jumps) != 1 || o.Jumps[0] != "me@work-jump" {
		t.Fatalf("saved jumps = %v", o.Jumps)
	}
}
