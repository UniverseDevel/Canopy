// Package proxy is a transparent, recording gRPC proxy. It forwards every
// service and method (gNMI, gNOI, anything) as raw frames, decodes and records
// the gNMI messages it understands, and records other calls by method name and
// size. An optional HTTP(S) reverse proxy covers NX-API, which the operator's
// NX-OS provider sends to the same device address on port 443/80.
package proxy

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// frame is a raw gRPC message.
type frame struct{ payload []byte }

// rawCodec passes frames through untouched.
type rawCodec struct{}

func (rawCodec) Name() string { return "proto" } // same content subtype as protobuf

func (rawCodec) Marshal(v any) ([]byte, error) {
	f, ok := v.(*frame)
	if !ok {
		return nil, fmt.Errorf("proxy codec: unexpected %T", v)
	}
	return f.payload, nil
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	f, ok := v.(*frame)
	if !ok {
		return fmt.Errorf("proxy codec: unexpected %T", v)
	}
	f.payload = append([]byte(nil), data...)
	return nil
}

// Record is one message that passed through.
type Record struct {
	Time      time.Time       `json:"time"`
	Call      int64           `json:"call"`      // call number
	Method    string          `json:"method"`    // /gnmi.gNMI/Set
	Direction string          `json:"direction"` // request or response
	Size      int             `json:"size"`
	Message   json.RawMessage `json:"message,omitempty"` // decoded gNMI message
	Error     string          `json:"error,omitempty"`   // call status at the end
	Peer      string          `json:"peer,omitempty"`
}

// Options configure the proxy.
type Options struct {
	Listen   string
	Upstream string
	// TLS on the listening side; nil means plaintext.
	TLS *tls.Config
	// Upstream TLS; nil means plaintext.
	UpstreamTLS *tls.Config
	// MaxRecords kept in memory (default 10000); OnRecord is called for each.
	MaxRecords int
	OnRecord   func(Record)
}

// Proxy forwards and records.
type Proxy struct {
	opt      Options
	upstream *grpc.ClientConn
	mu       sync.Mutex
	records  []Record
	calls    int64
}

// New creates a proxy (not yet listening).
func New(o Options) (*Proxy, error) {
	if o.MaxRecords == 0 {
		o.MaxRecords = 10000
	}
	creds := insecure.NewCredentials()
	if o.UpstreamTLS != nil {
		creds = credentials.NewTLS(o.UpstreamTLS)
	}
	cc, err := grpc.NewClient("passthrough:///"+o.Upstream, grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256<<20), grpc.ForceCodec(rawCodec{})))
	if err != nil {
		return nil, err
	}
	return &Proxy{opt: o, upstream: cc}, nil
}

// Serve listens and forwards until ctx ends; it returns the listen address.
func (p *Proxy) Serve(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", p.opt.Listen)
	if err != nil {
		return "", err
	}
	opts := []grpc.ServerOption{
		grpc.ForceServerCodec(rawCodec{}),
		grpc.UnknownServiceHandler(p.handle),
		grpc.MaxRecvMsgSize(256 << 20),
	}
	if p.opt.TLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(p.opt.TLS)))
	}
	s := grpc.NewServer(opts...)
	go func() { <-ctx.Done(); s.Stop(); p.upstream.Close() }()
	go s.Serve(ln)
	return ln.Addr().String(), nil
}

func (p *Proxy) record(r Record) {
	p.mu.Lock()
	p.records = append(p.records, r)
	if len(p.records) > p.opt.MaxRecords {
		p.records = p.records[len(p.records)-p.opt.MaxRecords:]
	}
	p.mu.Unlock()
	if p.opt.OnRecord != nil {
		p.opt.OnRecord(r)
	}
}

// Records returns the recorded messages (optionally only one method).
func (p *Proxy) Records(method string) []Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Record{}
	for _, r := range p.records {
		if method == "" || strings.HasSuffix(r.Method, method) {
			out = append(out, r)
		}
	}
	return out
}

// Clear drops the recorded messages.
func (p *Proxy) Clear() {
	p.mu.Lock()
	p.records = nil
	p.mu.Unlock()
}

// handle forwards one call of any method in both directions.
func (p *Proxy) handle(_ any, ss grpc.ServerStream) error {
	method, ok := grpc.MethodFromServerStream(ss)
	if !ok {
		return errors.New("proxy: unknown method")
	}
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	md, _ := metadata.FromIncomingContext(ss.Context())
	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(ss.Context(), md.Copy()))
	defer cancel()
	cs, err := p.upstream.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, method)
	if err != nil {
		p.record(Record{Time: time.Now(), Call: call, Method: method, Direction: "response", Error: err.Error()})
		return err
	}
	// client -> upstream
	upErr := make(chan error, 1)
	go func() {
		for {
			f := &frame{}
			if err := ss.RecvMsg(f); err != nil {
				if errors.Is(err, io.EOF) {
					cs.CloseSend()
					upErr <- nil
				} else {
					upErr <- err
				}
				return
			}
			p.record(Record{Time: time.Now(), Call: call, Method: method, Direction: "request", Size: len(f.payload), Message: decode(method, true, f.payload)})
			if err := cs.SendMsg(f); err != nil {
				upErr <- err
				return
			}
		}
	}()
	// upstream -> client
	first := true
	for {
		f := &frame{}
		err := cs.RecvMsg(f)
		if first {
			if h, herr := cs.Header(); herr == nil {
				ss.SendHeader(h)
			}
			first = false
		}
		if err != nil {
			ss.SetTrailer(cs.Trailer())
			if errors.Is(err, io.EOF) {
				return nil
			}
			p.record(Record{Time: time.Now(), Call: call, Method: method, Direction: "response", Error: err.Error()})
			return err
		}
		p.record(Record{Time: time.Now(), Call: call, Method: method, Direction: "response", Size: len(f.payload), Message: decode(method, false, f.payload)})
		if err := ss.SendMsg(f); err != nil {
			return err
		}
		select {
		case e := <-upErr:
			if e != nil {
				return e
			}
		default:
		}
	}
}

// decode renders gNMI messages as JSON; other services are recorded by size.
func decode(method string, request bool, b []byte) json.RawMessage {
	var m proto.Message
	switch method {
	case "/gnmi.gNMI/Capabilities":
		m = pick[*gpb.CapabilityRequest, *gpb.CapabilityResponse](request)
	case "/gnmi.gNMI/Get":
		m = pick[*gpb.GetRequest, *gpb.GetResponse](request)
	case "/gnmi.gNMI/Set":
		m = pick[*gpb.SetRequest, *gpb.SetResponse](request)
	case "/gnmi.gNMI/Subscribe":
		m = pick[*gpb.SubscribeRequest, *gpb.SubscribeResponse](request)
	default:
		return nil
	}
	if err := proto.Unmarshal(b, m); err != nil {
		return nil
	}
	out, err := protojson.MarshalOptions{UseProtoNames: false}.Marshal(m)
	if err != nil {
		return nil
	}
	return readableValues(out)
}

// readableValues replaces base64 jsonVal/jsonIetfVal fields with the JSON they
// carry, so records show values as sent.
func readableValues(b []byte) json.RawMessage {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	var walk func(x any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, vv := range t {
				if s, ok := vv.(string); ok && (k == "jsonVal" || k == "jsonIetfVal") {
					if raw, err := base64.StdEncoding.DecodeString(s); err == nil && json.Valid(raw) {
						t[k] = json.RawMessage(raw)
						continue
					}
				}
				t[k] = walk(vv)
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		}
		return x
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		return b
	}
	return out
}

func pick[Req, Resp proto.Message](request bool) proto.Message {
	var r Req
	var s Resp
	if request {
		return r.ProtoReflect().New().Interface()
	}
	return s.ProtoReflect().New().Interface()
}

// ReverseHTTPS serves an HTTP(S) reverse proxy to upstream (for NX-API) until
// ctx ends. Requests are recorded by method and path (bodies are not kept).
func (p *Proxy) ReverseHTTPS(ctx context.Context, listen, upstream string, serverTLS, upstreamTLS *tls.Config) (string, error) {
	scheme := "http"
	if upstreamTLS != nil {
		scheme = "https"
	}
	target, err := url.Parse(scheme + "://" + upstream)
	if err != nil {
		return "", err
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{TLSClientConfig: upstreamTLS}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.calls++
		call := p.calls
		p.mu.Unlock()
		p.record(Record{Time: time.Now(), Call: call, Method: "HTTP " + r.Method + " " + r.URL.Path, Direction: "request", Size: int(r.ContentLength), Peer: r.RemoteAddr})
		rp.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return "", err
	}
	if serverTLS != nil {
		ln = tls.NewListener(ln, serverTLS)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	go srv.Serve(ln)
	return ln.Addr().String(), nil
}
