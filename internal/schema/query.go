package schema

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/gpath"
)

// Node returns the node with id, or nil.
func (t *Tree) Node(id int) *Node {
	if id < 0 || id >= len(t.Nodes) {
		return nil
	}
	return t.Nodes[id]
}

// Children returns the children of id, or the roots for id < 0.
func (t *Tree) Children(id int) []*Node {
	ids := t.Roots
	if id >= 0 {
		n := t.Node(id)
		if n == nil {
			return nil
		}
		ids = n.Children
	}
	out := make([]*Node, 0, len(ids))
	for _, i := range ids {
		out = append(out, t.Nodes[i])
	}
	return out
}

// Ancestors returns the ids from the root down to id's parent.
func (t *Tree) Ancestors(id int) []int {
	var out []int
	for n := t.Node(id); n != nil && n.Parent >= 0; n = t.Node(n.Parent) {
		out = append([]int{n.Parent}, out...)
	}
	return out
}

// FindPath returns the node with the given data path (keys may be given or "*";
// module prefixes are ignored), or nil.
// When several modules define the same path (for example /system in ietf-system,
// openconfig-system and a vendor module), a module prefix on the first element
// ("/srl_nokia-system:system/...", as devices send in JSON_IETF) picks the module.
func (t *Tree) FindPath(p string) *Node {
	want := normPath(p)
	first := strings.SplitN(strings.TrimLeft(p, "/"), "/", 2)[0]
	mod := ""
	if i := strings.IndexByte(first, ':'); i >= 0 && !strings.Contains(first[:i], "[") {
		mod = first[:i]
	}
	var found *Node
	for _, n := range t.Nodes {
		if normPath(n.Path) != want {
			continue
		}
		if mod == "" || t.rootModule(n) == mod {
			return n
		}
		if found == nil {
			found = n
		}
	}
	return found
}

func (t *Tree) rootModule(n *Node) string {
	for n.Parent >= 0 {
		n = t.Nodes[n.Parent]
	}
	return n.Module
}

// normPath reduces a path to its element names without keys or module prefixes.
func normPath(p string) string {
	gp, err := gpath.Parse(p)
	if err != nil {
		return p
	}
	var b strings.Builder
	for _, e := range gp.Elem {
		seg := e.Name
		if i := strings.IndexByte(seg, ':'); i >= 0 {
			seg = seg[i+1:]
		}
		b.WriteString("/" + seg)
	}
	return b.String()
}

// SearchResult is a match.
type SearchResult struct {
	*Node
	Score int `json:"score"`
}

// Search finds nodes whose name (or, if q contains '/', path) contains q,
// case-insensitively. Exact name matches come first, then shallower nodes.
func (t *Tree) Search(q string, limit int) []SearchResult {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	byPath := strings.Contains(q, "/")
	var out []SearchResult
	for _, n := range t.Nodes {
		name := strings.ToLower(n.Name)
		var score int
		switch {
		case byPath && (strings.Contains(strings.ToLower(n.Path), q) || strings.Contains(strings.ToLower(stripKeys(n.Path)), q)):
			score = 2
		case name == q:
			score = 0
		case strings.HasPrefix(name, q):
			score = 1
		case strings.Contains(name, q):
			score = 2
		default:
			continue
		}
		out = append(out, SearchResult{Node: n, Score: score*1000 + strings.Count(n.Path, "/")})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score < out[j].Score
		}
		return out[i].Path < out[j].Path
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Source returns the YANG statement that defines the node: the lines from its
// location to the matching closing brace (at most maxLines).
func (t *Tree) Source(id int, maxLines int) (file string, line int, text string, err error) {
	n := t.Node(id)
	if n == nil || n.Location == "" {
		return "", 0, "", nil
	}
	file, line = splitLocation(n.Location)
	f, err := os.Open(file)
	if err != nil {
		return file, line, "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var b strings.Builder
	depth, started, ln, count := 0, false, 0, 0
	inStr := byte(0)
	for sc.Scan() {
		ln++
		if ln < line {
			continue
		}
		s := sc.Text()
		b.WriteString(s)
		b.WriteByte('\n')
		count++
		for i := 0; i < len(s); i++ {
			c := s[i]
			if inStr != 0 {
				if c == '\\' && inStr == '"' {
					i++
				} else if c == inStr {
					inStr = 0
				}
				continue
			}
			switch c {
			case '"', '\'':
				inStr = c
			case '/':
				if i+1 < len(s) && s[i+1] == '/' {
					i = len(s)
				}
			case '{':
				depth++
				started = true
			case '}':
				depth--
			case ';':
				if !started && depth == 0 {
					return file, line, b.String(), nil
				}
			}
		}
		if started && depth <= 0 {
			break
		}
		if maxLines > 0 && count >= maxLines {
			b.WriteString("...\n")
			break
		}
	}
	return file, line, b.String(), sc.Err()
}

// splitLocation splits "file:line:col" (file may contain a drive letter).
func splitLocation(loc string) (string, int) {
	parts := strings.Split(loc, ":")
	if len(parts) >= 3 {
		if l, err := strconv.Atoi(parts[len(parts)-2]); err == nil {
			return strings.Join(parts[:len(parts)-2], ":"), l
		}
	}
	if len(parts) >= 2 {
		if l, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return strings.Join(parts[:len(parts)-1], ":"), l
		}
	}
	return loc, 1
}

// stripKeys removes "[...]" key selectors from a path.
func stripKeys(p string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '[':
			depth++
		case ']':
			depth--
		default:
			if depth == 0 {
				b.WriteByte(p[i])
			}
		}
	}
	return b.String()
}
