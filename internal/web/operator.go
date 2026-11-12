package web

import (
	"net/http"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/k8s"
)

// opReq is the common part of operator requests. Empty fields take the
// server's defaults (canopy ui --kubeconfig, --operator-jump, ...).
type opReq struct {
	Options   app.OperatorOptions `json:"options"`
	Namespace string              `json:"namespace"`
}

func (s *Server) opts(o app.OperatorOptions) app.OperatorOptions {
	d := s.OpDefaults
	if o.Kubeconfig == "" {
		o.Kubeconfig = d.Kubeconfig
	}
	if o.Context == "" {
		o.Context = d.Context
	}
	if len(o.Jumps) == 0 {
		o.Jumps = d.Jumps
	}
	if len(o.IdentityFiles) == 0 {
		o.IdentityFiles = d.IdentityFiles
	}
	if o.OperatorRepo == "" {
		o.OperatorRepo = d.OperatorRepo
	}
	if o.TimeoutSec == 0 {
		o.TimeoutSec = d.TimeoutSec
	}
	return o
}

func (s *Server) opInfo(w http.ResponseWriter, r *http.Request) {
	o := s.opts(app.OperatorOptions{Options: k8s.Options{Kubeconfig: r.URL.Query().Get("kubeconfig")}})
	ctxs, cur, err := k8s.Contexts(o.Options)
	resp := map[string]any{"defaults": s.OpDefaults, "contexts": ctxs, "current": cur}
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
	sess, err := s.svc.ConnectDevice(r.Context(), s.opts(req.Options), req.Namespace, req.Name, req.Target)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
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
	if err := cl.SetPaused(r.Context(), req.Kind, req.Namespace, req.Name, req.Paused); err != nil {
		writeErr(w, err, 0)
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
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	v, err := s.svc.Verify(r.Context(), s.opts(req.Options), req.Session, req.Kind, req.Namespace, req.Name, req.Deleted, req.Fixture)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, v)
}
