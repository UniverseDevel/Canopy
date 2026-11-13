package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/shadow"
)

func screenText(s tcell.SimulationScreen) string {
	cells, w, _ := s.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		} else {
			b.WriteByte(' ')
		}
		if (i+1)%w == 0 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func waitFor(t *testing.T, s tcell.SimulationScreen, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(screenText(s), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", want, screenText(s))
}

func TestTUI(t *testing.T) {
	t.Setenv("CANOPY_CONFIG_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sh := shadow.New()
	sh.Username, sh.Password = "admin", "pw"
	if err := sh.Load([]byte(`{"system":{"hostname":"leaf-one","information":{"version":"1.0"}}}`)); err != nil {
		t.Fatal(err)
	}
	addr, err := sh.Serve(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.New(nil)
	defer svc.Close()
	sess, err := svc.Connect(ctx, app.Target{Address: addr, Insecure: true, Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoadSchema([]string{"../schema/testdata/ok"}, schema.LoadOptions{}); err != nil {
		t.Fatal(err)
	}
	sim := tcell.NewSimulationScreen("UTF-8")
	sim.Init()
	sim.SetSize(160, 40)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, svc, Options{Session: sess, Models: "test", Shell: "bash", Qualified: false, Screen: sim}) }()

	waitFor(t, sim, "system")
	key := func(r rune) { sim.InjectKey(tcell.KeyRune, r, tcell.ModNone) }
	typeText := func(s string) {
		for _, r := range s {
			key(r)
		}
	}
	// search, go to the node, mark it, read it
	key('/')
	waitFor(t, sim, "Find a node")
	typeText("hostname")
	waitFor(t, sim, "/system/hostname")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // into the results
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // go there
	waitFor(t, sim, "Host name.")
	key(' ')
	waitFor(t, sim, "1 marked")
	waitFor(t, sim, "gnmic -a "+addr)
	key('G')
	waitFor(t, sim, "leaf-one")
	key('c')
	waitFor(t, sim, "Capabilities")
	key('q')
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("q did not quit")
	}
}
