package app

import (
	"context"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/buildinfo"
	"github.com/UniverseDevel/Canopy/internal/config"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/snapshot"
)

// SetOptions control a Set.
type SetOptions struct {
	DryRun  bool `json:"dryRun"`  // validate and preview only; nothing is sent
	Confirm bool `json:"confirm"` // required to send; the caller asked the human
	// Shell for the generated gnmic command (bash or powershell).
	Shell string `json:"shell,omitempty"`
	// Ignore lists path fragments left out of the before/after diff.
	Ignore []string `json:"ignore,omitempty"`
}

// SetOutcome is the result of a Set, a dry run or a preview.
type SetOutcome struct {
	Preview     string               `json:"preview"`     // the SetRequest, readable
	Gnmic       []generate.Command   `json:"gnmic"`       // equivalent gnmic set
	Problems    []string             `json:"problems"`    // schema validation
	Destructive bool                 `json:"destructive"` // replace or delete
	Sent        bool                 `json:"sent"`
	Result      *config.Result       `json:"result,omitempty"`
	Diff        *snapshot.DiffResult `json:"diff,omitempty"` // config before vs after
}

// Set validates, previews and (when confirmed and not a dry run) applies
// changes. Before and after the change it reads the affected paths (config
// only) and returns the difference.
func (s *Service) Set(ctx context.Context, id string, r config.Request, opt SetOptions) (*SetOutcome, error) {
	sess, err := s.Session(id)
	if err != nil {
		return nil, err
	}
	if r.Encoding == "" && (sess.Encoding == "json" || sess.Encoding == "json_ietf") {
		r.Encoding = sess.Encoding
	}
	req, err := config.Build(r)
	if err != nil {
		return nil, err
	}
	out := &SetOutcome{Preview: config.Preview(req), Destructive: config.Destructive(r), Problems: []string{}}
	// Validate against the loaded schema where the node is known.
	if t, err := s.Tree(); err == nil {
		for _, op := range r.Ops {
			if strings.EqualFold(op.Op, "delete") {
				continue
			}
			n := t.FindPath(op.Path)
			if n == nil {
				out.Problems = append(out.Problems, op.Path+": not in the loaded models (not validated)")
				continue
			}
			out.Problems = append(out.Problems, config.Validate(t, n.ID, op.Value)...)
		}
	}
	var ops []generate.SetOp
	for _, op := range r.Ops {
		ops = append(ops, generate.SetOp{Op: op.Op, Path: op.Path, Value: string(op.Value)})
	}
	if g, err := generate.GnmicSet(generate.Request{Target: sess.conn.Opts, Hops: sess.conn.Hops, Encoding: r.Encoding, Shell: opt.Shell}, ops); err == nil {
		out.Gnmic = g.Commands
	}
	if opt.DryRun {
		s.Log.Info("set dry run", "session", id, "ops", len(r.Ops), "problems", len(out.Problems))
		return out, nil
	}
	if !opt.Confirm {
		return out, apperr.New(apperr.ClassUsage, "confirmation_required",
			"changing the device needs explicit confirmation").
			WithHint("review the preview, then confirm (CLI: --yes)")
	}
	paths := opPaths(r)
	before, _ := live.Get(ctx, sess.conn, live.GetRequest{Paths: paths, Type: "config", Encoding: sess.Encoding})
	s.Log.Warn("set", "session", id, "address", sess.Address, "ops", len(r.Ops), "destructive", out.Destructive, "paths", paths)
	res, err := config.Apply(ctx, sess.conn, req)
	if err != nil {
		s.Log.Warn("set failed", "session", id, "error", apperr.As(err).Message)
		return out, err
	}
	out.Sent, out.Result = true, res
	after, _ := live.Get(ctx, sess.conn, live.GetRequest{Paths: paths, Type: "config", Encoding: sess.Encoding})
	if before != nil && after != nil {
		d := snapshot.Diff(before.Flat, after.Flat, opt.Ignore)
		out.Diff = &d
	}
	return out, nil
}

// opPaths are the paths to read around a Set: each changed path; for updates
// of a single leaf, the leaf itself.
func opPaths(r config.Request) []string {
	seen := map[string]bool{}
	var out []string
	for _, op := range r.Ops {
		if !seen[op.Path] {
			seen[op.Path] = true
			out = append(out, op.Path)
		}
	}
	return out
}

// Snapshots is the snapshot store.
func (s *Service) Snapshots() snapshot.Store { return snapshot.Store{Dir: snapshot.DefaultDir()} }

// TakeSnapshot reads paths and stores the result.
func (s *Service) TakeSnapshot(ctx context.Context, id string, r live.GetRequest, name, note string) (*snapshot.Meta, error) {
	sess, err := s.Session(id)
	if err != nil {
		return nil, err
	}
	res, err := s.Get(ctx, id, r)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if name == "" {
		name = snapshot.NewName(sess.Label, now)
	}
	sn := &snapshot.Snapshot{
		Meta: snapshot.Meta{Name: name, Device: sess.Label, Address: sess.Address, Time: now, Paths: r.Paths,
			Type: res.Type, Encoding: res.Encoding, Models: sess.Models, Canopy: buildinfo.Get().Version, Note: note},
		Flat: res.Flat, Data: res.Data,
	}
	if err := s.Snapshots().Save(sn); err != nil {
		return nil, err
	}
	s.Log.Info("snapshot", "name", name, "leaves", len(res.Flat))
	return &sn.Meta, nil
}

// CompareSnapshot diffs a snapshot against another one, or against the device
// now when other is empty (same paths and type, read from session id).
func (s *Service) CompareSnapshot(ctx context.Context, name, other, id string, ignore []string) (*snapshot.DiffResult, error) {
	st := s.Snapshots()
	a, err := st.Load(name)
	if err != nil {
		return nil, err
	}
	var b []live.Update
	if other != "" {
		o, err := st.Load(other)
		if err != nil {
			return nil, err
		}
		b = o.Flat
	} else {
		res, err := s.Get(ctx, id, live.GetRequest{Paths: a.Meta.Paths, Type: a.Meta.Type, Encoding: a.Meta.Encoding})
		if err != nil {
			return nil, err
		}
		b = res.Flat
	}
	d := snapshot.Diff(a.Flat, b, ignore)
	return &d, nil
}
