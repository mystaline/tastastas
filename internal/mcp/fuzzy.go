package mcp

import (
	"context"
	"fmt"
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

// guardProjectID rejects a write when project_id is a near-miss of an
// existing project (fuzzy match, not exact) — same fuzzy logic recall
// uses for its "Did you mean" warning, but blocking instead of advisory,
// since a write silently landing in the wrong project is data loss.
// Returns nil (proceed) when project_id exactly matches an existing
// project, or when there is no fuzzy match at all (genuinely new project).
// Returns a non-nil error, with candidate suggestions, when there is a
// fuzzy match but no exact match.
func guardProjectID(ctx context.Context, db store.Store, projectID string) error {
	if projectID == "" || projectID == "default" {
		return nil // documented no-project-given fallback, not a typo
	}
	projects, err := db.ListProjects(ctx)
	if err != nil {
		return nil // don't block writes on a listing failure
	}
	for _, p := range projects {
		if p.ProjectID == projectID || p.EffectiveProjectID == projectID {
			return nil
		}
	}
	suggestions := fuzzyMatchProjects(projectID, projects)
	if len(suggestions) == 0 {
		return nil
	}
	return fmt.Errorf(
		"project_id '%s' does not exactly match an existing project. Did you mean: %s? "+
			"(pass the exact id to write to that project, or a clearly different id to create a new one)",
		projectID, formatSuggestions(suggestions))
}
