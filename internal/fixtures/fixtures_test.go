package fixtures

import (
	"os"
	"path/filepath"
	"testing"
)

const banner = `# Banner PreLogin
-- banners/banner --
apiVersion: networking.metal.ironcore.dev/v1alpha1
kind: Banner
metadata:
  name: banner
spec:
  deviceRef:
    name: device
  type: PreLogin
  message:
    inline: "Unauthorized access is prohibited."
-- state/preload --
{"srl_nokia-system:system": {"srl_nokia-system-info:information": {"version": "v24.3.1"}}}
-- state/expect --
{"srl_nokia-system:system": {"srl_nokia-system-info:information": {"version": "v24.3.1"},
 "srl_nokia-system-banner:banner": {"login-banner": "Unauthorized access is prohibited."}}}
-- state/delete --
{"srl_nokia-system:system": {"srl_nokia-system-info:information": {"version": "v24.3.1"}, "srl_nokia-system-banner:banner": {}}}
`

const iface = `# Interface
-- interfaces/eth --
kind: Interface
spec:
  deviceRef: {name: device}
  name: ethernet-1/1
  description: uplink
  adminState: Up
-- state/expect --
{"interface": [{"name": "ethernet-1/1", "description": "uplink", "admin-state": "enable"}]}
`

func write(t *testing.T, name, s string) string {
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte(s), 0o644)
	return dir
}

func TestBannerExpected(t *testing.T) {
	fs, err := Load(write(t, "banner.txtar", banner))
	if err != nil {
		t.Fatal(err)
	}
	f := fs[0]
	if f.Title != "Banner PreLogin" || f.Kinds()[0] != "Banner" {
		t.Fatalf("parsed %+v", f)
	}
	leaves, gone, err := Expected(f, "Banner", map[string]any{"type": "PreLogin", "message": map[string]any{"inline": "Welcome to the lab"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 1 || leaves[0].Path != "/srl_nokia-system:system/srl_nokia-system-banner:banner/login-banner" || leaves[0].Value != "Welcome to the lab" || leaves[0].From != "message.inline" {
		t.Errorf("leaves %+v", leaves)
	}
	if leaves[0].FixturePath != "/srl_nokia-system:system/srl_nokia-system-banner:banner/login-banner" || leaves[0].FixtureValue == "Welcome to the lab" {
		t.Errorf("fixture side %+v", leaves[0])
	}
	if len(gone) != 1 || gone[0].Path != "/srl_nokia-system:system/srl_nokia-system-banner:banner/login-banner" {
		t.Errorf("gone %v", gone)
	}
}

func TestKeyedPathMapping(t *testing.T) {
	fs, _ := Load(write(t, "interface.txtar", iface))
	leaves, _, err := Expected(fs[0], "Interface", map[string]any{"name": "ethernet-1/7", "description": "to spine", "adminState": "Up"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, l := range leaves {
		got[l.Path] = l.Value
	}
	if got["/interface[name=ethernet-1/7]/description"] != "to spine" || got["/interface[name=ethernet-1/7]/admin-state"] != "enable" {
		t.Errorf("leaves %+v", leaves)
	}
}

func TestExportRoundTrip(t *testing.T) {
	cr := []byte("apiVersion: networking.metal.ironcore.dev/v1alpha1\nkind: Banner\nmetadata:\n  name: motd\nspec:\n  deviceRef: {name: device}\n  message: {inline: hi}\n")
	b, err := Export("Banner from lab", cr, map[string]any{"a": 1}, map[string]any{"a": 1, "b": "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := write(t, "x.txtar", string(b))
	fs, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := fs[0]
	if f.Title != "Banner from lab" || f.CR("Banner") == nil || f.Expect == nil || f.Delete != nil {
		t.Errorf("round trip %+v\n%s", f, b)
	}
	applied, _ := f.Touched()
	if len(applied) != 1 || applied[0].Path != "/b" {
		t.Errorf("touched %+v", applied)
	}
}

func TestMatchUnorderedAndEmpty(t *testing.T) {
	want := map[string]any{"a": map[string]any{"b": []any{"x", "y"}}, "empty": map[string]any{}}
	got := map[string]any{"a": map[string]any{"b": []any{"y", "x"}}}
	if ok, d := Match(want, got); !ok {
		t.Errorf("should match: %+v", d)
	}
	if ok, d := Match(want, map[string]any{"a": map[string]any{"b": []any{"x"}}}); ok || len(d) == 0 {
		t.Errorf("should differ: %v %+v", ok, d)
	}
}

func TestFindRepoExplicit(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindRepo(dir); err == nil {
		t.Error("a folder without test/gnmi/testdata should not be accepted")
	}
	os.MkdirAll(filepath.Join(dir, "test", "gnmi", "testdata"), 0o755)
	if got, err := FindRepo(dir); err != nil || got != dir {
		t.Errorf("FindRepo = %q, %v", got, err)
	}
}

func TestRepoChoice(t *testing.T) {
	t.Setenv("CANOPY_CONFIG_DIR", t.TempDir())
	fork, public := t.TempDir(), t.TempDir()
	for _, d := range []string{fork, public} {
		os.MkdirAll(filepath.Join(d, "test", "gnmi", "testdata"), 0o755)
	}
	if _, err := Choose(t.TempDir()); err == nil {
		t.Error("a folder that isn't a checkout should be refused")
	}
	Choose(public)
	c, _ := Choose(fork)
	if c.Current != fork || len(c.Recent) != 2 || c.Recent[0] != fork {
		t.Errorf("choice %+v", c)
	}
	if got, err := FindRepo(""); err != nil || got != fork {
		t.Errorf("FindRepo = %q, %v", got, err)
	}
	if got, _ := FindRepo(public); got != public {
		t.Errorf("an explicit folder should win: %q", got)
	}
	c, _ = Forget(fork)
	if c.Current != "" || len(c.Recent) != 1 {
		t.Errorf("after forget %+v", c)
	}
}

// The operator sends paths with their module prefixes, and devices may pick
// the model by them (SR Linux: "openconfig-system:system" is OpenConfig,
// "system" is native), so Verify must read the same paths.
func TestTouchedKeepsModulePrefixes(t *testing.T) {
	f, err := ParseBytes("banner.txtar", []byte(`# Banner
-- banners/banner --
kind: Banner
metadata: {name: banner}
spec: {message: {inline: "hi"}}
-- state/preload --
{}
-- state/expect --
{"openconfig-system:system": {"config": {"login-banner": "hi"}}}
-- state/delete --
{"openconfig-system:system": {"config": {}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	applied, removed := f.Touched()
	want := "/openconfig-system:system/config/login-banner"
	if len(applied) != 1 || applied[0].Path != want {
		t.Errorf("applied %+v", applied)
	}
	if len(removed) != 1 || removed[0] != want {
		t.Errorf("removed %v", removed)
	}
}
