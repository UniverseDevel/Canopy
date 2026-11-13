package models

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWebURL(t *testing.T) {
	m := &Manager{Dir: filepath.Join(t.TempDir(), "models"), gh: newGitHub(), locks: map[string]*sync.Mutex{}}
	m.subs = []subLink{{Repo: "YangModels/yang", Ref: "main", Path: "vendor/juniper", Target: "Juniper/yang", Commit: "abc123"}}
	os.MkdirAll(m.setDir("junos", "23.4/23.4R2"), 0o755)
	os.WriteFile(filepath.Join(m.setDir("junos", "23.4/23.4R2"), ".version"), []byte("23.4/23.4R2"), 0o644)
	cases := map[string]string{
		filepath.Join(m.Dir, "srlinux", "v26.7.1", "srlinux-yang-models", "srl_nokia", "models", "system", "srl_nokia-system.yang"):
			"https://github.com/nokia/srlinux-yang-models/blob/v26.7.1/srlinux-yang-models/srl_nokia/models/system/srl_nokia-system.yang#L87",
		filepath.Join(m.Dir, "nxos", "10.4-3", "Cisco-NX-OS-device.yang"):
			"https://github.com/YangModels/yang/blob/main/vendor/cisco/nx/10.4-3/Cisco-NX-OS-device.yang#L87",
		filepath.Join(m.setDir("junos", "23.4/23.4R2"), "native", "conf-and-rpcs", "junos", "conf", "junos-conf-system@2023-01-01.yang"):
			"https://github.com/Juniper/yang/blob/abc123/23.4/23.4R2/native/conf-and-rpcs/junos/conf/junos-conf-system@2023-01-01.yang#L87",
		filepath.Join(m.Dir, "_shared", "YangModels_yang", "standard", "ietf", "RFC", "ietf-interfaces@2018-02-20.yang"):
			"https://github.com/YangModels/yang/blob/main/standard/ietf/RFC/ietf-interfaces@2018-02-20.yang#L87",
		filepath.Join(m.Dir, "eos", "EOS-4.31.0F", "release", "openconfig", "models", "x.yang"):
			"https://github.com/aristanetworks/yang/blob/master/EOS-4.31.0F/release/openconfig/models/x.yang#L87",
	}
	for f, want := range cases {
		if got, ok := m.WebURL(f, 87); !ok || got != want {
			t.Errorf("%s\n got  %s\n want %s", f, got, want)
		}
	}
	for _, f := range []string{filepath.Join(m.Dir, "_device", "10.0.0.1_830", "a.yang"), filepath.Join(t.TempDir(), "local", "a.yang")} {
		if u, ok := m.WebURL(f, 1); ok {
			t.Errorf("%s: %s", f, u)
		}
	}
}
