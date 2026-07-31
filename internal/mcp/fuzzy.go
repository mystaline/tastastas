package mcp

import (
	"strings"

	"github.com/mystaline/tastastas/internal/store"
)

// fuzzyMatchProjects returns project_ids from projects that fuzzy-match the query.
// Matching: case-insensitive substring containment (both directions) or edit distance ≤ 2.
// Results are sorted: substring matches first, then by edit distance ascending.
func fuzzyMatchProjects(query string, projects []store.ProjectInfo) []string {
	if query == "" {
		return nil
	}
	type candidate struct {
		id   string
		dist int
	}
	var subs, dists []candidate
	q := strings.ToLower(query)

	for _, p := range projects {
		id := p.ProjectID
		low := strings.ToLower(id)
		if strings.Contains(low, q) || strings.Contains(q, low) {
			subs = append(subs, candidate{id: id, dist: 0})
			continue
		}
		d := editDistance(q, low)
		if d <= 2 {
			dists = append(dists, candidate{id: id, dist: d})
		}
	}

	// Sort dists by edit distance ascending
	for i := 0; i < len(dists); i++ {
		for j := i + 1; j < len(dists); j++ {
			if dists[j].dist < dists[i].dist {
				dists[i], dists[j] = dists[j], dists[i]
			}
		}
	}

	result := make([]string, 0, len(subs)+len(dists))
	for _, c := range subs {
		result = append(result, c.id)
	}
	for _, c := range dists {
		result = append(result, c.id)
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
