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
	want := `gnmic -a 127.0.0.1:57400 -u "$GNMI_USERNAME" -p "$GNMI_PASSWORD" --skip-verify -e json_ietf get --type state --path /system/information --path '/interface[name=ethernet-1/1]/admin-state'`
	if cmd != want {
		t.Errorf("got  %s\nwant %s", cmd, want)
	}
	if strings.Contains(cmd, "secret") || strings.Contains(cmd, "\n") {
		t.Error("password leaked or not one line")
	}
}

func TestThroughJumpBash(t *testing.T) {
	out, err := Gnmic(Request{
		Target: conn.Options{Address: "10.0.0.5:9339", SkipVerify: true},
		Hops:   []sshx.Hop{{User: "a", Host: "j1", Alias: "j1", Port: 22}, {User: "b", Host: "j2.example.net", Alias: "j2", Port: 2222}},
		Paths:  []string{"/interface[name=e1]"}, Mode: "stream",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `ssh -J a@j1 -p 2222 -t b@j2 "gnmic -a 10.0.0.5:9339 -u '$GNMI_USERNAME' -p '$GNMI_PASSWORD' --skip-verify subscribe --path '/interface[name=e1]' --mode stream --stream-mode sample --sample-interval 10s"`
	if got := out.Commands[0].Text; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSetThroughJumpEscapes(t *testing.T) {
	out, err := GnmicSet(Request{Target: conn.Options{Address: "10.0.0.5:57400", Insecure: true}, Hops: []sshx.Hop{{Host: "j"}}},
		[]SetOp{{Op: "update", Path: "/a", Value: `{"x":"$y"}`}})
	if err != nil {
		t.Fatal(err)
	}
	want := `ssh j "gnmic -a 10.0.0.5:57400 -u '$GNMI_USERNAME' -p '$GNMI_PASSWORD' --insecure -e json_ietf set --update-path /a --update-value '{\"x\":\"\$y\"}'"`
	if got := out.Commands[0].Text; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestThroughJumpPowerShell(t *testing.T) {
	out, err := Gnmic(Request{
		Target: conn.Options{Address: "10.0.0.5:9339", CAFile: "ca.pem"},
		Hops:   []sshx.Hop{{User: "b", Host: "j2"}},
		Paths:  []string{"/x"}, Shell: "powershell", Mode: "capabilities",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `ssh b@j2 ('gnmic -a 10.0.0.5:9339 -u ''' + $env:GNMI_USERNAME + ''' -p ''' + $env:GNMI_PASSWORD + ''' --tls-ca ca.pem capabilities')`
	if got := out.Commands[0].Text; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !strings.Contains(out.Commands[0].Note, "TLS files") {
		t.Errorf("note: %s", out.Commands[0].Note)
	}
}

func TestSetCommand(t *testing.T) {
	out, err := GnmicSet(Request{Target: conn.Options{Address: "10.0.0.5:57400", SkipVerify: true}},
		[]SetOp{{Op: "update", Path: "/system/banner/login-banner", Value: `"it's me"`}, {Op: "delete", Path: "/interface[name=e1]"}})
	if err != nil {
		t.Fatal(err)
	}
	c := out.Commands[0].Text
	for _, want := range []string{"-e json_ietf set", `--update-path /system/banner/login-banner --update-value '"it'\''s me"'`, "--delete '/interface[name=e1]'"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %q", want, c)
		}
	}
}

func TestMixedOriginsSplit(t *testing.T) {
	out, err := Gnmic(Request{Target: conn.Options{Address: "h:1", Insecure: true}, Paths: []string{"/a", "openconfig:/b", "/c"}})
	if err != nil {
		t.Fatal(err)
	}
	c := out.Commands[0].Text
	if strings.Count(c, "gnmic ") != 2 || !strings.Contains(c, "--path /a --path /c && gnmic") || !strings.Contains(c, "--path openconfig:/b") {
		t.Errorf("got %s", c)
	}
}

// An operator Device's CA comes from a Secret, in memory: the command needs a
// file for it, named as a placeholder and explained in the note.
func TestInMemoryCA(t *testing.T) {
	out, err := Gnmic(Request{Target: conn.Options{Address: "10.0.0.1:57400", CAPEM: []byte("pem")}, Paths: []string{"/system"}})
	if err != nil {
		t.Fatal(err)
	}
	c := out.Commands[0]
	if !strings.Contains(c.Text, "--tls-ca ca.pem") || !strings.Contains(c.Note, "ca.pem") {
		t.Errorf("text %q note %q", c.Text, c.Note)
	}
}
