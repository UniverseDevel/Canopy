// Package config builds, previews, validates and applies gNMI Set requests.
// Applying is guarded: callers must confirm explicitly, and a dry run sends
// nothing.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/gpath"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

// Op is one change.
type Op struct {
	Op    string          `json:"op"` // update (merge), replace, delete
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"` // JSON (JSON_IETF), not for delete
}

// Request is a set of changes.
type Request struct {
	Ops      []Op   `json:"ops"`
	Encoding string `json:"encoding,omitempty"` // json_ietf (default) or json
	Prefix   string `json:"prefix,omitempty"`
}

// Build validates the operations and builds the gNMI SetRequest.
func Build(r Request) (*gpb.SetRequest, error) {
	if len(r.Ops) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no changes given")
	}
	enc := strings.ToLower(r.Encoding)
	if enc == "" {
		enc = "json_ietf"
	}
	if enc != "json_ietf" && enc != "json" {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "Set supports json_ietf or json values, not %q", r.Encoding)
	}
	req := &gpb.SetRequest{}
	if r.Prefix != "" {
		p, err := gpath.Parse(r.Prefix)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		req.Prefix = p
	}
	for i, op := range r.Ops {
		p, err := gpath.Parse(op.Path)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, fmt.Errorf("change %d: %w", i+1, err))
		}
		if strings.Contains(op.Path, "=*]") {
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "change %d: path %s has a wildcard key; give concrete list keys", i+1, op.Path)
		}
		switch strings.ToLower(op.Op) {
		case "delete":
			req.Delete = append(req.Delete, p)
		case "update", "replace", "":
			if len(op.Value) == 0 {
				return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "change %d (%s %s) needs a value", i+1, op.Op, op.Path)
			}
			var probe any
			if err := json.Unmarshal(op.Value, &probe); err != nil {
				return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "change %d: value is not valid JSON: %v", i+1, err)
			}
			tv := &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(op.Value)}}
			if enc == "json" {
				tv = &gpb.TypedValue{Value: &gpb.TypedValue_JsonVal{JsonVal: []byte(op.Value)}}
			}
			u := &gpb.Update{Path: p, Val: tv}
			if strings.ToLower(op.Op) == "replace" {
				req.Replace = append(req.Replace, u)
			} else {
				req.Update = append(req.Update, u)
			}
		default:
			return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "change %d: unknown operation %q (update, replace, delete)", i+1, op.Op)
		}
	}
	return req, nil
}

// Destructive reports whether the request replaces or deletes anything.
func Destructive(r Request) bool {
	for _, op := range r.Ops {
		if o := strings.ToLower(op.Op); o == "replace" || o == "delete" {
			return true
		}
	}
	return false
}

// Result is what a Set returned.
type Result struct {
	Timestamp int64          `json:"timestamp"`
	Results   []UpdateResult `json:"results"`
	ElapsedMs int64          `json:"elapsedMs"`
}

// UpdateResult is one operation's result.
type UpdateResult struct {
	Op   string `json:"op"`
	Path string `json:"path"`
}

// Apply sends the request.
func Apply(ctx context.Context, c *conn.Conn, req *gpb.SetRequest) (*Result, error) {
	start := time.Now()
	resp, err := c.Client.Set(ctx, req)
	if err != nil {
		return nil, apperr.FromDevice(err)
	}
	out := &Result{Timestamp: resp.GetTimestamp(), ElapsedMs: time.Since(start).Milliseconds()}
	for _, r := range resp.GetResponse() {
		out.Results = append(out.Results, UpdateResult{
			Op:   strings.ToLower(r.GetOp().String()),
			Path: gpath.String(gpath.Join(resp.GetPrefix(), r.GetPath())),
		})
	}
	return out, nil
}

// Preview renders the request readably: paths as strings, values as JSON.
func Preview(req *gpb.SetRequest) string {
	type upd struct {
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	}
	out := struct {
		Prefix  string   `json:"prefix,omitempty"`
		Delete  []string `json:"delete,omitempty"`
		Replace []upd    `json:"replace,omitempty"`
		Update  []upd    `json:"update,omitempty"`
	}{}
	if req.GetPrefix() != nil {
		out.Prefix = gpath.String(req.GetPrefix())
	}
	for _, d := range req.GetDelete() {
		out.Delete = append(out.Delete, gpath.String(d))
	}
	conv := func(us []*gpb.Update) []upd {
		var r []upd
		for _, u := range us {
			raw := u.GetVal().GetJsonIetfVal()
			if raw == nil {
				raw = u.GetVal().GetJsonVal()
			}
			r = append(r, upd{Path: gpath.String(u.GetPath()), Value: raw})
		}
		return r
	}
	out.Replace, out.Update = conv(req.GetReplace()), conv(req.GetUpdate())
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}
