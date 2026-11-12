// Package live reads data from devices.
package live

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
	"google.golang.org/protobuf/proto"
)

// GetRequest is a Get on one or more paths.
type GetRequest struct {
	Paths    []string `json:"paths"`
	Type     string   `json:"type,omitempty"`     // all (default), config, state, operational
	Encoding string   `json:"encoding,omitempty"` // json_ietf, json, ...; empty = from Capabilities
	Prefix   string   `json:"prefix,omitempty"`
}

// Update is one value at a full path.
type Update struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// Notification is one notification with full paths.
type Notification struct {
	Timestamp int64    `json:"timestamp"`
	Updates   []Update `json:"updates,omitempty"`
	Deletes   []string `json:"deletes,omitempty"`
}

// Result is the result of a Get.
type Result struct {
	Paths         []string       `json:"paths"`
	Type          string         `json:"type"`
	Encoding      string         `json:"encoding"`
	Notifications []Notification `json:"notifications"`
	// Data merges all updates into one JSON tree rooted at "/".
	Data any `json:"data"`
	// Flat lists every leaf as path = value.
	Flat        []Update `json:"flat"`
	Empty       bool     `json:"empty"`
	ElapsedMs   int64    `json:"elapsedMs"`
	SizeBytes   int      `json:"sizeBytes"`
	LeafCount   int      `json:"leafCount"`
	UpdateCount int      `json:"updateCount"`
}

var dataTypes = map[string]gpb.GetRequest_DataType{
	"":            gpb.GetRequest_ALL,
	"all":         gpb.GetRequest_ALL,
	"config":      gpb.GetRequest_CONFIG,
	"state":       gpb.GetRequest_STATE,
	"operational": gpb.GetRequest_OPERATIONAL,
}

// BuildGetRequest validates and builds the gNMI request.
func BuildGetRequest(r GetRequest) (*gpb.GetRequest, error) {
	if len(r.Paths) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "at least one path is required")
	}
	dt, ok := dataTypes[strings.ToLower(r.Type)]
	if !ok {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown data type %q (all, config, state, operational)", r.Type)
	}
	if r.Encoding == "" {
		r.Encoding = "json_ietf"
	}
	enc, ok := conn.ParseEncoding(r.Encoding)
	if !ok {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown encoding %q", r.Encoding)
	}
	req := &gpb.GetRequest{Type: dt, Encoding: enc}
	if r.Prefix != "" {
		p, err := gpath.Parse(r.Prefix)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		req.Prefix = p
	}
	for _, s := range r.Paths {
		p, err := gpath.Parse(s)
		if err != nil {
			return nil, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		req.Path = append(req.Path, p)
	}
	return req, nil
}

// Get runs a Get on c.
func Get(ctx context.Context, c *conn.Conn, r GetRequest) (*Result, error) {
	req, err := BuildGetRequest(r)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	// Many devices refuse a Get that mixes origins (native and "openconfig:"
	// paths); send one Get per origin and merge the results.
	var groups [][]*gpb.Path
	idx := map[string]int{}
	for _, p := range req.Path {
		i, ok := idx[p.GetOrigin()]
		if !ok {
			i = len(groups)
			idx[p.GetOrigin()] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], p)
	}
	var notes []*gpb.Notification
	size := 0
	for _, g := range groups {
		one := proto.Clone(req).(*gpb.GetRequest)
		one.Path = g
		resp, err := c.Client.Get(ctx, one)
		if err != nil {
			return nil, apperr.FromDevice(err)
		}
		notes = append(notes, resp.GetNotification()...)
		size += proto.Size(resp)
	}
	res, err := Convert(notes)
	if err != nil {
		return nil, err
	}
	res.Paths = r.Paths
	res.Type = strings.ToLower(req.Type.String())
	res.Encoding = conn.EncodingName(req.Encoding)
	res.ElapsedMs = time.Since(start).Milliseconds()
	res.SizeBytes = size
	return res, nil
}

// Convert turns notifications into a Result (without request fields).
func Convert(ns []*gpb.Notification) (*Result, error) {
	res := &Result{Notifications: []Notification{}, Flat: []Update{}}
	root := map[string]any{}
	for _, n := range ns {
		out := Notification{Timestamp: n.GetTimestamp()}
		for _, u := range n.GetUpdate() {
			full := gpath.Join(n.GetPrefix(), u.GetPath())
			v, err := DecodeValue(u.GetVal())
			if err != nil {
				return nil, apperr.Wrap(apperr.ClassDevice, apperr.CodeDevice,
					fmt.Errorf("decoding value at %s: %w", gpath.String(full), err))
			}
			ps := gpath.String(full)
			out.Updates = append(out.Updates, Update{Path: ps, Value: v})
			mergeAt(root, full.GetElem(), v)
			Flatten(ps, v, func(p string, leaf any) {
				res.Flat = append(res.Flat, Update{Path: p, Value: leaf})
			})
			res.UpdateCount++
		}
		for _, d := range n.GetDelete() {
			out.Deletes = append(out.Deletes, gpath.String(gpath.Join(n.GetPrefix(), d)))
		}
		res.Notifications = append(res.Notifications, out)
	}
	res.Data = root
	res.LeafCount = len(res.Flat)
	res.Empty = res.UpdateCount == 0
	return res, nil
}

// DecodeValue converts a TypedValue to a JSON-compatible Go value.
func DecodeValue(tv *gpb.TypedValue) (any, error) {
	if tv == nil {
		return nil, nil
	}
	switch v := tv.GetValue().(type) {
	case *gpb.TypedValue_JsonIetfVal:
		return decodeJSON(v.JsonIetfVal)
	case *gpb.TypedValue_JsonVal:
		return decodeJSON(v.JsonVal)
	case *gpb.TypedValue_StringVal:
		return v.StringVal, nil
	case *gpb.TypedValue_IntVal:
		return v.IntVal, nil
	case *gpb.TypedValue_UintVal:
		return v.UintVal, nil
	case *gpb.TypedValue_BoolVal:
		return v.BoolVal, nil
	case *gpb.TypedValue_FloatVal:
		return v.FloatVal, nil
	case *gpb.TypedValue_DoubleVal:
		return v.DoubleVal, nil
	case *gpb.TypedValue_DecimalVal:
		d := v.DecimalVal
		return json.Number(formatDecimal(d.GetDigits(), d.GetPrecision())), nil
	case *gpb.TypedValue_BytesVal:
		return v.BytesVal, nil
	case *gpb.TypedValue_AsciiVal:
		return v.AsciiVal, nil
	case *gpb.TypedValue_LeaflistVal:
		var out []any
		for _, e := range v.LeaflistVal.GetElement() {
			x, err := DecodeValue(e)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	case *gpb.TypedValue_ProtoBytes:
		return v.ProtoBytes, nil
	case *gpb.TypedValue_AnyVal:
		return v.AnyVal.String(), nil
	}
	return nil, fmt.Errorf("unsupported value type %T", tv.GetValue())
}

func decodeJSON(b []byte) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber() // keep 64-bit counters exact
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func formatDecimal(digits int64, precision uint32) string {
	neg := digits < 0
	if neg {
		digits = -digits
	}
	s := fmt.Sprintf("%0*d", int(precision)+1, digits)
	if precision > 0 {
		s = s[:len(s)-int(precision)] + "." + s[len(s)-int(precision):]
	}
	if neg {
		s = "-" + s
	}
	return s
}
