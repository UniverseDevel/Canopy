package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/snapshot"
)

// AuditEntry is one operation the UI performed on a device, with the gNMIc
// command that does the same.
type AuditEntry struct {
	Seq     int64             `json:"seq"`
	Time    time.Time         `json:"time"`
	Session string            `json:"session,omitempty"`
	Device  string            `json:"device,omitempty"`
	Action  string            `json:"action"` // connect, get, set, snapshot, verify, ...
	Summary string            `json:"summary,omitempty"`
	Command map[string]string `json:"command,omitempty"` // shell -> command line
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Ms      int64             `json:"ms"`
}

// audit keeps the recent entries in memory and appends every entry to
// audit.jsonl in Canopy's config folder (next to snapshots).
type audit struct {
	mu      sync.Mutex
	seq     int64
	entries []AuditEntry
	file    string
}

func newAudit() *audit {
	return &audit{file: filepath.Join(filepath.Dir(snapshot.DefaultDir()), "audit.jsonl")}
}

func (a *audit) add(e AuditEntry) {
	a.mu.Lock()
	a.seq++
	e.Seq = a.seq
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	a.entries = append(a.entries, e)
	if len(a.entries) > 2000 {
		a.entries = a.entries[len(a.entries)-2000:]
	}
	a.mu.Unlock()
	if a.file == "" {
		return
	}
	if b, err := json.Marshal(e); err == nil {
		os.MkdirAll(filepath.Dir(a.file), 0o700)
		if f, err := os.OpenFile(a.file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

func (a *audit) since(seq int64) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []AuditEntry{}
	for _, e := range a.entries {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

// record adds an entry for an operation on a session. gen describes the
// equivalent gNMIc command (nil: none).
func (s *Server) record(session, action, summary string, gen *generate.Request, start time.Time, err error) {
	e := AuditEntry{Session: session, Action: action, Summary: summary, OK: err == nil, Ms: time.Since(start).Milliseconds()}
	if err != nil {
		e.Error = apperr.As(err).Error()
	}
	if sess, serr := s.svc.Session(session); serr == nil {
		e.Device = sess.Label
	}
	if gen != nil && session != "" {
		e.Command = map[string]string{}
		for _, sh := range []string{"bash", "powershell"} {
			r := *gen
			r.Shell = sh
			if out, gerr := s.svc.Generate(session, nil, r); gerr == nil && len(out.Commands) > 0 {
				e.Command[sh] = out.Commands[0].Text
			}
		}
	}
	s.audit.add(e)
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	writeJSON(w, map[string]any{"entries": s.audit.since(since), "file": s.audit.file})
}

// recordCmd audits an operation whose equivalent is a fixed command (the same
// for both shells).
func (s *Server) recordCmd(session, action, summary, cmd string, start time.Time, err error) {
	e := AuditEntry{Session: session, Action: action, Summary: summary, OK: err == nil, Ms: time.Since(start).Milliseconds()}
	if err != nil {
		e.Error = apperr.As(err).Error()
	}
	if sess, serr := s.svc.Session(session); serr == nil {
		e.Device = sess.Label
	}
	if cmd != "" {
		e.Command = map[string]string{"bash": cmd, "powershell": cmd}
	}
	s.audit.add(e)
}
