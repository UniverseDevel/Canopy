// Package catalog is a local index of the YANG modules in model sets: what each
// release contains, searchable by module name, prefix, namespace and
// description. It is built lazily: a downloaded set is indexed in full (parsed
// headers), and a release that was only listed is indexed from its file names.
// Storage is SQLite (pure Go, no CGo) with full-text search.
package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openconfig/goyang/pkg/yang"
	_ "modernc.org/sqlite"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// Entry is one YANG file of a release.
type Entry struct {
	Source      string   `json:"source"`
	Version     string   `json:"version"`
	Path        string   `json:"path"`
	Module      string   `json:"module"`
	Kind        string   `json:"kind,omitempty"` // module or submodule; empty when only listed
	Revision    string   `json:"revision,omitempty"`
	Namespace   string   `json:"namespace,omitempty"`
	Prefix      string   `json:"prefix,omitempty"`
	OCVersion   string   `json:"ocVersion,omitempty"`
	BelongsTo   string   `json:"belongsTo,omitempty"`
	Description string   `json:"description,omitempty"`
	Imports     []string `json:"imports,omitempty"`
	Deviates    []string `json:"deviates,omitempty"` // modules this one deviates
	Enriched    bool     `json:"enriched"`           // parsed from the file, not only the name
}

// Release is an indexed model set.
type Release struct {
	Source    string    `json:"source"`
	Version   string    `json:"version"`
	Files     int       `json:"files"`
	Enriched  bool      `json:"enriched"`
	IndexedAt time.Time `json:"indexedAt"`
}

// Catalog is the index.
type Catalog struct {
	db *sql.DB
	mu sync.Mutex // writes
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS releases(
  source TEXT NOT NULL, version TEXT NOT NULL, files INTEGER NOT NULL, enriched INTEGER NOT NULL,
  indexed_at TEXT NOT NULL, PRIMARY KEY(source, version));
CREATE TABLE IF NOT EXISTS files(
  id INTEGER PRIMARY KEY, source TEXT NOT NULL, version TEXT NOT NULL, path TEXT NOT NULL,
  module TEXT NOT NULL, kind TEXT, revision TEXT, namespace TEXT, prefix TEXT, ocversion TEXT,
  belongs TEXT, description TEXT, imports TEXT, deviates TEXT, enriched INTEGER NOT NULL,
  UNIQUE(source, version, path));
CREATE INDEX IF NOT EXISTS files_module ON files(module);
CREATE VIRTUAL TABLE IF NOT EXISTS files_fts USING fts5(module, prefix, namespace, description, content='files', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS files_ai AFTER INSERT ON files BEGIN
  INSERT INTO files_fts(rowid, module, prefix, namespace, description) VALUES (new.id, new.module, new.prefix, new.namespace, new.description);
END;
CREATE TRIGGER IF NOT EXISTS files_ad AFTER DELETE ON files BEGIN
  INSERT INTO files_fts(files_fts, rowid, module, prefix, namespace, description) VALUES ('delete', old.id, old.module, old.prefix, old.namespace, old.description);
END;
`

// DefaultPath is <user cache dir>/canopy/catalog.db (or $CANOPY_CACHE_DIR).
func DefaultPath() string {
	base := os.Getenv("CANOPY_CACHE_DIR")
	if base == "" {
		if d, err := os.UserCacheDir(); err == nil {
			base = filepath.Join(d, "canopy")
		} else {
			base = ".canopy-cache"
		}
	}
	return filepath.Join(base, "catalog.db")
}

// Open opens (or creates) the catalog.
func Open(path string) (*Catalog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, fmt.Errorf("catalog %s: %w", path, err))
	}
	return &Catalog{db: db}, nil
}

// Close closes the database.
func (c *Catalog) Close() error { return c.db.Close() }

// Release returns an indexed release, or nil.
func (c *Catalog) Release(source, version string) (*Release, error) {
	r := Release{Source: source, Version: version}
	var at string
	err := c.db.QueryRow(`SELECT files, enriched, indexed_at FROM releases WHERE source=? AND version=?`, source, version).Scan(&r.Files, &r.Enriched, &at)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, dbErr(err)
	}
	r.IndexedAt, _ = time.Parse(time.RFC3339, at)
	return &r, nil
}

// Releases lists the indexed releases.
func (c *Catalog) Releases() ([]Release, error) {
	rows, err := c.db.Query(`SELECT source, version, files, enriched, indexed_at FROM releases ORDER BY source, version`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		var r Release
		var at string
		if err := rows.Scan(&r.Source, &r.Version, &r.Files, &r.Enriched, &at); err != nil {
			return nil, dbErr(err)
		}
		r.IndexedAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// IndexDirs indexes a downloaded release from its folders (every .yang file,
// headers parsed). It replaces what was indexed for the release before.
func (c *Catalog) IndexDirs(ctx context.Context, source, version string, dirs []string) (int, error) {
	var entries []Entry
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() || !strings.HasSuffix(p, ".yang") {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rel, _ := filepath.Rel(d, p)
			e := Entry{Source: source, Version: version, Path: filepath.ToSlash(filepath.Join(filepath.Base(d), rel))}
			b, err := os.ReadFile(p)
			if err == nil {
				parseHeader(string(b), p, &e)
			}
			if e.Module == "" {
				e.Module, e.Revision = fromName(filepath.Base(p))
			}
			entries = append(entries, e)
			return nil
		})
		if err != nil {
			return 0, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
		}
	}
	return len(entries), c.replace(source, version, entries, true)
}

// IndexNames indexes a release from a listing of its file paths (module and
// revision from the file names only).
func (c *Catalog) IndexNames(source, version string, paths []string) error {
	var entries []Entry
	for _, p := range paths {
		if !strings.HasSuffix(p, ".yang") {
			continue
		}
		e := Entry{Source: source, Version: version, Path: p}
		e.Module, e.Revision = fromName(p[strings.LastIndex(p, "/")+1:])
		entries = append(entries, e)
	}
	return c.replace(source, version, entries, false)
}

func (c *Catalog) replace(source, version string, entries []Entry, enriched bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.db.Begin()
	if err != nil {
		return dbErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM files WHERE source=? AND version=?`, source, version); err != nil {
		return dbErr(err)
	}
	st, err := tx.Prepare(`INSERT OR REPLACE INTO files(source, version, path, module, kind, revision, namespace, prefix, ocversion, belongs, description, imports, deviates, enriched)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return dbErr(err)
	}
	defer st.Close()
	for _, e := range entries {
		if _, err := st.Exec(e.Source, e.Version, e.Path, e.Module, e.Kind, e.Revision, e.Namespace, e.Prefix, e.OCVersion, e.BelongsTo,
			e.Description, strings.Join(e.Imports, " "), strings.Join(e.Deviates, " "), e.Enriched); err != nil {
			return dbErr(err)
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO releases(source, version, files, enriched, indexed_at) VALUES(?,?,?,?,?)`,
		source, version, len(entries), enriched, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return dbErr(err)
	}
	return dbErr(tx.Commit())
}

// Query filters a search.
type Query struct {
	Text    string `json:"q"`       // words matched against module, prefix, namespace, description
	Source  string `json:"source"`  // optional
	Version string `json:"version"` // optional
	Module  string `json:"module"`  // exact module name (optional)
	Limit   int    `json:"limit"`
}

// Search finds modules. Words match prefixes of the words in module names,
// prefixes, namespaces and descriptions ("oc interf" finds
// openconfig-interfaces).
func (c *Catalog) Search(ctx context.Context, q Query) ([]Entry, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}
	where := []string{"1=1"}
	var args []any
	from, order := "files f", "length(f.module), f.module, f.source, f.version DESC"
	if m := ftsQuery(q.Text); m != "" {
		// module names weigh most, then prefixes, namespaces, descriptions
		from = "files f JOIN files_fts ON files_fts.rowid = f.id"
		where = append(where, "files_fts MATCH ?")
		order = "bm25(files_fts, 50.0, 20.0, 5.0, 1.0), length(f.module), f.source, f.version DESC"
		args = append(args, m)
	} else if strings.TrimSpace(q.Text) != "" {
		return nil, nil
	}
	if q.Source != "" {
		where = append(where, "f.source=?")
		args = append(args, q.Source)
	}
	if q.Version != "" {
		where = append(where, "f.version=?")
		args = append(args, q.Version)
	}
	if q.Module != "" {
		where = append(where, "f.module=?")
		args = append(args, q.Module)
	}
	args = append(args, q.Limit)
	rows, err := c.db.QueryContext(ctx, `SELECT f.source, f.version, f.path, f.module, f.kind, f.revision, f.namespace, f.prefix, f.ocversion, f.belongs, f.description, f.imports, f.deviates, f.enriched
FROM `+from+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var kind, rev, ns, pfx, ocv, bel, desc, imp, dev sql.NullString
		if err := rows.Scan(&e.Source, &e.Version, &e.Path, &e.Module, &kind, &rev, &ns, &pfx, &ocv, &bel, &desc, &imp, &dev, &e.Enriched); err != nil {
			return nil, dbErr(err)
		}
		e.Kind, e.Revision, e.Namespace, e.Prefix, e.OCVersion, e.BelongsTo, e.Description = kind.String, rev.String, ns.String, pfx.String, ocv.String, bel.String, desc.String
		e.Imports, e.Deviates = strings.Fields(imp.String), strings.Fields(dev.String)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Containing lists the indexed releases that contain a module, with its
// revision there.
func (c *Catalog) Containing(ctx context.Context, module string) ([]Entry, error) {
	return c.Search(ctx, Query{Module: module, Limit: 1000})
}

// ftsQuery turns words into an FTS5 query: every word must match the start of a
// token.
func ftsQuery(s string) string {
	var parts []string
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		parts = append(parts, `"`+w+`"*`)
	}
	return strings.Join(parts, " AND ")
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

var nameRe = regexp.MustCompile(`^(.+?)(?:@(\d{4}-\d{2}-\d{2}))?\.yang$`)

// fromName parses "module@2024-01-01.yang".
func fromName(base string) (string, string) {
	if m := nameRe.FindStringSubmatch(base); m != nil {
		return m[1], m[2]
	}
	return strings.TrimSuffix(base, ".yang"), ""
}

// parseHeader reads the top-level statements of a module.
func parseHeader(text, path string, e *Entry) {
	sts, err := yang.Parse(text, path)
	if err != nil || len(sts) == 0 {
		return
	}
	top := sts[0]
	if top.Keyword != "module" && top.Keyword != "submodule" {
		return
	}
	e.Module, e.Kind, e.Enriched = top.Argument, top.Keyword, true
	for _, s := range top.SubStatements() {
		switch s.Keyword {
		case "namespace":
			e.Namespace = s.Argument
		case "prefix":
			e.Prefix = s.Argument
		case "belongs-to":
			e.BelongsTo = s.Argument
			for _, x := range s.SubStatements() {
				if x.Keyword == "prefix" {
					e.Prefix = x.Argument
				}
			}
		case "revision":
			if s.Argument > e.Revision {
				e.Revision = s.Argument
			}
		case "description":
			if e.Description == "" {
				e.Description = firstSentence(s.Argument)
			}
		case "import", "include":
			e.Imports = append(e.Imports, s.Argument)
		case "deviation":
			if mod := deviated(s.Argument, top); mod != "" && !contains(e.Deviates, mod) {
				e.Deviates = append(e.Deviates, mod)
			}
		default:
			if strings.HasSuffix(s.Keyword, ":openconfig-version") {
				e.OCVersion = s.Argument
			}
		}
	}
}

// deviated maps the prefix of a deviation target to the imported module.
func deviated(target string, top *yang.Statement) string {
	seg := strings.TrimPrefix(target, "/")
	if i := strings.IndexAny(seg, "/["); i >= 0 {
		seg = seg[:i]
	}
	pfx, _, ok := strings.Cut(seg, ":")
	if !ok {
		return ""
	}
	for _, s := range top.SubStatements() {
		if s.Keyword != "import" {
			continue
		}
		for _, x := range s.SubStatements() {
			if x.Keyword == "prefix" && x.Argument == pfx {
				return s.Argument
			}
		}
	}
	return ""
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 && i < 300 {
		return s[:i+1]
	}
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func dbErr(err error) error {
	if err == nil {
		return nil
	}
	return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, fmt.Errorf("catalog: %w", err))
}
