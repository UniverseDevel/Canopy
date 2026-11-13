package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	// the kubeconfig files' times are part of the key: a kubeconfig written
	// again (a new cluster port, refreshed credentials) gets a new client
	key := o.Kubeconfig + "\x00" + o.Context + "\x00" + kubeconfigStamp(o.Kubeconfig)
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

// kubeconfigStamp names the kubeconfig files client-go reads (the given
// one, else $KUBECONFIG's, else ~/.kube/config) with their modification times.
func kubeconfigStamp(kubeconfig string) string {
	files := []string{kubeconfig}
	if kubeconfig == "" {
		files = filepath.SplitList(os.Getenv("KUBECONFIG"))
		if len(files) == 0 {
			if home, err := os.UserHomeDir(); err == nil {
				files = []string{filepath.Join(home, ".kube", "config")}
			}
		}
	}
	var b strings.Builder
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s@%d;", f, fi.ModTime().UnixNano())
		}
	}
	return b.String()
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
	fromOptions := len(t.Jumps) == 0 && len(o.Jumps) > 0
	if len(t.Jumps) == 0 {
		t.Jumps, t.IdentityFiles = o.Jumps, o.IdentityFiles
	}
	if t.TimeoutSec == 0 {
		t.TimeoutSec = o.TimeoutSec
	}
	sess, err := s.Connect(ctx, t)
	if err != nil {
		// a jump host that fails is often one that doesn't belong here (a lab's,
		// from the run configuration): say where it comes from
		if e := apperr.As(err); fromOptions && e.Details["jumpHost"] != "" && e.Hint == "" {
			e.Hint = fmt.Sprintf("the jump host %s comes from the cluster settings (⚙ in NetOp) or 'canopy ui --operator-jump'; "+
				"change it there, or choose to connect directly", strings.Join(o.Jumps, ", "))
			return nil, e
		}
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
	Status   string `json:"status"` // ok, mismatch, missing; after delete: ok (gone), other (a value the resource doesn't set), present
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
// After a delete the resource is gone; deletedYAML, when given (the YAML that
// was applied, e.g. a sample's), says which leaves to check instead of the
// fixture's own resource.
func (s *Service) Verify(ctx context.Context, o OperatorOptions, id, kind, ns, name string, deleted bool, fixtureName, deletedYAML string) (*Verification, error) {
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
		// Spec is gone: use what was applied when known, else the fixture's own.
		spec, _ = fx.CR(k)["spec"].(map[string]any)
		if deletedYAML != "" {
			docs, err := decodeDocs([]byte(deletedYAML))
			if err != nil {
				return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("the deleted resource's YAML could not be read")
			}
			for _, d := range docs {
				if strings.EqualFold(str(d, "kind"), k) {
					if sp, ok := d["spec"].(map[string]any); ok {
						spec = sp
					}
					break
				}
			}
		}
	}
	leaves, gone, err := fixtures.Expected(fx, k, spec)
	if err != nil {
		return nil, err
	}
	check := leaves
	if deleted {
		// what the resource set at each path, to tell its value from another
		// one (a device default, e.g. SR Linux's welcome banner)
		set := map[string]fixtures.Leaf{}
		for _, l := range leaves {
			set[l.Path] = l
		}
		check = nil
		for _, p := range gone {
			l := set[p]
			l.Path = p
			check = append(check, l)
		}
	}
	for _, l := range check {
		actual, found, err := readLeaf(ctx, s, id, l.Path)
		vl := VerifyLeaf{Path: l.Path, Expected: l.Value, From: l.From, Actual: actual}
		switch {
		case err != nil:
			vl.Status, vl.Actual = "error", apperr.As(err).Message
		case deleted && found && l.Value != nil && !equal(l.Value, actual):
			vl.Status = "other" // the resource's value is gone; something else is there
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
	others := 0
	for _, l := range v.Leaves {
		switch {
		case l.Status == "other":
			others++
		case l.Status != "ok":
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
	if others > 0 {
		// a note, not a reason to fail
		v.Reasons = append(v.Reasons, fmt.Sprintf("%d leaf(s) hold a value the resource doesn't set (e.g. the device's default): its own value is gone", others))
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
