// Package gpath converts between gNMI paths and their string form
// ("origin:/a/b[k=v]/c"), as used by gNMIc.
package gpath

import (
	"fmt"
	"sort"
	"strings"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

// Parse parses a path string. An optional origin is given as "origin:/path" (the
// colon must come before the first slash, so module-prefixed elements such as
// "/oc-if:interfaces" are not mistaken for an origin).
func Parse(s string) (*gpb.Path, error) {
	s = strings.TrimSpace(s)
	p := &gpb.Path{}
	if i := strings.Index(s, ":/"); i > 0 && !strings.ContainsAny(s[:i], "/[") {
		p.Origin = s[:i]
		s = s[i+1:]
	}
	elems, err := splitElems(s)
	if err != nil {
		return nil, err
	}
	for _, e := range elems {
		pe, err := parseElem(e)
		if err != nil {
			return nil, err
		}
		p.Elem = append(p.Elem, pe)
	}
	return p, nil
}

// splitElems splits on '/' outside of key brackets. Backslash escapes the next
// character inside keys.
func splitElems(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && depth > 0 && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
			continue
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("path %q: unbalanced ']'", s)
			}
		case c == '/' && depth == 0:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}
	if depth != 0 {
		return nil, fmt.Errorf("path %q: unbalanced '['", s)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out, nil
}

func parseElem(s string) (*gpb.PathElem, error) {
	i := strings.IndexByte(s, '[')
	if i < 0 {
		return &gpb.PathElem{Name: s}, nil
	}
	pe := &gpb.PathElem{Name: s[:i], Key: map[string]string{}}
	rest := s[i:]
	for len(rest) > 0 {
		if rest[0] != '[' {
			return nil, fmt.Errorf("element %q: expected '['", s)
		}
		// find the matching ']' honoring escapes
		j := 1
		var kv strings.Builder
		for ; j < len(rest) && rest[j] != ']'; j++ {
			if rest[j] == '\\' && j+1 < len(rest) {
				j++
			}
			kv.WriteByte(rest[j])
		}
		if j >= len(rest) {
			return nil, fmt.Errorf("element %q: missing ']'", s)
		}
		k, v, ok := strings.Cut(kv.String(), "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("element %q: key must be name=value", s)
		}
		pe.Key[k] = v
		rest = rest[j+1:]
	}
	return pe, nil
}

// String formats a path. The origin, if any, is written as "origin:/...".
func String(p *gpb.Path) string {
	if p == nil {
		return "/"
	}
	var b strings.Builder
	if p.Origin != "" {
		b.WriteString(p.Origin)
		b.WriteByte(':')
	}
	if len(p.Elem) == 0 {
		b.WriteByte('/')
	}
	for _, e := range p.Elem {
		b.WriteByte('/')
		b.WriteString(e.Name)
		keys := make([]string, 0, len(e.Key))
		for k := range e.Key {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "[%s=%s]", k, escapeKey(e.Key[k]))
		}
	}
	return b.String()
}

func escapeKey(v string) string {
	if !strings.ContainsAny(v, `]\`) {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `]`, `\]`)
	return r.Replace(v)
}

// Join returns prefix + path as one path. The origin of the prefix wins unless empty.
func Join(prefix, p *gpb.Path) *gpb.Path {
	out := &gpb.Path{}
	if prefix != nil {
		out.Origin = prefix.Origin
		out.Elem = append(out.Elem, prefix.Elem...)
	}
	if p != nil {
		if out.Origin == "" {
			out.Origin = p.Origin
		}
		out.Elem = append(out.Elem, p.Elem...)
	}
	return out
}
