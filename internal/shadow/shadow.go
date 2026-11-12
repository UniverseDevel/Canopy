// Package shadow is a gNMI target that keeps its state in memory: Capabilities
// (replayable from a real device), Get, Set (update, replace, delete) and
// Subscribe ONCE. It stands in for a device in tests and dry runs; its state can
// be loaded and read over an HTTP control API (see http.go).
package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/gpath"
	"github.com/UniverseDevel/Canopy/internal/live"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Server is the shadow target.
type Server struct {
	gpb.UnimplementedGNMIServer

	mu     sync.RWMutex
	leaves map[string]leaf // normalized path -> leaf
	caps   *gpb.CapabilityResponse
	// Username and Password, when set, are required in the request metadata.
	Username, Password string
	// Log of Set requests received (for "what did the client send?").
	sets []SetRecord
}

type leaf struct {
	path  string // as stored (concrete, with keys)
	value any
}

// SetRecord is one received Set.
type SetRecord struct {
	Time    time.Time `json:"time"`
	Request string    `json:"request"` // readable form
}

// New creates an empty target advertising JSON_IETF and no models.
func New() *Server {
	return &Server{leaves: map[string]leaf{}, caps: &gpb.CapabilityResponse{
		GNMIVersion: "0.10.0", SupportedEncodings: []gpb.Encoding{gpb.Encoding_JSON_IETF, gpb.Encoding_JSON},
	}}
}

// SetCapabilities replaces what Capabilities returns (replay a real device).
func (s *Server) SetCapabilities(c *gpb.CapabilityResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.caps = c
}

// Serve listens on addr (for example 127.0.0.1:0) and serves until ctx ends. It
// returns the actual address once listening.
func (s *Server) Serve(ctx context.Context, addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	g := grpc.NewServer()
	gpb.RegisterGNMIServer(g, s)
	go func() { <-ctx.Done(); g.Stop() }()
	go g.Serve(ln)
	return ln.Addr().String(), nil
}

func (s *Server) auth(ctx context.Context) error {
	if s.Username == "" && s.Password == "" {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if first(md["username"]) != s.Username || first(md["password"]) != s.Password {
		return status.Error(codes.Unauthenticated, "invalid username or password")
	}
	return nil
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// Capabilities returns the configured capabilities.
func (s *Server) Capabilities(ctx context.Context, _ *gpb.CapabilityRequest) (*gpb.CapabilityResponse, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caps, nil
}

// Get returns one update per leaf under each requested path.
func (s *Server) Get(ctx context.Context, r *gpb.GetRequest) (*gpb.GetResponse, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	n, err := s.read(r.GetPrefix(), r.GetPath())
	if err != nil {
		return nil, err
	}
	return &gpb.GetResponse{Notification: []*gpb.Notification{n}}, nil
}

// Subscribe supports mode ONCE.
func (s *Server) Subscribe(stream gpb.GNMI_SubscribeServer) error {
	if err := s.auth(stream.Context()); err != nil {
		return err
	}
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	sl := req.GetSubscribe()
	if sl == nil || sl.GetMode() != gpb.SubscriptionList_ONCE {
		return status.Error(codes.Unimplemented, "the shadow target supports Subscribe ONCE only")
	}
	var paths []*gpb.Path
	for _, sub := range sl.GetSubscription() {
		paths = append(paths, sub.GetPath())
	}
	n, err := s.read(sl.GetPrefix(), paths)
	if err != nil {
		return err
	}
	if err := stream.Send(&gpb.SubscribeResponse{Response: &gpb.SubscribeResponse_Update{Update: n}}); err != nil {
		return err
	}
	return stream.Send(&gpb.SubscribeResponse{Response: &gpb.SubscribeResponse_SyncResponse{SyncResponse: true}})
}

func (s *Server) read(prefix *gpb.Path, paths []*gpb.Path) (*gpb.Notification, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := &gpb.Notification{Timestamp: time.Now().UnixNano()}
	keys := make([]string, 0, len(s.leaves))
	for k := range s.leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(paths) == 0 {
		paths = []*gpb.Path{{}}
	}
	for _, p := range paths {
		want := gpath.Join(prefix, p)
		for _, k := range keys {
			l := s.leaves[k]
			lp, err := gpath.Parse(l.path)
			if err != nil || !under(lp, want) {
				continue
			}
			b, _ := json.Marshal(l.value)
			n.Update = append(n.Update, &gpb.Update{Path: lp, Val: &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: b}}})
		}
	}
	return n, nil
}

// Set applies deletes, then replaces, then updates (gNMI order).
func (s *Server) Set(ctx context.Context, r *gpb.SetRequest) (*gpb.SetResponse, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &gpb.SetResponse{Timestamp: time.Now().UnixNano(), Prefix: r.GetPrefix()}
	for _, d := range r.GetDelete() {
		s.deleteUnder(gpath.Join(r.GetPrefix(), d))
		resp.Response = append(resp.Response, &gpb.UpdateResult{Path: d, Op: gpb.UpdateResult_DELETE})
	}
	for _, u := range r.GetReplace() {
		full := gpath.Join(r.GetPrefix(), u.GetPath())
		s.deleteUnder(full)
		if err := s.write(full, u.GetVal()); err != nil {
			return nil, err
		}
		resp.Response = append(resp.Response, &gpb.UpdateResult{Path: u.GetPath(), Op: gpb.UpdateResult_REPLACE})
	}
	for _, u := range r.GetUpdate() {
		if err := s.write(gpath.Join(r.GetPrefix(), u.GetPath()), u.GetVal()); err != nil {
			return nil, err
		}
		resp.Response = append(resp.Response, &gpb.UpdateResult{Path: u.GetPath(), Op: gpb.UpdateResult_UPDATE})
	}
	s.sets = append(s.sets, SetRecord{Time: time.Now().UTC(), Request: setString(r)})
	return resp, nil
}

func (s *Server) write(p *gpb.Path, tv *gpb.TypedValue) error {
	v, err := live.DecodeValue(tv)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "value at %s: %v", gpath.String(p), err)
	}
	if strings.Contains(gpath.String(p), "=*]") {
		return status.Errorf(codes.InvalidArgument, "path %s has a wildcard key", gpath.String(p))
	}
	live.Flatten(gpath.String(p), v, func(lp string, lv any) {
		s.leaves[norm(lp)] = leaf{path: lp, value: lv}
	})
	return nil
}

func (s *Server) deleteUnder(p *gpb.Path) {
	for k, l := range s.leaves {
		lp, err := gpath.Parse(l.path)
		if err == nil && under(lp, p) {
			delete(s.leaves, k)
		}
	}
}

// under reports whether leaf path lp is at or below want. Element names are
// compared without module prefixes; "*" (or a missing key) matches any key value.
func under(lp, want *gpb.Path) bool {
	if len(want.GetElem()) > len(lp.GetElem()) {
		return false
	}
	for i, we := range want.GetElem() {
		le := lp.GetElem()[i]
		if bare(we.GetName()) != bare(le.GetName()) {
			return false
		}
		for k, v := range we.GetKey() {
			if v != "*" && le.GetKey()[k] != v {
				return false
			}
		}
	}
	return true
}

func bare(n string) string {
	if i := strings.IndexByte(n, ':'); i >= 0 {
		return n[i+1:]
	}
	return n
}

// norm is the map key: path without module prefixes.
func norm(p string) string {
	gp, err := gpath.Parse(p)
	if err != nil {
		return p
	}
	for _, e := range gp.Elem {
		e.Name = bare(e.Name)
	}
	gp.Origin = ""
	return gpath.String(gp)
}

func setString(r *gpb.SetRequest) string {
	var b strings.Builder
	for _, d := range r.GetDelete() {
		fmt.Fprintf(&b, "delete %s\n", gpath.String(gpath.Join(r.GetPrefix(), d)))
	}
	for _, u := range r.GetReplace() {
		v, _ := live.DecodeValue(u.GetVal())
		j, _ := json.Marshal(v)
		fmt.Fprintf(&b, "replace %s = %s\n", gpath.String(gpath.Join(r.GetPrefix(), u.GetPath())), j)
	}
	for _, u := range r.GetUpdate() {
		v, _ := live.DecodeValue(u.GetVal())
		j, _ := json.Marshal(v)
		fmt.Fprintf(&b, "update %s = %s\n", gpath.String(gpath.Join(r.GetPrefix(), u.GetPath())), j)
	}
	return b.String()
}

// Load replaces the state with a JSON tree (RFC 7951, rooted at "/").
func (s *Server) Load(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaves = map[string]leaf{}
	live.Flatten("/", v, func(lp string, lv any) { s.leaves[norm(lp)] = leaf{path: lp, value: lv} })
	return nil
}

// Clear removes all state and the Set log.
func (s *Server) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaves = map[string]leaf{}
	s.sets = nil
}

// Leaves returns the state as path = value pairs, sorted.
func (s *Server) Leaves() []live.Update {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]live.Update, 0, len(s.leaves))
	for _, l := range s.leaves {
		out = append(out, live.Update{Path: l.path, Value: l.value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Sets returns the Set requests received.
func (s *Server) Sets() []SetRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]SetRecord{}, s.sets...)
}
