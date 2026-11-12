package models

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// github talks to the GitHub REST API (listings) and to raw/codeload hosts
// (content, not rate limited like the API). GITHUB_TOKEN is used when set.
type github struct {
	http  *http.Client
	token string
	api   string
	raw   string
	load  string
}

func newGitHub() *github {
	return &github{
		http:  &http.Client{Timeout: 5 * time.Minute},
		token: os.Getenv("GITHUB_TOKEN"),
		api:   "https://api.github.com",
		raw:   "https://raw.githubusercontent.com",
		load:  "https://codeload.github.com",
	}
}

func (g *github) get(ctx context.Context, u string, api bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "canopy")
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	if g.token != "" && (api || strings.HasPrefix(u, g.raw)) {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, fmt.Errorf("GitHub: %w", err)).
			WithHint("model download needs internet access to github.com (or use a local YANG folder)")
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	e := apperr.New(apperr.ClassSchema, apperr.CodeSchema, "GitHub %s: %s", resp.Status, u)
	if resp.StatusCode == http.StatusNotFound {
		e.Code = apperr.CodeNotFound
	}
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
		(resp.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(string(body), "rate limit")) {
		e.Message = "GitHub API rate limit reached"
		e.Hint = "set GITHUB_TOKEN (a token without scopes is enough) or try again later; downloaded models stay cached"
	}
	return nil, e
}

func (g *github) getJSON(ctx context.Context, u string, v any) error {
	resp, err := g.get(ctx, u, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return apperr.Wrap(apperr.ClassSchema, apperr.CodeSchema, fmt.Errorf("GitHub response %s: %w", u, err))
	}
	return nil
}

// tags returns the repository's tag names (newest pages first, up to 500).
func (g *github) tags(ctx context.Context, repo string) ([]string, error) {
	var out []string
	for page := 1; page <= 5; page++ {
		var batch []struct {
			Name string `json:"name"`
		}
		if err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/tags?per_page=100&page=%d", g.api, repo, page), &batch); err != nil {
			return nil, err
		}
		for _, t := range batch {
			out = append(out, t.Name)
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // dir, file, submodule, symlink
	SHA  string `json:"sha"`
}

// listDir lists one folder at ref.
func (g *github) listDir(ctx context.Context, repo, ref, dir string) ([]dirEntry, error) {
	var out []dirEntry
	u := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", g.api, repo, escapePath(dir), url.QueryEscape(ref))
	err := g.getJSON(ctx, u, &out)
	return out, err
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob, tree, commit
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// tree lists a tree recursively.
func (g *github) tree(ctx context.Context, repo, sha string) ([]treeEntry, error) {
	var out struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := g.getJSON(ctx, fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", g.api, repo, sha), &out); err != nil {
		return nil, err
	}
	if out.Truncated {
		return nil, apperr.New(apperr.ClassSchema, apperr.CodeSchema, "GitHub tree listing of %s is truncated (folder too large)", repo)
	}
	return out.Tree, nil
}

// downloadFolder downloads the .yang files of the folder dir (tree sha) into dest.
func (g *github) downloadFolder(ctx context.Context, repo, ref, dir, sha, dest string, progress func(string)) (int, error) {
	entries, err := g.tree(ctx, repo, sha)
	if err != nil {
		return 0, err
	}
	var files []treeEntry
	for _, e := range entries {
		if e.Type == "blob" && strings.HasSuffix(e.Path, ".yang") {
			files = append(files, e)
		}
	}
	if progress != nil {
		progress(fmt.Sprintf("downloading %d files from %s/%s", len(files), repo, dir))
	}
	jobs := make(chan treeEntry)
	errs := make(chan error, len(files))
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := g.downloadFile(ctx, repo, ref, path.Join(dir, f.Path), filepath.Join(dest, filepath.FromSlash(f.Path))); err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return 0, err
	}
	return len(files), nil
}

func (g *github) downloadFile(ctx context.Context, repo, ref, p, dest string) error {
	if !safeRel(p) {
		return fmt.Errorf("unsafe path %q", p)
	}
	resp, err := g.get(ctx, fmt.Sprintf("%s/%s/%s/%s", g.raw, repo, ref, escapePath(p)), false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeFile(dest, io.LimitReader(resp.Body, 32<<20))
}

// extractTarball downloads repo@tag and extracts the .yang files under the given
// prefixes (relative to the archive's top folder) into dest.
func (g *github) extractTarball(ctx context.Context, repo, tag string, prefixes []string, dest string) (int, error) {
	resp, err := g.get(ctx, fmt.Sprintf("%s/%s/tar.gz/refs/tags/%s", g.load, repo, url.PathEscape(tag)), false)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return 0, apperr.Wrap(apperr.ClassSchema, apperr.CodeSchema, err)
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, apperr.Wrap(apperr.ClassSchema, apperr.CodeSchema, err)
		}
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(h.Name, ".yang") {
			continue // regular .yang files only; links are ignored
		}
		_, rel, ok := strings.Cut(h.Name, "/") // strip "<repo>-<tag>/"
		if !ok || !safeRel(rel) || !hasPrefix(rel, prefixes) {
			continue
		}
		if err := writeFile(filepath.Join(dest, filepath.FromSlash(rel)), io.LimitReader(tr, 32<<20)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func hasPrefix(p string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, x := range prefixes {
		if p == x || strings.HasPrefix(p, strings.TrimSuffix(x, "/")+"/") {
			return true
		}
	}
	return false
}

// safeRel rejects absolute paths and parent references (archive contents are
// untrusted).
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, ":") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return false
		}
	}
	return true
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func writeFile(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
