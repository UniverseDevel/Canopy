package fixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/UniverseDevel/Canopy/internal/snapshot"
)

// Match compares device state with a fixture state the way the operator's test
// does (JSON equality, arrays in any order), with one leniency: empty objects
// and arrays count as absent, because a shadow target keeps leaves, not empty
// containers. It returns the leaf differences when they don't match.
func Match(want, got any) (bool, []snapshot.Change) {
	w, g := normalize(want), normalize(got)
	if equalUnordered(w, g) {
		return true, nil
	}
	d := snapshot.Diff(flat(w), flat(g), nil)
	return false, d.Changes
}

// normalize converts to plain JSON values (json.Number for numbers) and drops
// empty objects and arrays.
func normalize(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var x any
	d := json.NewDecoder(bytesReader(b))
	d.UseNumber()
	if d.Decode(&x) != nil {
		return v
	}
	return prune(x)
}

func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			if p := prune(c); p == nil {
				delete(t, k)
			} else {
				t[k] = p
			}
		}
		if len(t) == 0 {
			return nil
		}
	case []any:
		var out []any
		for _, c := range t {
			if p := prune(c); p != nil {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

func equalUnordered(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if !equalUnordered(v, y[k]) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		used := make([]bool, len(y))
	next:
		for _, v := range x {
			for j, w := range y {
				if !used[j] && equalUnordered(v, w) {
					used[j] = true
					continue next
				}
			}
			return false
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		return ok && (x == y || fmt.Sprint(numVal(x)) == fmt.Sprint(numVal(y)))
	default:
		return reflect.DeepEqual(a, b)
	}
}

func numVal(n json.Number) any {
	if i, err := n.Int64(); err == nil {
		return i
	}
	f, _ := n.Float64()
	return f
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
