package generate

import (
	"strings"
	"testing"

	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/sshx"
)

func TestGetCommand(t *testing.T) {
	out, err := Gnmic(Request{
		Target: conn.Options{Address: "127.0.0.1:57400", SkipVerify: true, Password: "secret"},
		Paths:  []string{"/system/information", "/interface[name=ethernet-1/1]/admin-state"},
		Type:   "state", Encoding: "json_ietf",
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := out.Commands[0].Text
	want := `gnmic -a 127.0.0.1:57400 -u "$GNMI_USERNAME" -p "$GNMI_PASSWORD" --skip-verify -e json_ietf get --type state`
	if !strings.HasPrefix(strings.ReplaceAll(cmd, " \\\n  ", " "), want) {
		t.Errorf("got %s", cmd)
	}
	if !strings.Contains(cmd, `'/interface[name=ethernet-1/1]/admin-state'`) {
		t.Errorf("path not quoted: %s", cmd)
	}
	if strings.Contains(cmd+out.Config, "secret") {
		t.Error("password leaked")
	}
}

func TestStreamThroughJump(t *testing.T) {
	out, err := Gnmic(Request{
		Target: conn.Options{Address: "10.0.0.5:9339", CAFile: "ca.pem"},
		Hops:   []sshx.Hop{{User: "a", Host: "j1", Alias: "j1", Port: 22}, {User: "b", Host: "j2.example.net", Alias: "j2", Port: 2222}},
		Paths:  []string{"/interfaces"}, Mode: "stream", Shell: "powershell",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Commands[0].Text != "ssh -N -L 9339:10.0.0.5:9339 -J a@j1 -p 2222 b@j2" {
		t.Errorf("tunnel: %s", out.Commands[0].Text)
	}
	c := strings.ReplaceAll(out.Commands[1].Text, " `\n  ", " ")
	for _, want := range []string{"-a 127.0.0.1:9339", "$env:GNMI_PASSWORD", "--tls-server-name 10.0.0.5", "--mode stream --stream-mode sample --sample-interval 10s"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %s", want, c)
		}
	}
}

func TestSetCommand(t *testing.T) {
	out, err := GnmicSet(Request{Target: conn.Options{Address: "10.0.0.5:57400", SkipVerify: true}},
		[]SetOp{{Op: "update", Path: "/system/banner/login-banner", Value: `"it's me"`}, {Op: "delete", Path: "/interface[name=e1]"}})
	if err != nil {
		t.Fatal(err)
	}
	c := strings.ReplaceAll(out.Commands[0].Text, " \\\n  ", " ")
	for _, want := range []string{"-e json_ietf set", `--update-path /system/banner/login-banner --update-value '"it'\''s me"'`, "--delete '/interface[name=e1]'"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %q", want, c)
		}
	}
}
