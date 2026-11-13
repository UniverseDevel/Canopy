package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/fixtures"
	"github.com/UniverseDevel/Canopy/internal/k8s"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/shadow"
	"github.com/UniverseDevel/Canopy/internal/snapshot"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FixtureRunOptions configure running a fixture against the live operator.
type FixtureRunOptions struct {
	Provider string `json:"provider"` // e.g. srlinux.nokia.networking.metal.ironcore.dev
	File     string `json:"file"`     // fixture file name in the provider folder
	Text     string `json:"text"`     // or the fixture text itself
	// DeviceAddress is the shadow target's address as the operator reaches it
	// (for kind: the docker network gateway, or host.docker.internal).
	DeviceAddress string `json:"deviceAddress"`
	// ShadowControl is the HTTP control URL of a shadow target you run yourself
	// ('canopy shadow --tls --http ...'). Empty: Canopy runs one on ShadowListen.
	ShadowControl string `json:"shadowControl,omitempty"`
	ShadowListen  string `json:"shadowListen,omitempty"` // default :57500
	TLS           bool   `json:"tls"`                    // embedded shadow serves TLS (the operator expects it)
	// Credentials the Device uses (a basic-auth Secret is created when set).
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"` // per wait, default 60
	Keep       bool   `json:"keep,omitempty"`       // keep the namespace for inspection
}

// FixtureStep is one step of a run.
type FixtureStep struct {
	Time   time.Time `json:"time"`
	Step   string    `json:"step"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail,omitempty"`
}

// FixtureRun is the result of running a fixture.
type FixtureRun struct {
	Fixture   string            `json:"fixture"`
	Title     string            `json:"title"`
	Namespace string            `json:"namespace"`
	Verdict   string            `json:"verdict"` // pass, fail, error
	Steps     []FixtureStep     `json:"steps"`
	Diff      []snapshot.Change `json:"diff,omitempty"` // first failing comparison
	ElapsedMs int64             `json:"elapsedMs"`
}

func (r *FixtureRun) step(ok bool, name, format string, a ...any) {
	r.Steps = append(r.Steps, FixtureStep{Time: time.Now(), Step: name, OK: ok, Detail: fmt.Sprintf(format, a...)})
}

// shadowCtl is the state of the target the operator talks to.
type shadowCtl interface {
	set(state any) error
	state() (any, error)
}

type embeddedShadow struct{ s *shadow.Server }

func (e embeddedShadow) set(v any) error {
	b, _ := json.Marshal(v)
	if v == nil {
		b = []byte("{}")
	}
	return e.s.Load(b)
}

func (e embeddedShadow) state() (any, error) {
	root := map[string]any{}
	for _, l := range e.s.Leaves() {
		live.MergePath(root, l.Path, l.Value)
	}
	return root, nil
}

type remoteShadow struct{ url string }

func (r remoteShadow) do(method string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, strings.TrimRight(r.url, "/")+"/v1/state", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("shadow control %s: %s %s", method, resp.Status, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func (r remoteShadow) set(v any) error {
	b, _ := json.Marshal(v)
	if v == nil {
		b = []byte("{}")
	}
	_, err := r.do(http.MethodPut, b)
	return err
}

func (r remoteShadow) state() (any, error) {
	b, err := r.do(http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	return v, d.Decode(&v)
}

// FixtureList lists a provider's fixtures in the operator checkout.
func (s *Service) FixtureList(o OperatorOptions, provider string) (string, []*fixtures.Fixture, error) {
	repo, err := fixtures.FindRepo(o.OperatorRepo)
	if err != nil {
		return "", nil, err
	}
	dir := fixtures.Dir(repo, provider)
	fs, err := fixtures.Load(dir)
	return dir, fs, err
}

// FixtureSave writes a fixture into the provider's folder of the operator
// checkout and returns the file path.
func (s *Service) FixtureSave(o OperatorOptions, provider, name string, text []byte) (string, error) {
	if !strings.HasSuffix(name, ".txtar") {
		name += ".txtar"
	}
	if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", apperr.New(apperr.ClassUsage, apperr.CodeUsage, "bad file name %q", name)
	}
	if _, err := fixtures.ParseBytes(name, text); err != nil {
		return "", err
	}
	repo, err := fixtures.FindRepo(o.OperatorRepo)
	if err != nil {
		return "", err
	}
	dir := fixtures.Dir(repo, provider)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no fixtures folder for provider %q (%s)", provider, dir)
	}
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, text, 0o644); err != nil {
		return "", apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	return file, nil
}

// RunFixture runs a fixture the way network-operator's gNMI test does, but
// against the operator running in the cluster: preload a shadow target, create a
// Device pointing at it and the fixture's resources in a new namespace, compare
// the target's state with state/expect, delete the resources, compare with
// state/delete, and clean up.
func (s *Service) RunFixture(ctx context.Context, o OperatorOptions, ro FixtureRunOptions) (*FixtureRun, error) {
	start := time.Now()
	if ro.Provider == "" || ro.DeviceAddress == "" {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "provider and device address are required")
	}
	var f *fixtures.Fixture
	var err error
	if strings.TrimSpace(ro.Text) != "" {
		f, err = fixtures.ParseBytes(ro.File, []byte(ro.Text))
	} else {
		repo, rerr := fixtures.FindRepo(o.OperatorRepo)
		if rerr != nil {
			return nil, rerr
		}
		if ro.File != filepath.Base(ro.File) {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "bad file name %q", ro.File)
		}
		f, err = fixtures.Parse(filepath.Join(fixtures.Dir(repo, ro.Provider), ro.File))
	}
	if err != nil {
		return nil, err
	}
	if f.Expect == nil || len(f.CRs) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "the fixture needs resources and state/expect")
	}
	cl, err := s.Operator(o)
	if err != nil {
		return nil, err
	}
	wait := time.Duration(ro.TimeoutSec) * time.Second
	if wait <= 0 {
		wait = 60 * time.Second
	}
	run := &FixtureRun{Fixture: f.File, Title: f.Title, Verdict: "error"}
	defer func() { run.ElapsedMs = time.Since(start).Milliseconds() }()

	// The target.
	var sh shadowCtl
	if ro.ShadowControl != "" {
		sh = remoteShadow{url: ro.ShadowControl}
		run.step(true, "shadow", "using the shadow target at %s (control %s)", ro.DeviceAddress, ro.ShadowControl)
	} else {
		srv := shadow.New()
		srv.Username, srv.Password = ro.Username, ro.Password
		if ro.TLS {
			tc, _, terr := shadow.SelfSigned([]string{"localhost", "127.0.0.1"})
			if terr != nil {
				return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, terr)
			}
			srv.TLS = tc
		}
		listen := ro.ShadowListen
		if listen == "" {
			listen = ":57500"
		}
		sctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		addr, serr := srv.Serve(sctx, listen)
		if serr != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, fmt.Errorf("shadow target on %s: %w", listen, serr))
		}
		sh = embeddedShadow{s: srv}
		run.step(true, "shadow", "shadow target listening on %s (TLS %v); the Device uses %s", addr, ro.TLS, ro.DeviceAddress)
	}
	if err := sh.set(f.Preload); err != nil {
		run.step(false, "preload", "%v", err)
		return run, nil
	}
	run.step(true, "preload", "state/preload loaded (%d leaves)", leafCount(f.Preload))

	// Namespace, credentials, Device.
	ns, err := cl.CreateNamespace(ctx, "canopy-fixture-", map[string]string{"canopy-fixture": "true"})
	if err != nil {
		run.step(false, "namespace", "%v", apperr.As(err).Error())
		return run, nil
	}
	run.Namespace = ns
	run.step(true, "namespace", "created %s", ns)
	var created []*unstructured.Unstructured
	defer func() {
		if ro.Keep {
			run.step(true, "cleanup", "kept namespace %s for inspection", ns)
			return
		}
		// Resources before the Device, so their finalizers can still reach it.
		// A resource the operator can't finalize (its provider failed) would
		// block the namespace: drop its finalizers, it's a throwaway namespace.
		var forced []string
		for i := len(created) - 1; i >= 0; i-- {
			u := created[i]
			cctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			cl.DeleteObject(cctx, u.GroupVersionKind(), u.GetNamespace(), u.GetName())
			if !waitGone(cctx, cl, u, 30*time.Second) {
				if err := cl.DropFinalizers(cctx, u.GroupVersionKind(), u.GetNamespace(), u.GetName()); err == nil {
					forced = append(forced, u.GetKind()+" "+u.GetName())
					waitGone(cctx, cl, u, 10*time.Second)
				}
			}
			cancel()
		}
		if len(forced) > 0 {
			run.step(false, "cleanup", "removed finalizers of %s (deletion was stuck)", strings.Join(forced, ", "))
		}
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cl.DeleteNamespace(cctx, ns); err != nil {
			run.step(false, "cleanup", "namespace %s: %v", ns, err)
			return
		}
		run.step(true, "cleanup", "deleted namespace %s", ns)
	}()

	devSpec := map[string]any{"provider": ro.Provider, "endpoint": map[string]any{"address": ro.DeviceAddress}}
	if ro.Username != "" {
		sec := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/basic-auth",
			"metadata":   map[string]any{"name": "fixture-basic-auth", "namespace": ns},
			"stringData": map[string]any{"username": ro.Username, "password": ro.Password},
		}}
		if _, err := cl.CreateObject(ctx, sec); err != nil {
			run.step(false, "device", "secret: %v", apperr.As(err).Error())
			return run, nil
		}
		devSpec["endpoint"].(map[string]any)["secretRef"] = map[string]any{"name": "fixture-basic-auth"}
	}
	dev := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.Group + "/v1alpha1", "kind": "Device",
		"metadata": map[string]any{"generateName": "test-device-", "namespace": ns},
		"spec":     devSpec,
	}}
	dev, err = cl.CreateObject(ctx, dev)
	if err != nil {
		run.step(false, "device", "%v", apperr.As(err).Error())
		return run, nil
	}
	created = append(created, dev)
	run.step(true, "device", "created Device %s -> %s", dev.GetName(), ro.DeviceAddress)
	// The operator's test marks the Device Running itself; give the controller a
	// moment first.
	phase := waitPhase(ctx, cl, dev, 15*time.Second)
	if phase != "Running" {
		if cur, _ := cl.GetObject(ctx, dev.GroupVersionKind(), ns, dev.GetName()); cur != nil {
			unstructured.SetNestedField(cur.Object, "Running", "status", "phase")
			if err := cl.UpdateStatus(ctx, cur); err != nil {
				run.step(false, "device", "phase %q; setting Running failed: %v", phase, err)
				return run, nil
			}
		}
		run.step(true, "device", "phase was %q; set status.phase=Running as the operator's test does", phase)
	} else {
		run.step(true, "device", "Device is Running")
	}

	// Resources, in the fixture's order.
	for _, cr := range f.CRs {
		u, cerr := toUnstructured(cr)
		if cerr != nil {
			run.step(false, "resource", "%v", cerr)
			return run, nil
		}
		u.SetNamespace(ns)
		u.SetResourceVersion("")
		if _, ok, _ := unstructured.NestedMap(u.Object, "spec", "deviceRef"); ok || u.GroupVersionKind().Group != "" {
			unstructured.SetNestedField(u.Object, dev.GetName(), "spec", "deviceRef", "name")
		}
		got, cerr := cl.CreateObject(ctx, u)
		if cerr != nil {
			run.step(false, "resource", "%s %s: %v", u.GetKind(), u.GetName(), apperr.As(cerr).Error())
			return run, nil
		}
		created = append(created, got)
		if msg, ok := waitReady(ctx, cl, got, wait); !ok {
			run.step(false, "resource", "%s %s: %s", got.GetKind(), got.GetName(), msg)
			return run, nil
		} else {
			run.step(true, "resource", "%s %s: %s", got.GetKind(), got.GetName(), msg)
		}
	}

	// Compare with state/expect.
	if ok, diff := waitState(ctx, sh, f.Expect, wait); !ok {
		run.Verdict, run.Diff = "fail", diff
		run.step(false, "expect", "the target's state doesn't match state/expect (%d differences)", len(diff))
		return run, nil
	}
	run.step(true, "expect", "state matches state/expect")

	// Delete and compare with state/delete.
	for i := len(created) - 1; i >= 1; i-- { // keep the Device
		u := created[i]
		if err := cl.DeleteObject(ctx, u.GroupVersionKind(), ns, u.GetName()); err != nil {
			run.step(false, "delete", "%s %s: %v", u.GetKind(), u.GetName(), err)
			return run, nil
		}
		if !waitGone(ctx, cl, u, wait) {
			run.step(false, "delete", "%s %s still exists after %s", u.GetKind(), u.GetName(), wait)
			return run, nil
		}
		run.step(true, "delete", "%s %s deleted", u.GetKind(), u.GetName())
	}
	created = created[:1]
	if f.Delete != nil {
		if ok, diff := waitState(ctx, sh, f.Delete, wait); !ok {
			run.Verdict, run.Diff = "fail", diff
			run.step(false, "delete-state", "the target's state doesn't match state/delete (%d differences)", len(diff))
			return run, nil
		}
		run.step(true, "delete-state", "state matches state/delete")
	}
	run.Verdict = "pass"
	return run, nil
}

func leafCount(v any) int {
	n := 0
	if v != nil {
		live.Flatten("/", v, func(string, any) { n++ })
	}
	return n
}

// toUnstructured converts YAML-decoded maps (with int values) to JSON types.
func toUnstructured(m map[string]any) (*unstructured.Unstructured, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(b); err != nil {
		return nil, err
	}
	return u, nil
}

func waitPhase(ctx context.Context, cl *k8s.Client, u *unstructured.Unstructured, d time.Duration) string {
	end := time.Now().Add(d)
	phase := ""
	for time.Now().Before(end) {
		if cur, _ := cl.GetObject(ctx, u.GroupVersionKind(), u.GetNamespace(), u.GetName()); cur != nil {
			phase, _, _ = unstructured.NestedString(cur.Object, "status", "phase")
			if phase == "Running" {
				return phase
			}
		}
		if sleep(ctx, 500*time.Millisecond) != nil {
			break
		}
	}
	return phase
}

// finalizerWindow is how long a resource may take to get a finalizer before
// it counts as config-only. The operator's own test waits 3 s, but there the
// controllers run in the test process; a real cluster's operator can be busy,
// restarting (Tilt) or waiting for its leader lease, so Canopy waits longer.
const finalizerWindow = 15 * time.Second

// waitReady waits like the operator's test: resources without a finalizer
// (config only) are done; others need Ready (or Configured) True. Status
// conditions count as a sign of a controller too.
func waitReady(ctx context.Context, cl *k8s.Client, u *unstructured.Unstructured, d time.Duration) (string, bool) {
	gvk, ns, name := u.GroupVersionKind(), u.GetNamespace(), u.GetName()
	window := finalizerWindow
	if d < window {
		window = d
	}
	fin := false
	for end := time.Now().Add(window); time.Now().Before(end); {
		if cur, _ := cl.GetObject(ctx, gvk, ns, name); cur != nil && (len(cur.GetFinalizers()) > 0 || len(k8s.ConditionsOf(cur)) > 0) {
			fin = true
			break
		}
		if sleep(ctx, 200*time.Millisecond) != nil {
			return "cancelled", false
		}
	}
	if !fin {
		return fmt.Sprintf("created; assumed config-only (no finalizer or status within %s, so no controller seems to reconcile it)", window), true
	}
	last := "no conditions yet"
	for end := time.Now().Add(d); time.Now().Before(end); {
		if cur, _ := cl.GetObject(ctx, gvk, ns, name); cur != nil {
			cs := k8s.ConditionsOf(cur)
			want := "Ready"
			for _, c := range cs {
				if c.Type == "Configured" {
					want = "Configured"
				}
			}
			for _, c := range cs {
				if c.Type == want {
					if c.Status == "True" {
						return want + "=True", true
					}
					last = fmt.Sprintf("%s=%s %s %s", want, c.Status, c.Reason, c.Message)
				}
			}
		}
		if sleep(ctx, 500*time.Millisecond) != nil {
			return "cancelled", false
		}
	}
	return "not ready after " + d.String() + ": " + last, false
}

func waitGone(ctx context.Context, cl *k8s.Client, u *unstructured.Unstructured, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); {
		cur, err := cl.GetObject(ctx, u.GroupVersionKind(), u.GetNamespace(), u.GetName())
		if cur == nil && err == nil {
			return true
		}
		if sleep(ctx, 500*time.Millisecond) != nil {
			return false
		}
	}
	return false
}

func waitState(ctx context.Context, sh shadowCtl, want any, d time.Duration) (bool, []snapshot.Change) {
	var diff []snapshot.Change
	for end := time.Now().Add(d); ; {
		got, err := sh.state()
		if err == nil {
			ok, df := fixtures.Match(want, got)
			if ok {
				return true, nil
			}
			diff = df
		}
		if time.Now().After(end) || sleep(ctx, time.Second) != nil {
			return false, diff
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

