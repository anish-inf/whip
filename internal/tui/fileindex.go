package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// fileIndexTTL is how long a cached recursive file listing is reused.
// Rebuilding walks the tree, so completion must not do it per keystroke.
const fileIndexTTL = 2 * time.Second

// fileIndex caches one recursive listing per root directory (os.Getwd in
// production, the model's working dir in tests) so @mention fuzzy completion
// never re-walks the tree on a keystroke.
var fileIndex struct {
	sync.Mutex
	builtAt time.Time
	root    string
	files   []string // slash-separated, relative to root
}

// currentRoot reports the directory fuzzy @mentions search. The TUI runs from
// the repo root, but tests chdir into fixture trees, so the model carries the
// effective root; the fallback keeps the bare completion helpers testable.
var currentRoot = os.Getwd

// refreshFileIndex rebuilds the recursive listing if it is stale or the root
// changed. Skips hidden dirs (.git, .agents) and heavy dependency dirs
// (vendor, node_modules) so the index stays small and the walk stays fast.
func refreshFileIndex() {
	fileIndex.Lock()
	defer fileIndex.Unlock()
	wd, err := currentRoot()
	if err != nil {
		return
	}
	if wd == fileIndex.root && time.Since(fileIndex.builtAt) < fileIndexTTL {
		return
	}
	var files []string
	_ = filepath.WalkDir(wd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entry; keep going
		}
		if path == wd {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(wd, path)
		if err != nil {
			return nil //nolint:nilerr // path came from walking wd; skip it rather than abort the walk
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	fileIndex.root, fileIndex.files, fileIndex.builtAt = wd, files, time.Now()
}

// fuzzyFiles returns up to limit files from the index matching query, best
// first. An empty query lists every indexed file (sorted, so results are
// stable). Match quality prefers the query as a contiguous substring (base
// name first, then path), then subsequence matches (base name, then path).
// The result cap keeps per-keystroke scoring bounded; it only binds on
// pathological queries in huge trees, where the top-ranked matches win anyway.
func fuzzyFiles(query string, limit int) []string {
	refreshFileIndex()
	fileIndex.Lock()
	files := append([]string(nil), fileIndex.files...)
	fileIndex.Unlock()

	q := strings.ToLower(query)
	type hit struct {
		f    string
		tier int
	}
	var hits []hit
	for _, f := range files {
		tier := matchTier(f, q)
		if tier < 0 {
			continue
		}
		hits = append(hits, hit{f, tier})
		if q != "" && len(hits) >= limit {
			break // rough cut; the partial resort below ranks what we kept
		}
	}
	if q != "" {
		sort.SliceStable(hits, func(a, b int) bool {
			if hits[a].tier != hits[b].tier {
				return hits[a].tier < hits[b].tier
			}
			return hits[a].f < hits[b].f
		})
	} else {
		sort.Strings(files)
		hits = hits[:0]
		for _, f := range files {
			hits = append(hits, hit{f, 0})
		}
	}
	out := make([]string, 0, min(len(hits), limit))
	for _, h := range hits {
		out = append(out, h.f)
		if len(out) == limit {
			break
		}
	}
	return out
}

// matchTier grades how well q matches file f (both compared lowercase):
//
//	0: q is a substring of the base name
//	1: q is a substring of the full path
//	2: q is a subsequence of the base name
//	3: q is a subsequence of the full path
//	-1: no match
func matchTier(f, q string) int {
	if q == "" {
		return 0
	}
	lf := strings.ToLower(f)
	base := lf[strings.LastIndexByte(lf, '/')+1:]
	if strings.Contains(base, q) {
		return 0
	}
	if strings.Contains(lf, q) {
		return 1
	}
	if subseq(base, q) {
		return 2
	}
	if subseq(lf, q) {
		return 3
	}
	return -1
}

// subseq reports whether every rune of q appears in s, in order.
func subseq(s, q string) bool {
	for _, r := range q {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+1:]
	}
	return true
}

// resolveMentionPath turns an @mention token into an absolute path. Real
// paths (relative, absolute, ~) stat as-is; a bare word like "roadmap"
// falls back to the fuzzy index when exactly one file matches.
func resolveMentionPath(p string) (string, bool) {
	abs := p
	if abs == "~" || strings.HasPrefix(abs, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			abs = home + abs[1:]
		}
	}
	if !filepath.IsAbs(abs) {
		if wd, err := os.Getwd(); err == nil {
			abs = filepath.Join(wd, abs)
		}
	}
	if _, err := os.Stat(abs); err == nil {
		return abs, true
	}
	// Not a literal path: try a unique fuzzy match against the index, but
	// only for bare words (no separators) so partial paths stay untouched.
	if !strings.ContainsAny(p, "/\\") {
		if hits := fuzzyFiles(p, 2); len(hits) == 1 {
			if wd, err := currentRoot(); err == nil {
				return filepath.Join(wd, filepath.FromSlash(hits[0])), true
			}
		}
	}
	return "", false
}
