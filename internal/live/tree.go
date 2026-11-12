package live

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
)

// mergeAt places v at the path elems inside root. Keyed elements become lists of
// objects that carry their key leaves, as in RFC 7951 JSON. Module prefixes in
// element names ("oc-if:interfaces") are kept as the device sent them.
func mergeAt(root map[string]any, elems []*gpb.PathElem, v any) {
	if len(elems) == 0 {
		if m, ok := v.(map[string]any); ok {
			mergeMaps(root, m)
		}
		return
	}
	cur := root
	for i, e := range elems {
		last := i == len(elems)-1
		if len(e.GetKey()) > 0 {
			list, _ := cur[e.Name].([]any)
			entry := findEntry(list, e.GetKey())
			if entry == nil {
				entry = map[string]any{}
				for k, kv := range e.GetKey() {
					entry[k] = kv
				}
				list = append(list, entry)
				cur[e.Name] = list
			}
			if last {
				if m, ok := v.(map[string]any); ok {
					mergeMaps(entry, m)
				}
				return
			}
			cur = entry
			continue
		}
		if last {
			if m, ok := v.(map[string]any); ok {
				existing, _ := cur[e.Name].(map[string]any)
				if existing == nil {
					cur[e.Name] = m
				} else {
					mergeMaps(existing, m)
				}
			} else {
				cur[e.Name] = v
			}
			return
		}
		next, ok := cur[e.Name].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[e.Name] = next
		}
		cur = next
	}
}

func findEntry(list []any, keys map[string]string) map[string]any {
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		match := true
		for k, kv := range keys {
			if fmt.Sprint(lookupKey(m, k)) != kv {
				match = false
				break
			}
		}
		if match {
			return m
		}
	}
	return nil
}

// lookupKey finds k in m, also as a module-prefixed name ("mod:k").
func lookupKey(m map[string]any, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	for mk, v := range m {
		if i := strings.IndexByte(mk, ':'); i >= 0 && mk[i+1:] == k {
			return v
		}
	}
	return nil
}

func mergeMaps(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeMaps(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// keyGuess lists leaf names commonly used as list keys, tried in order when the
// schema isn't known. Without a schema a list entry is addressed by these, or by
// its index.
var keyGuess = []string{"name", "id", "index", "prefix", "address", "ip-prefix", "neighbor-address", "identifier", "key"}

// Flatten calls fn for every leaf under v, with the path built from base.
// Lists of objects become keyed elements when a key can be guessed.
func Flatten(base string, v any, fn func(path string, leaf any)) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			fn(base, x)
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			Flatten(joinPath(base, k), x[k], fn)
		}
	case []any:
		if len(x) == 0 {
			fn(base, x)
			return
		}
		if _, ok := x[0].(map[string]any); !ok {
			fn(base, x) // leaf-list
			return
		}
		for i, e := range x {
			m, ok := e.(map[string]any)
			if !ok {
				fn(fmt.Sprintf("%s[%d]", base, i), e)
				continue
			}
			sel, key := listSelector(m, i)
			rest := make(map[string]any, len(m))
			for k, vv := range m {
				if k != key {
					rest[k] = vv
				}
			}
			Flatten(base+sel, rest, fn)
		}
	default:
		fn(base, v)
	}
}

func listSelector(m map[string]any, i int) (sel, key string) {
	for _, g := range keyGuess {
		for k, v := range m {
			name := k
			if j := strings.IndexByte(k, ':'); j >= 0 {
				name = k[j+1:]
			}
			if name == g {
				switch v.(type) {
				case string, json.Number, float64, int64, uint64, bool:
					return fmt.Sprintf("[%s=%v]", name, v), k
				}
			}
		}
	}
	return fmt.Sprintf("[%d]", i), ""
}

func joinPath(base, k string) string {
	if strings.HasSuffix(base, "/") {
		return base + k
	}
	return base + "/" + k
}
