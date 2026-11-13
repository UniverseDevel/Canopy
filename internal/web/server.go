// Package web serves the browser UI and its JSON API on the loopback interface.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/buildinfo"
	"github.com/UniverseDevel/Canopy/internal/config"
	"github.com/UniverseDevel/Canopy/internal/connections"
	"github.com/UniverseDevel/Canopy/internal/format"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/models"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/secrets"
)

//go:embed dist
var dist embed.FS

// Server is the UI server.
type Server struct {
	svc   *app.Service
	log   *slog.Logger
	token string
	ln    net.Listener
	http  *http.Server
	// OpDefaults fill operator requests that leave fields empty.
	OpDefaults app.OperatorOptions
	audit      *audit
}

// New creates a server listening on addr (which must be a loopback address).
func New(svc *app.Service, log *slog.Logger, addr string) (*Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "the UI only listens on loopback addresses, not %q", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
	}
	b := make([]byte, 16)
	rand.Read(b)
	s := &Server{svc: svc, log: log, token: hex.EncodeToString(b), ln: ln, audit: newAudit()}
	s.http = &http.Server{Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// URL is the address to open, including the token.
func (s *Server) URL() string {
	return "http://" + s.ln.Addr().String() + "/?token=" + s.token
}

// Serve serves until ctx is done.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.http.Shutdown(sctx)
	}()
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !s.validToken(r.URL.Query().Get("token")) {
			http.Error(w, "Forbidden: open the URL printed by 'canopy ui', including its token.", http.StatusForbidden)
			return
		}
		files.ServeHTTP(w, r)
	})
	mux.Handle("GET /assets/", files)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/info", s.info)
	api.HandleFunc("GET /api/audit", s.auditList)
	api.HandleFunc("POST /api/connect", s.connect)
	api.HandleFunc("POST /api/disconnect", s.disconnect)
	api.HandleFunc("GET /api/sessions", s.sessions)
	api.HandleFunc("POST /api/get", s.get)
	api.HandleFunc("POST /api/generate", s.generate)
	api.HandleFunc("POST /api/schema/load", s.schemaLoad)
	api.HandleFunc("GET /api/schema", s.schemaSummary)
	api.HandleFunc("GET /api/schema/children", s.schemaChildren)
	api.HandleFunc("GET /api/schema/node", s.schemaNode)
	api.HandleFunc("GET /api/schema/search", s.schemaSearch)
	api.HandleFunc("GET /api/schema/find", s.schemaFind)
	api.HandleFunc("POST /api/format", s.format)
	api.HandleFunc("GET /api/connections", s.connList)
	api.HandleFunc("PUT /api/connections", s.connSave)
	api.HandleFunc("DELETE /api/connections", s.connDelete)
	api.HandleFunc("GET /api/models/sources", s.modelSources)
	api.HandleFunc("GET /api/models/versions", s.modelVersions)
	api.HandleFunc("POST /api/models/load", s.modelLoad)
	api.HandleFunc("POST /api/models/auto", s.modelAuto)
	api.HandleFunc("GET /api/models/report", s.modelReport)
	api.HandleFunc("GET /api/models/catalog", s.catReleases)
	api.HandleFunc("POST /api/models/search", s.catSearch)
	api.HandleFunc("POST /api/models/index", s.catIndex)
	api.HandleFunc("GET /api/models/profiles", s.profiles)
	api.HandleFunc("POST /api/models/device", s.deviceModels)
	api.HandleFunc("POST /api/set", s.set)
	api.HandleFunc("GET /api/schema/skeleton", s.skeleton)
	api.HandleFunc("GET /api/snapshots", s.snapshotsList)
	api.HandleFunc("POST /api/snapshots", s.snapshotTake)
	api.HandleFunc("POST /api/snapshots/diff", s.snapshotDiff)
	api.HandleFunc("DELETE /api/snapshots", s.snapshotDelete)
	api.HandleFunc("GET /api/operator/info", s.opInfo)
	api.HandleFunc("POST /api/operator/devices", s.opDevices)
	api.HandleFunc("POST /api/operator/connect", s.opConnect)
	api.HandleFunc("POST /api/operator/resources", s.opResources)
	api.HandleFunc("POST /api/operator/pause", s.opPause)
	api.HandleFunc("POST /api/operator/verify", s.opVerify)
	api.HandleFunc("POST /api/fixtures/list", s.fxList)
	api.HandleFunc("POST /api/fixtures/read", s.fxRead)
	api.HandleFunc("POST /api/fixtures/export", s.fxExport)
	api.HandleFunc("POST /api/fixtures/save", s.fxSave)
	api.HandleFunc("POST /api/fixtures/run", s.fxRun)
	mux.Handle("/api/", s.guard(api))
	return s.secure(mux)
}

// secure adds headers and blocks DNS rebinding by checking the Host header.
func (s *Server) secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "Forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// guard requires the token on every API call.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validToken(r.Header.Get("X-Canopy-Token")) {
			writeErr(w, apperr.New(apperr.ClassUsage, "forbidden", "missing or wrong token"), http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) validToken(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error, status int) {
	ae := apperr.As(err)
	if status == 0 {
		switch ae.Class {
		case apperr.ClassUsage:
			status = http.StatusBadRequest
		case apperr.ClassInternal:
			status = http.StatusInternalServerError
		default:
			status = http.StatusUnprocessableEntity
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": ae})
}

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	if err := d.Decode(v); err != nil {
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "bad request body: %v", err)
	}
	return nil
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"build": buildinfo.Get()})
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	var t app.Target
	if err := decode(r, &t); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	sess, err := s.svc.Connect(r.Context(), t)
	if err != nil {
		s.audit.add(AuditEntry{Device: t.Address, Action: "connect", Summary: t.Address, Error: apperr.As(err).Error(), Ms: time.Since(start).Milliseconds()})
		writeErr(w, err, 0)
		return
	}
	s.record(sess.ID, "connect", sess.Address, &generate.Request{Mode: "capabilities"}, start, nil)
	writeJSON(w, sess)
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	s.record(req.Session, "disconnect", "", nil, time.Now(), nil)
	s.svc.Disconnect(req.Session)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) sessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.svc.Sessions())
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Quiet   bool   `json:"quiet"` // not audited (UI helper reads)
		live.GetRequest
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	res, err := s.svc.Get(r.Context(), req.Session, req.GetRequest)
	if !req.Quiet {
		s.record(req.Session, "get", strings.Join(req.Paths, " "), &generate.Request{Mode: "get", Paths: req.Paths, Type: req.Type, Encoding: req.Encoding}, start, err)
	}
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, res)
}

func (s *Server) generate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string      `json:"session"`
		Target  *app.Target `json:"target"`
		generate.Request
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	out, err := s.svc.Generate(req.Session, req.Target, req.Request)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, out)
}

func (s *Server) schemaLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dirs    []string `json:"dirs"`
		Modules []string `json:"modules"`
		Force   bool     `json:"force"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	var dirs []string
	for _, d := range req.Dirs {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, d)
		}
	}
	t, err := s.svc.LoadSchema(dirs, schema.LoadOptions{Modules: req.Modules, Force: req.Force})
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, t.Summary())
}

func (s *Server) tree(w http.ResponseWriter) *schema.Tree {
	t, err := s.svc.Tree()
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return nil
	}
	return t
}

func (s *Server) schemaSummary(w http.ResponseWriter, _ *http.Request) {
	t, err := s.svc.Tree()
	if err != nil {
		writeJSON(w, nil) // nothing loaded yet
		return
	}
	writeJSON(w, t.Summary())
}

func (s *Server) schemaChildren(w http.ResponseWriter, r *http.Request) {
	t := s.tree(w)
	if t == nil {
		return
	}
	id := -1
	if v := r.URL.Query().Get("id"); v != "" {
		id, _ = strconv.Atoi(v)
	}
	writeJSON(w, t.Children(id))
}

func (s *Server) schemaNode(w http.ResponseWriter, r *http.Request) {
	t := s.tree(w)
	if t == nil {
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	n := t.Node(id)
	if n == nil {
		writeErr(w, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no node %d", id), http.StatusNotFound)
		return
	}
	file, line, src, err := t.Source(id, 400)
	resp := map[string]any{"node": n, "ancestors": t.Ancestors(id), "source": src, "sourceFile": file, "sourceLine": line}
	if err != nil {
		resp["sourceError"] = err.Error()
	}
	writeJSON(w, resp)
}

func (s *Server) schemaSearch(w http.ResponseWriter, r *http.Request) {
	t := s.tree(w)
	if t == nil {
		return
	}
	writeJSON(w, t.Search(r.URL.Query().Get("q"), 300))
}

// schemaFind maps a data path (for example from a Get result) to a schema node.
func (s *Server) schemaFind(w http.ResponseWriter, r *http.Request) {
	t := s.tree(w)
	if t == nil {
		return
	}
	n := t.FindPath(r.URL.Query().Get("path"))
	if n == nil {
		writeErr(w, apperr.New(apperr.ClassSchema, apperr.CodeNotFound, "path not in the loaded models"), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"node": n, "ancestors": t.Ancestors(n.ID)})
}

// format renders a JSON value as YAML (the UI does JSON and flat itself).
func (s *Server) format(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Format string          `json:"format"`
		Data   json.RawMessage `json:"data"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	d := json.NewDecoder(strings.NewReader(string(req.Data)))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		writeErr(w, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "bad data: %v", err), 0)
		return
	}
	var out string
	var err error
	switch req.Format {
	case "yaml":
		out, err = format.YAML(v)
	case "json":
		out, err = format.JSON(v)
	default:
		err = apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown format %q", req.Format)
	}
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]string{"text": out})
}

func (s *Server) connList(w http.ResponseWriter, _ *http.Request) {
	list, err := s.svc.Presets.List()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, list)
}

// connSave saves a preset. Passwords in the request are ignored (the type has no
// password field).
func (s *Server) connSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		connections.Connection
		// Opt-in: store this password in the OS keychain for the preset, or
		// remove the stored one.
		Password         string `json:"password,omitempty"`
		RememberPassword bool   `json:"rememberPassword,omitempty"`
		ForgetPassword   bool   `json:"forgetPassword,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	c := req.Connection
	if err := s.svc.Presets.Put(c); err != nil {
		writeErr(w, err, 0)
		return
	}
	switch {
	case req.RememberPassword && req.Password != "":
		if err := secrets.Set(c.Name, req.Password); err != nil {
			writeErr(w, err, 0)
			return
		}
	case req.ForgetPassword:
		secrets.Delete(c.Name)
	}
	saved, err := s.svc.Presets.Get(c.Name)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, saved)
}

func (s *Server) connDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Presets.Delete(r.URL.Query().Get("name")); err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) modelSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, models.Sources)
}

func (s *Server) modelVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.svc.Models.Versions(r.Context(), r.URL.Query().Get("source"), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, vs)
}

func (s *Server) modelLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	res, err := s.svc.LoadModels(r.Context(), req.Source, req.Version, nil)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, res)
}

// modelAuto detects the device's platform and loads the matching model set.
func (s *Server) modelAuto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	res, err := s.svc.AutoModels(r.Context(), req.Session, nil)
	if err != nil {
		if res == nil {
			writeErr(w, err, 0)
			return
		}
		writeJSON(w, map[string]any{"detection": res.Detection, "error": apperr.As(err)})
		return
	}
	writeJSON(w, res)
}

func (s *Server) modelReport(w http.ResponseWriter, r *http.Request) {
	rep, err := s.svc.MatchReport(r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, rep)
}

func (s *Server) set(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		config.Request
		app.SetOptions
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	out, err := s.svc.Set(r.Context(), req.Session, req.Request, req.SetOptions)
	if out != nil && (out.Sent || (err != nil && req.Confirm && !req.DryRun)) {
		var ops []generate.SetOp
		var sum []string
		for _, op := range req.Ops {
			ops = append(ops, generate.SetOp{Op: op.Op, Path: op.Path, Value: string(op.Value)})
			sum = append(sum, op.Op+" "+op.Path)
		}
		s.record(req.Session, "set", strings.Join(sum, ", "), &generate.Request{Mode: "set", SetOps: ops, Encoding: req.Encoding}, start, err)
	}
	if err != nil {
		if out != nil {
			// Still return the preview and validation alongside the error.
			ae := apperr.As(err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{"error": ae, "outcome": out})
			return
		}
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, out)
}

func (s *Server) skeleton(w http.ResponseWriter, r *http.Request) {
	t := s.tree(w)
	if t == nil {
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	if t.Node(id) == nil {
		writeErr(w, apperr.New(apperr.ClassUsage, apperr.CodeNotFound, "no node %d", id), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"value": config.Skeleton(t, id)})
}

func (s *Server) snapshotsList(w http.ResponseWriter, _ *http.Request) {
	l, err := s.svc.Snapshots().List()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, l)
}

func (s *Server) snapshotTake(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string   `json:"session"`
		Paths   []string `json:"paths"`
		Type    string   `json:"type"`
		Name    string   `json:"name"`
		Note    string   `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	m, err := s.svc.TakeSnapshot(r.Context(), req.Session, live.GetRequest{Paths: req.Paths, Type: req.Type}, req.Name, req.Note)
	s.record(req.Session, "snapshot", strings.Join(req.Paths, " "), &generate.Request{Mode: "get", Paths: req.Paths, Type: req.Type}, start, err)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, m)
}

func (s *Server) snapshotDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		A       string   `json:"a"`
		B       string   `json:"b"`
		Session string   `json:"session"`
		Ignore  []string `json:"ignore"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	d, err := s.svc.CompareSnapshot(r.Context(), req.A, req.B, req.Session, req.Ignore)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, d)
}

func (s *Server) snapshotDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Snapshots().Delete(r.URL.Query().Get("name")); err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
