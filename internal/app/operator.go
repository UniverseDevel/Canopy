package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/fixtures"
	"github.com/UniverseDevel/Canopy/internal/k8s"
	"github.com/UniverseDevel/Canopy/internal/live"
)

// OperatorOptions select the cluster and how Canopy reaches the devices the
// operator manages (the devices' addresses are often only reachable from the
// cluster network, so jump hosts can be given).
type OperatorOptions struct {
	k8s.Options
	Jumps         []string `json:"jumps,omitempty"`
	IdentityFiles []string `json:"identityFiles,omitempty"`
	TimeoutSec    int      `json:"timeoutSec,omitempty"`
	OperatorRepo  string   `json:"operatorRepo,omitempty"` // for fixtures
}

type opState struct {
	mu  sync.Mutex
	cl  *k8s.Client
	key string
}

var op opState

// Operator returns a client for the cluster (cached per kubeconfig and context).
func (s *Service) Operator(o OperatorOptions) (*k8s.Client, error) {
	key := o.Kubeconfig + "\x00" + o.Context
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.cl != nil && op.key == key {
		return op.cl, nil
	}
	cl, err := k8s.New(o.Options)
	if err != nil {
		return nil, err
	}
	op.cl, op.key = cl, key
	return cl, nil
}

// ConnectDevice connects to a Device the way the operator does: its address,
// basic-auth Secret and TLS Secrets, plus the given jump hosts.
func (s *Service) ConnectDevice(ctx context.Context, o OperatorOptions, ns, name string, extra Target) (*Session, error) {
	cl, err := s.Operator(o)
	if err != nil {
		return nil, err
	}
	cr, err := cl.DeviceCredentials(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	t := extra
	t.Address, t.Username, t.Password = cr.Address, cr.Username, cr.Password
	t.CAPEM, t.CertPEM, t.KeyPEM = cr.CAPEM, cr.CertPEM, cr.KeyPEM
	// Like the operator: TLS always; without a CA in the Device, the server
	// certificate isn't verified.
	t.Insecure = false
	t.SkipVerify = len(cr.CAPEM) == 0
	t.Label = fmt.Sprintf("k8s:%s/%s", ns, name)
	if len(t.Jumps) == 0 {
		t.Jumps, t.IdentityFiles = o.Jumps, o.IdentityFiles
	}
	if t.TimeoutSec == 0 {
		t.TimeoutSec = o.TimeoutSec
	}
	sess, err := s.Connect(ctx, t)
	if err != nil {
		return nil, err
	}
	sess.Device = &DeviceRef{Namespace: ns, Name: name, Provider: cr.Provider}
	return sess, nil
}

// DeviceRef links a session to a Device CR.
type DeviceRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
}

// VerifyLeaf is one checked leaf.
type VerifyLeaf struct {
	Path     string `json:"path"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
	Status   string `json:"status"` // ok, mismatch, missing, present (after delete)
	From     string `json:"from,omitempty"`
}

// Verification is the QA verdict for one resource.
type Verification struct {
	Resource   k8s.Resource `json:"resource"`
	Device     string       `json:"device"`
	Fixture    string       `json:"fixture"`
	Mode       string       `json:"mode"`    // applied or deleted
	Verdict    string       `json:"verdict"` // pass, fail, unknown
	Reasons    []string     `json:"reasons"`
	Leaves     []VerifyLeaf `json:"leaves"`
	Alternates []string     `json:"alternates,omitempty"` // other fixtures for the kind
	ElapsedMs  int64        `json:"elapsedMs"`
}

// Verify checks a resource against the device behind a session: the leaves the
// provider's fixture for that kind writes, mapped onto the live resource's
// spec, must be on the device with the expected values (or gone, after delete).
// The resource's own status (Ready, Paused, generation) is part of the verdict.
func (s *Service) Verify(ctx context.Context, o OperatorOptions, id, kind, ns, name string, deleted bool, fixtureName string) (*Verification, error) {
	start := time.Now()
	sess, err := s.Session(id)
	if err != nil {
		return nil, err
	}
	if sess.Device == nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "session %s is not connected through the operator; connect the Device first", id)
	}
	cl, err := s.Operator(o)
	if err != nil {
		return nil, err
	}
	v := &Verification{Device: sess.Label, Mode: "applied", Reasons: []string{}, Leaves: []VerifyLeaf{}}
	var spec map[string]any
	if deleted {
		v.Mode = "deleted"
		v.Resource = k8s.Resource{Kind: kind, Namespace: ns, Name: name}
	} else {
		r, err := cl.GetResource(ctx, kind, ns, name)
		if err != nil {
			return nil, err
		}
		v.Resource, spec = *r, r.Spec
		if ref, _ := r.Spec["deviceRef"].(map[string]any); ref == nil || ref["name"] != sess.Device.Name {
			v.Reasons = append(v.Reasons, fmt.Sprintf("the resource's deviceRef is not %s", sess.Device.Name))
		}
	}
	repo, err := fixtures.FindRepo(o.OperatorRepo)
	if err != nil {
		return nil, err
	}
	all, err := fixtures.Load(fixtures.Dir(repo, sess.Device.Provider))
	if err != nil {
		return nil, err
	}
	var fx *fixtures.Fixture
	for _, f := range all {
		if f.CR(v.Resource.Kind) == nil && f.CR(kind) == nil {
			continue
		}
		if fx == nil && (fixtureName == "" || f.File == fixtureName) {
			fx = f
		} else {
			v.Alternates = append(v.Alternates, f.File)
		}
	}
	if fx == nil {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no fixture for %s in %s", kind, sess.Device.Provider).
			WithHint("the provider may not support this kind")
	}
	v.Fixture = fx.File
	k := v.Resource.Kind
	if k == "" {
		k = kind
	}
	if deleted {
		// Spec is gone; use the fixture's own values for the removal check.
		spec, _ = fx.CR(k)["spec"].(map[string]any)
	}
	leaves, gone, err := fixtures.Expected(fx, k, spec)
	if err != nil {
		return nil, err
	}
	check := leaves
	if deleted {
		check = nil
		for _, p := range gone {
			check = append(check, fixtures.Leaf{Path: p})
		}
	}
	for _, l := range check {
		actual, found, err := readLeaf(ctx, s, id, l.Path)
		vl := VerifyLeaf{Path: l.Path, Expected: l.Value, From: l.From, Actual: actual}
		switch {
		case err != nil:
			vl.Status, vl.Actual = "error", apperr.As(err).Message
		case deleted && found:
			vl.Status = "present"
		case deleted:
			vl.Status = "ok"
		case !found:
			vl.Status = "missing"
		case equal(l.Value, actual):
			vl.Status = "ok"
		default:
			vl.Status = "mismatch"
		}
		v.Leaves = append(v.Leaves, vl)
	}
	ok := len(v.Leaves) > 0
	for _, l := range v.Leaves {
		if l.Status != "ok" {
			ok = false
		}
	}
	if !deleted {
		if v.Resource.Ready != "True" {
			v.Reasons = append(v.Reasons, "the resource is not Ready ("+v.Resource.Ready+")")
		}
		if v.Resource.Paused {
			v.Reasons = append(v.Reasons, "the resource is paused")
		}
		if v.Resource.Observed != 0 && v.Resource.Observed != v.Resource.Generation {
			v.Reasons = append(v.Reasons, fmt.Sprintf("not reconciled yet (generation %d, observed %d)", v.Resource.Generation, v.Resource.Observed))
		}
	}
	switch {
	case len(v.Leaves) == 0:
		v.Verdict = "unknown"
		v.Reasons = append(v.Reasons, "the fixture defines no device leaves to check")
	case ok && len(v.Reasons) == 0:
		v.Verdict = "pass"
	default:
		v.Verdict = "fail"
	}
	v.ElapsedMs = time.Since(start).Milliseconds()
	s.Log.Info("verify", "kind", kind, "name", name, "device", v.Device, "fixture", v.Fixture, "verdict", v.Verdict)
	return v, nil
}

func readLeaf(ctx context.Context, s *Service, id, path string) (any, bool, error) {
	res, err := s.Get(ctx, id, live.GetRequest{Paths: []string{path}})
	if err != nil {
		ae := apperr.As(err)
		if ae.GRPCCode == "NotFound" || strings.Contains(strings.ToLower(ae.Message), "not found") {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(res.Flat) == 0 {
		return nil, false, nil
	}
	// The leaf itself, or the single value under it.
	for _, u := range res.Flat {
		return u.Value, true, nil
	}
	return nil, false, nil
}

func equal(a, b any) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
}
