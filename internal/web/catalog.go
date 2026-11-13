package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
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

// deviceModels downloads the device's own modules over NETCONF and loads them.
func (s *Server) deviceModels(w http.ResponseWriter, r *http.Request) {
	var req app.DeviceModelsRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	start := time.Now()
	res, err := s.svc.DeviceModels(r.Context(), req, nil)
	cmd := "canopy models from-device"
	if sess, e := s.svc.Session(req.Session); e == nil {
		if sess.Preset != "" {
			cmd += " --conn " + sess.Preset
		} else {
			cmd += " -a " + sess.Address
		}
	}
	if req.Port != 0 && req.Port != 830 {
		cmd += " --netconf-port " + strconv.Itoa(req.Port)
	}
	s.recordCmd(req.Session, "netconf", "get-schema: models from the device", cmd, start, err)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, res)
}

func (s *Server) cacheList(w http.ResponseWriter, _ *http.Request) {
	info, err := s.svc.Models.Cached()
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, info)
}

func (s *Server) cacheRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	set, err := s.svc.Models.Remove(req.Kind, req.Source, req.Version)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, set)
}

func (s *Server) cacheShare(w http.ResponseWriter, _ *http.Request) {
	res, err := s.svc.Models.Share(nil)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, res)
}
