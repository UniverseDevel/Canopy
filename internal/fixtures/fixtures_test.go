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
	if len(leaves) != 1 || leaves[0].Path != "/system/banner/login-banner" || leaves[0].Value != "Welcome to the lab" || leaves[0].From != "message.inline" {
		t.Errorf("leaves %+v", leaves)
	}
	if len(gone) != 1 || gone[0] != "/system/banner/login-banner" {
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
