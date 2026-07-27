package e2e

import (
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

func TestE2EFullWorkspaceRecall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full workspace recall test in -short mode")
	}

	bin := buildBinary(t)
	sess := connect(t, bin, "/tmp/full-workspace.db")

	var recallOut struct {
		Results []struct {
			ID        string  `json:"id"`
			Title     string  `json:"title"`
			MatchType string  `json:"match_type"`
			Score     float64 `json:"score"`
		} `json:"results"`
		Links []struct {
			FromID   string  `json:"from_id"`
			ToID     string  `json:"to_id"`
			EdgeType string  `json:"edge_type"`
			Score    float64 `json:"score"`
		} `json:"links"`
	}

	// Query 1: tastastas sidecar
	callTool(t, sess, "recall", map[string]any{
		"project_id": "full-workspace",
		"query":      "tastastas sidecar pool embedder",
		"limit":      5,
	}, &recallOut)

	t.Logf("=== QUERY: 'tastastas sidecar pool embedder' ===")
	t.Logf("Results: %d", len(recallOut.Results))
	for i, r := range recallOut.Results {
		t.Logf("  [%d] ID=%s | Title=%s | Type=%s | Score=%.4f", i, r.ID, r.Title, r.MatchType, r.Score)
	}
	if len(recallOut.Results) == 0 {
		t.Fatal("FAIL: zero results for 'tastastas sidecar pool embedder'")
	}

	// Query 2: agentic memory
	callTool(t, sess, "recall", map[string]any{
		"project_id": "full-workspace",
		"query":      "agentic memory",
		"limit":      5,
	}, &recallOut)

	t.Logf("=== QUERY: 'agentic memory' ===")
	t.Logf("Results: %d", len(recallOut.Results))
	for i, r := range recallOut.Results {
		t.Logf("  [%d] ID=%s | Title=%s | Type=%s | Score=%.4f", i, r.ID, r.Title, r.MatchType, r.Score)
	}

	// Query 3: something specific from tastastas project
	callTool(t, sess, "recall", map[string]any{
		"project_id": "full-workspace",
		"query":      "sqlite vec bge embedding",
		"limit":      5,
	}, &recallOut)

	t.Logf("=== QUERY: 'sqlite vec bge embedding' ===")
	t.Logf("Results: %d", len(recallOut.Results))
	for i, r := range recallOut.Results {
		t.Logf("  [%d] ID=%s | Title=%s | Type=%s | Score=%.4f", i, r.ID, r.Title, r.MatchType, r.Score)
	}
}
