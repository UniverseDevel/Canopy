package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/schema"
)

// Skeleton returns a JSON starting point for a config node: leaves with their
// default (or an example value of their type), list entries with their keys,
// nested containers one level deep. State nodes are left out.
func Skeleton(t *schema.Tree, id int) any {
	n := t.Node(id)
	if n == nil {
		return nil
	}
	return skeleton(t, n, 2)
}

func skeleton(t *schema.Tree, n *schema.Node, depth int) any {
	switch n.Kind {
	case "leaf":
		return example(n)
	case "leaf-list":
		return []any{example(n)}
	case "list":
		return []any{object(t, n, depth)}
	default:
		return object(t, n, depth)
	}
}

func object(t *schema.Tree, n *schema.Node, depth int) map[string]any {
	out := map[string]any{}
	for _, c := range t.Children(n.ID) {
		if !c.Config {
			continue
		}
		isKey := false
		for _, k := range n.Keys {
			isKey = isKey || k == c.Name
		}
		switch {
		case c.Kind == "leaf" || c.Kind == "leaf-list":
			if isKey || c.Mandatory || len(c.Default) > 0 || depth > 0 {
				out[c.Name] = skeleton(t, c, depth)
			}
		case depth > 0:
			out[c.Name] = skeleton(t, c, depth-1)
		}
	}
	return out
}

func example(n *schema.Node) any {
	if len(n.Default) > 0 {
		return typed(n, n.Default[0])
	}
	ti := n.Type
	if ti == nil {
		return ""
	}
	switch base(ti) {
	case "enumeration":
		if len(ti.Enums) > 0 {
			return ti.Enums[0]
		}
	case "boolean":
		return false
	case "int8", "int16", "int32", "uint8", "uint16", "uint32":
		return lowest(ti.Range)
	case "int64", "uint64", "decimal64":
		return fmt.Sprint(lowest(ti.Range)) // RFC 7951: 64-bit numbers are strings
	case "empty":
		return []any{nil}
	}
	return ""
}

func typed(n *schema.Node, s string) any {
	if n.Type == nil {
		return s
	}
	switch base(n.Type) {
	case "boolean":
		return s == "true"
	case "int8", "int16", "int32", "uint8", "uint16", "uint32":
		var v json.Number = json.Number(s)
		if _, err := v.Int64(); err == nil {
			return v
		}
	}
	return s
}

func base(ti *schema.TypeInfo) string {
	if ti.Base == "leafref" || ti.Base == "union" {
		return ti.Base
	}
	return ti.Base
}

func lowest(r string) json.Number {
	if r == "" {
		return "0"
	}
	lo := strings.TrimSpace(strings.SplitN(strings.SplitN(r, "|", 2)[0], "..", 2)[0])
	if lo == "min" || lo == "" {
		return "0"
	}
	return json.Number(lo)
}

// Validate checks a JSON value against the node's schema: leaf types (enums,
// booleans, integer ranges) and, for containers and lists, that every member
// exists and is config. It returns problems (empty when valid).
func Validate(t *schema.Tree, id int, raw json.RawMessage) []string {
	n := t.Node(id)
	if n == nil {
		return []string{"node not found"}
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return []string{"not valid JSON: " + err.Error()}
	}
	var probs []string
	validate(t, n, v, n.Path, &probs)
	return probs
}

func validate(t *schema.Tree, n *schema.Node, v any, at string, probs *[]string) {
	add := func(f string, a ...any) { *probs = append(*probs, at+": "+fmt.Sprintf(f, a...)) }
	if !n.Config {
		add("is state (read-only) and can't be configured")
		return
	}
	switch n.Kind {
	case "leaf":
		if err := checkLeaf(n.Type, v); err != "" {
			add("%s", err)
		}
	case "leaf-list":
		arr, ok := v.([]any)
		if !ok {
			add("leaf-list needs a JSON array")
			return
		}
		for _, x := range arr {
			if err := checkLeaf(n.Type, x); err != "" {
				add("%s", err)
			}
		}
	case "list":
		arr, ok := v.([]any)
		if !ok {
			if m, isObj := v.(map[string]any); isObj { // one entry given directly
				members(t, n, m, at, probs)
				return
			}
			add("list needs a JSON array of entries")
			return
		}
		for i, x := range arr {
			m, ok := x.(map[string]any)
			if !ok {
				add("entry %d is not an object", i)
				continue
			}
			for _, k := range n.Keys {
				if _, ok := lookup(m, k); !ok {
					add("entry %d is missing key %q", i, k)
				}
			}
			members(t, n, m, fmt.Sprintf("%s[%d]", at, i), probs)
		}
	default:
		m, ok := v.(map[string]any)
		if !ok {
			add("%s needs a JSON object", n.Kind)
			return
		}
		members(t, n, m, at, probs)
	}
}

func members(t *schema.Tree, n *schema.Node, m map[string]any, at string, probs *[]string) {
	kids := map[string]*schema.Node{}
	for _, c := range t.Children(n.ID) {
		kids[c.Name] = c
	}
	for k, v := range m {
		name := k
		if i := strings.IndexByte(k, ':'); i >= 0 {
			name = k[i+1:]
		}
		c, ok := kids[name]
		if !ok {
			*probs = append(*probs, fmt.Sprintf("%s: unknown member %q", at, k))
			continue
		}
		validate(t, c, v, at+"/"+name, probs)
	}
}

func lookup(m map[string]any, k string) (any, bool) {
	for mk, v := range m {
		if mk == k || strings.HasSuffix(mk, ":"+k) {
			return v, true
		}
	}
	return nil, false
}

// checkLeaf checks a value against a leaf type. Unions and leafrefs accept any
// scalar; patterns aren't checked (YANG uses XSD regular expressions).
func checkLeaf(ti *schema.TypeInfo, v any) string {
	if ti == nil {
		return ""
	}
	switch ti.Base {
	case "union", "leafref", "identityref", "instance-identifier":
		if _, isObj := v.(map[string]any); isObj {
			return "needs a scalar value"
		}
		return ""
	case "enumeration":
		s, ok := v.(string)
		if !ok {
			return "enumeration needs a string"
		}
		for _, e := range ti.Enums {
			if e == s {
				return ""
			}
		}
		return fmt.Sprintf("%q is not one of %s", s, strings.Join(ti.Enums, ", "))
	case "boolean":
		if _, ok := v.(bool); !ok {
			return "needs true or false"
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return "needs a string"
		}
		if ti.Length != "" && !inRange(ti.Length, big.NewInt(int64(len([]rune(s))))) {
			return fmt.Sprintf("length %d outside %s", len([]rune(s)), ti.Length)
		}
	case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64":
		var s string
		switch x := v.(type) {
		case json.Number:
			s = string(x)
		case string: // RFC 7951 encodes 64-bit integers as strings
			s = x
		default:
			return "needs an integer"
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return fmt.Sprintf("%q is not an integer", s)
		}
		if lim, ok := builtinRange[ti.Base]; ok && !inRange(lim, n) {
			return fmt.Sprintf("%s outside %s (%s)", s, lim, ti.Base)
		}
		if ti.Range != "" && !inRange(ti.Range, n) {
			return fmt.Sprintf("%s outside range %s", s, ti.Range)
		}
	}
	return ""
}

var builtinRange = map[string]string{
	"int8": "-128..127", "int16": "-32768..32767", "int32": "-2147483648..2147483647",
	"int64": "-9223372036854775808..9223372036854775807",
	"uint8": "0..255", "uint16": "0..65535", "uint32": "0..4294967295", "uint64": "0..18446744073709551615",
}

// inRange checks n against a YANG range like "1..10|20|30..max".
func inRange(r string, n *big.Int) bool {
	for _, part := range strings.Split(r, "|") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "..")
		lo, hi = strings.TrimSpace(lo), strings.TrimSpace(hi)
		if !isRange {
			hi = lo
		}
		okLo := lo == "min" || cmp(n, lo) >= 0
		okHi := hi == "max" || cmp(n, hi) <= 0
		if okLo && okHi {
			return true
		}
	}
	return false
}

func cmp(n *big.Int, s string) int {
	m, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return 0 // decimal or unparsable bound: don't reject
	}
	return n.Cmp(m)
}
