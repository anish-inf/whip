package tools

import (
	"sort"
	"strings"
)

// SuggestTool returns the closest live MCP tool names for an unknown one —
// the "did you mean?" that turns a stale or typo'd call (a disconnect
// mid-session leaves old names in the conversation) into a self-correcting
// turn instead of a dead one. Returns at most 2 candidates, best first;
// empty when nothing is close enough to be useful.
func SuggestTool(name string, candidates []string) []string {
	type scored struct {
		name string
		dist int
	}
	var hits []scored
	for _, c := range candidates {
		prefix := strings.HasPrefix(c, name) || strings.HasPrefix(name, c)
		d := levenshtein(name, c, 4) // early-exit cap: suggestions stop mattering past a few edits
		if prefix {
			d = -len(c) // prefix matches rank first, shorter (more general) first
		} else if d > 3 {
			continue
		}
		hits = append(hits, scored{c, d})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].dist != hits[j].dist {
			return hits[i].dist < hits[j].dist
		}
		return hits[i].name < hits[j].name
	})
	var out []string
	for _, h := range hits {
		if len(out) == 2 {
			break
		}
		out = append(out, h.name)
	}
	return out
}

// levenshtein computes the edit distance between a and b, stopping early once
// the distance provably exceeds maxDist (returns maxDist+1). Tool names are
// short and
// ASCII-heavy; a byte-wise DP is plenty.
func levenshtein(a, b string, maxDist int) int {
	if a == b {
		return 0
	}
	if d := len(a) - len(b); d > maxDist || -d > maxDist {
		return maxDist + 1
	}
	// prev[j] = edit distance between a[:i] and b[:j].
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > maxDist {
			return maxDist + 1
		}
		prev = cur
	}
	return prev[len(b)]
}

func min3(a, b, c int) int { return min(min(a, b), c) }
