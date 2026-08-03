package mcp

import (
	"sort"
	"strings"

	"github.com/mystaline/tastastas/internal/store"
)

// fuzzyMatchProjects returns projects that fuzzy-match the query.
// Matching: case-insensitive substring containment (both directions) or edit
// distance ≤ 2, tested against base ProjectID, decoded Stage, and
// EffectiveProjectID — keeping the best (lowest) score per project.
// Results are sorted: substring matches first, then by edit distance
// ascending; ties broken by EffectiveProjectID for stability.
func fuzzyMatchProjects(query string, projects []store.ProjectInfo) []store.ProjectInfo {
	if query == "" {
		return nil
	}
	type candidate struct {
		info     store.ProjectInfo
		isSubstr bool
		dist     int
	}
	q := strings.ToLower(query)
	var candidates []candidate

	for _, p := range projects {
		fields := []string{p.ProjectID, p.ProjectName, p.Stage, p.EffectiveProjectID}
		bestSubstr := false
		bestDist := -1
		for _, f := range fields {
			if f == "" {
				continue
			}
			low := strings.ToLower(f)
			if strings.Contains(low, q) || strings.Contains(q, low) {
				bestSubstr = true
				bestDist = 0
				break
			}
			d := editDistance(q, low)
			if d <= 2 && (bestDist == -1 || d < bestDist) {
				bestDist = d
			}
		}
		if bestSubstr || bestDist >= 0 {
			candidates = append(candidates, candidate{info: p, isSubstr: bestSubstr, dist: bestDist})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		ci, cj := candidates[i], candidates[j]
		if ci.isSubstr != cj.isSubstr {
			return ci.isSubstr
		}
		if ci.dist != cj.dist {
			return ci.dist < cj.dist
		}
		return ci.info.EffectiveProjectID < cj.info.EffectiveProjectID
	})

	result := make([]store.ProjectInfo, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, c.info)
	}
	return result
}

// editDistance computes Levenshtein distance between two strings.
// O(n*m) time and space, fine for short strings (project IDs).
func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Optimize: only keep two rows
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}
