package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/fixtures"
)

type fixtureInfo struct {
	File  string   `json:"file"`
	Title string   `json:"title"`
	Kinds []string `json:"kinds"`
}

// fxRepo returns the chosen network-operator checkout, the recent ones and
// the one in use.
func (s *Server) fxRepo(w http.ResponseWriter, _ *http.Request) {
	c := fixtures.Choice()
	out := map[string]any{"current": c.Current, "recent": c.Recent}
	if repo, err := fixtures.FindRepo(s.opts(app.OperatorOptions{}).OperatorRepo); err == nil {
		out["inUse"] = repo
	} else {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

// fxRepoSet chooses the checkout (path; "" for the default) or forgets one.
func (s *Server) fxRepoSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Forget string `json:"forget"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	var err error
	if req.Forget != "" {
		_, err = fixtures.Forget(req.Forget)
	} else {
		_, err = fixtures.Choose(req.Path)
	}
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	s.fxRepo(w, r)
}

// fxList lists the providers (provider empty) or a provider's fixtures.
func (s *Server) fxList(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options  app.OperatorOptions `json:"options"`
		Provider string              `json:"provider"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	o := s.opts(req.Options)
	repo, err := fixtures.FindRepo(o.OperatorRepo)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	providers := []string{}
	if ents, err := os.ReadDir(filepath.Join(repo, "test", "gnmi", "testdata")); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				providers = append(providers, e.Name())
			}
		}
	}
	sort.Strings(providers)
	out := map[string]any{"repo": repo, "providers": providers, "fixtures": []fixtureInfo{}}
	if req.Provider != "" {
		dir, fs, err := s.svc.FixtureList(o, req.Provider)
		if err != nil {
			writeErr(w, err, 0)
			return
		}
		var list []fixtureInfo
		for _, f := range fs {
			list = append(list, fixtureInfo{File: f.File, Title: f.Title, Kinds: f.Kinds()})
		}
		out["dir"], out["fixtures"] = dir, list
	}
	writeJSON(w, out)
}

func (s *Server) fxRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options  app.OperatorOptions `json:"options"`
		Provider string              `json:"provider"`
		File     string              `json:"file"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	repo, err := fixtures.FindRepo(s.opts(req.Options).OperatorRepo)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	if req.File != filepath.Base(req.File) || req.Provider != filepath.Base(req.Provider) {
		writeErr(w, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "bad file name"), 0)
		return
	}
	b, err := os.ReadFile(filepath.Join(fixtures.Dir(repo, req.Provider), req.File))
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.ClassUsage, apperr.CodeNotFound, err), 0)
		return
	}
	writeJSON(w, map[string]string{"text": string(b)})
}

// fxExport builds fixture text from a resource YAML and snapshots.
func (s *Server) fxExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		CR      string `json:"cr"`
		Preload string `json:"preload"`
		Expect  string `json:"expect"`
		Delete  string `json:"delete"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	st := s.svc.Snapshots()
	load := func(name string) (any, error) {
		if name == "" {
			return nil, nil
		}
		sn, err := st.Load(name)
		if err != nil {
			return nil, err
		}
		return sn.Data, nil
	}
	var vals [3]any
	for i, n := range []string{req.Preload, req.Expect, req.Delete} {
		v, err := load(n)
		if err != nil {
			writeErr(w, err, 0)
			return
		}
		vals[i] = v
	}
	if vals[1] == nil {
		writeErr(w, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "an expect snapshot is required"), 0)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "Exported by Canopy"
	}
	b, err := fixtures.Export(req.Title, []byte(req.CR), vals[0], vals[1], vals[2])
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]string{"text": string(b)})
}

func (s *Server) fxSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options  app.OperatorOptions `json:"options"`
		Provider string              `json:"provider"`
		Name     string              `json:"name"`
		Text     string              `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	file, err := s.svc.FixtureSave(s.opts(req.Options), req.Provider, req.Name, []byte(req.Text))
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]string{"file": file})
}

func (s *Server) fxRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Options app.OperatorOptions   `json:"options"`
		Run     app.FixtureRunOptions `json:"run"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	res, err := s.svc.RunFixture(r.Context(), s.opts(req.Options), req.Run)
	sum := req.Run.Provider + " " + req.Run.File
	e := AuditEntry{Action: "fixture", Summary: sum, OK: err == nil && res != nil && res.Verdict == "pass", Error: errString(err), Ms: time.Since(start).Milliseconds()}
	if res != nil {
		e.Summary += ": " + res.Verdict
	}
	s.audit.add(e)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, res)
}
