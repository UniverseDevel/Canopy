package web

import (
	"net/http"

	"github.com/UniverseDevel/Canopy/internal/catalog"
	"github.com/UniverseDevel/Canopy/internal/models"
)

// Model catalog and platform profiles.

func (s *Server) catReleases(w http.ResponseWriter, _ *http.Request) {
	c, err := s.svc.Models.Catalog()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	rs, err := c.Releases()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	if rs == nil {
		rs = []catalog.Release{}
	}
	writeJSON(w, rs)
}

func (s *Server) catSearch(w http.ResponseWriter, r *http.Request) {
	var q catalog.Query
	if err := decode(r, &q); err != nil {
		writeErr(w, err, 0)
		return
	}
	c, err := s.svc.Models.Catalog()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	es, err := c.Search(r.Context(), q)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	if es == nil {
		es = []catalog.Entry{}
	}
	writeJSON(w, es)
}

func (s *Server) catIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	res, err := s.svc.Models.Index(r.Context(), req.Source, models.NormalizeVersion(req.Source, req.Version))
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, res)
}

func (s *Server) profiles(w http.ResponseWriter, _ *http.Request) {
	ps, err := models.Profiles()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]any{"dir": models.ProfilesDir(), "profiles": ps})
}
