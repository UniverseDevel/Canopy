package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/fixtures"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/k8s"
)

// opReq is the common part of operator requests. Empty fields take the saved
// cluster settings (operator.json), then the server's defaults (canopy ui
// --kubeconfig, --operator-jump, ...).
type opReq struct {
	Options   app.OperatorOptions `json:"options"`
	Namespace string              `json:"namespace"`
}

func (s *Server) opts(o app.OperatorOptions) app.OperatorOptions {
	st := readOpSettings()
	if o.Kubeconfig == "" {
		o.Kubeconfig = st.Kubeconfig
	}
	if o.Context == "" {
		o.Context = st.Context
	}
	if len(o.Jumps) == 0 {
		o.Jumps = st.Jumps
	}
	if len(o.IdentityFiles) == 0 {
		o.IdentityFiles = st.IdentityFiles
	}
	// "connect directly" in the settings wins over the --operator-jump default
	direct := st.Direct && len(o.Jumps) == 0
	d := s.OpDefaults
	if o.Kubeconfig == "" {
		o.Kubeconfig = d.Kubeconfig
	}
	if o.Context == "" {
		o.Context = d.Context
	}
	if len(o.Jumps) == 0 && !direct {
		o.Jumps = d.Jumps
	}
	if len(o.IdentityFiles) == 0 && !direct {
		o.IdentityFiles = d.IdentityFiles
	}
	if o.OperatorRepo == "" && fixtures.Choice().Current == "" {
		o.OperatorRepo = d.OperatorRepo // the chosen checkout, if any, wins (FindRepo)
	}
	if o.TimeoutSec == 0 {
		o.TimeoutSec = d.TimeoutSec
	}
	return o
}

func (s *Server) opInfo(w http.ResponseWriter, r *http.Request) {
	o := s.opts(app.OperatorOptions{Options: k8s.Options{Kubeconfig: r.URL.Query().Get("kubeconfig")}})
	ctxs, cur, err := k8s.Contexts(o.Options)
	resp := map[string]any{"defaults": s.OpDefaults, "settings": readOpSettings(), "contexts": ctxs, "current": cur}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

func (s *Server) opDevices(w http.ResponseWriter, r *http.Request) {
	var req opReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	cl, err := s.svc.Operator(s.opts(req.Options))
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	ds, err := cl.Devices(r.Context(), req.Namespace)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]any{"context": cl.Context, "devices": ds})
}

func (s *Server) opConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		Name   string     `json:"name"`
		Target app.Target `json:"target"` // host key acceptance, passphrases
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	sess, err := s.svc.ConnectDevice(r.Context(), s.opts(req.Options), req.Namespace, req.Name, req.Target)
	if err != nil {
		s.audit.add(AuditEntry{Device: req.Namespace + "/" + req.Name, Action: "connect", Summary: "operator Device", Error: apperr.As(err).Error(), Ms: time.Since(start).Milliseconds()})
		writeErr(w, err, 0)
		return
	}
	s.record(sess.ID, "connect", "operator Device "+req.Namespace+"/"+req.Name, &generate.Request{Mode: "capabilities"}, start, nil)
	writeJSON(w, sess)
}

func (s *Server) opResources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		Device string `json:"device"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	cl, err := s.svc.Operator(s.opts(req.Options))
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	rs, err := cl.Resources(r.Context(), req.Namespace, req.Device)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, rs)
}

func (s *Server) opPause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Paused  bool   `json:"paused"`
		WaitSec int    `json:"waitSec"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	cl, err := s.svc.Operator(s.opts(req.Options))
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	perr := cl.SetPaused(r.Context(), req.Kind, req.Namespace, req.Name, req.Paused)
	act := "resume"
	if req.Paused {
		act = "pause"
	}
	s.audit.add(AuditEntry{Action: act, Summary: req.Kind + " " + req.Namespace + "/" + req.Name, OK: perr == nil, Error: errString(perr), Ms: time.Since(start).Milliseconds(),
		Command: map[string]string{"bash": kubectlPause(req.Kind, req.Namespace, req.Name, req.Paused), "powershell": kubectlPause(req.Kind, req.Namespace, req.Name, req.Paused)}})
	if perr != nil {
		writeErr(w, perr, 0)
		return
	}
	state := ""
	if req.WaitSec > 0 {
		state, err = cl.WaitPaused(r.Context(), req.Kind, req.Namespace, req.Name, req.Paused, time.Duration(req.WaitSec)*time.Second)
		if err != nil {
			writeErr(w, err, 0)
			return
		}
	}
	writeJSON(w, map[string]any{"paused": req.Paused, "condition": state})
}

func (s *Server) opVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		Session string `json:"session"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
		Fixture string `json:"fixture"`
		YAML    string `json:"yaml"` // after a delete: what was applied
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	v, err := s.svc.Verify(r.Context(), s.opts(req.Options), req.Session, req.Kind, req.Namespace, req.Name, req.Deleted, req.Fixture, req.YAML)
	sum := req.Kind + " " + req.Namespace + "/" + req.Name
	if v != nil {
		sum += ": " + v.Verdict
	}
	// the reads Verify made, as a gNMIc command to repeat them by hand
	var gen *generate.Request
	if v != nil && len(v.Leaves) > 0 {
		gen = &generate.Request{Mode: "get"}
		for _, l := range v.Leaves {
			gen.Paths = append(gen.Paths, l.Path)
		}
	}
	s.record(req.Session, "verify", sum, gen, start, err)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, v)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return apperr.As(err).Error()
}

// kubectlPause is the kubectl command equivalent to pausing or resuming.
func kubectlPause(kind, ns, name string, paused bool) string {
	if paused {
		return "kubectl annotate " + strings.ToLower(kind) + " " + name + " -n " + ns + " "+k8s.PausedAnnotation+"=true --overwrite"
	}
	return "kubectl annotate " + strings.ToLower(kind) + " " + name + " -n " + ns + " "+k8s.PausedAnnotation+"-"
}

// opSamples lists network-operator's sample resources for a Device.
func (s *Server) opSamples(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		Device   string `json:"device"`
		Provider string `json:"provider"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	list, dir, err := s.svc.Samples(r.Context(), s.opts(req.Options), req.Namespace, req.Device, req.Provider)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]any{"dir": dir, "samples": list})
}

// opApply applies objects (a sample, maybe edited) to a Device; opRemove
// deletes them. Both change the device's configuration through the operator.
func (s *Server) opApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		Device string `json:"device"`
		YAML   string `json:"yaml"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	done, applied, err := s.svc.ApplySamples(r.Context(), s.opts(req.Options), req.Namespace, req.Device, req.YAML)
	cmd := "kubectl apply --server-side --field-manager=canopy --force-conflicts -f - <<'EOF'\n" + applied + "EOF"
	ps := "@'\n" + applied + "'@ | kubectl apply --server-side --field-manager=canopy --force-conflicts -f -"
	s.audit.add(AuditEntry{Action: "apply", Summary: strings.Join(done, ", ") + " for Device " + req.Namespace + "/" + req.Device, OK: err == nil, Error: errString(err),
		Ms: time.Since(start).Milliseconds(), Command: map[string]string{"bash": cmd, "powershell": ps}})
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]any{"applied": done})
}

func (s *Server) opRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		opReq
		YAML string `json:"yaml"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	done, err := s.svc.DeleteSamples(r.Context(), s.opts(req.Options), req.Namespace, req.YAML)
	var cmds []string
	for _, d := range done {
		cmds = append(cmds, "kubectl delete -n "+req.Namespace+" "+strings.ToLower(strings.Replace(d, "/", " ", 1)))
	}
	cmd := strings.Join(cmds, "\n")
	s.audit.add(AuditEntry{Action: "delete", Summary: strings.Join(done, ", ") + " in " + req.Namespace, OK: err == nil, Error: errString(err),
		Ms: time.Since(start).Milliseconds(), Command: map[string]string{"bash": cmd, "powershell": cmd}})
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]any{"deleted": done})
}
