package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/UniverseDevel/Canopy/internal/live"
)

// ServeControl runs the HTTP control API on addr until ctx ends:
//
//	GET    /v1/state  state as a JSON tree (RFC 7951)
//	PUT    /v1/state  replace the state with a JSON tree
//	DELETE /v1/state  clear the state and the Set log
//	GET    /v1/leaves state as path/value pairs
//	GET    /v1/sets   Set requests received
//
// The paths match the operator's fake gNMI server README where they overlap.
func (s *Server) ServeControl(ctx context.Context, addr string) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, _ *http.Request) {
		root := map[string]any{}
		for _, l := range s.Leaves() {
			live.MergePath(root, l.Path, l.Value)
		}
		writeJSON(w, root)
	})
	mux.HandleFunc("PUT /v1/state", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := s.Load(b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]int{"leaves": len(s.Leaves())})
	})
	mux.HandleFunc("DELETE /v1/state", func(w http.ResponseWriter, _ *http.Request) {
		s.Clear()
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/leaves", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.Leaves()) })
	mux.HandleFunc("GET /v1/sets", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.Sets()) })
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return
		}
	}()
	return ln.Addr().String(), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

