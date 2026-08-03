package e2e

import (
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

func TestE2EFullWorkspaceRecall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full workspace recall test in -short mode")
	}

	bin := buildBinary(t)
	// Fresh DB per run. This test used to depend on a pre-populated
	// /tmp/full-workspace.db that nothing created anymore — on a clean
	// machine it came up empty and recall returned zero results. It now
	// ingests the workspace itself.
	sess := connectWithTimeout(t, bin, filepath.Join(t.TempDir(), "full-workspace.db"), 10*time.Minute)

	// Ingest the whole repo (this package lives at internal/e2e, two levels
	// below the repo root). connect spawns the binary with -embed-backend
	// none, so ingest skips chunking/embedding (fast) and recall below runs
	// in pure lexical (FTS5) mode.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root abs: %v", err)
	}

	var ingestOut struct {
		JobID string `json:"job_id"`
	}
	callTool(t, sess, "ingest", map[string]any{
		"cwd":        root,
		"project_id": "full-workspace",
		"stage":      "e2e-test",
	}, &ingestOut)
	if ingestOut.JobID == "" {
		t.Fatalf("ingest: expected job_id, got %+v", ingestOut)
	}

	var jobOut struct {
		Status string `json:"status"`
		Nodes  int    `json:"nodes_ingested"`
		Error  string `json:"error"`
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		callTool(t, sess, "job_status", map[string]any{"id": ingestOut.JobID}, &jobOut)
		if jobOut.Status == "done" || jobOut.Status == "error" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if jobOut.Status != "done" {
		t.Fatalf("ingest job did not complete: %+v", jobOut)
	}
	if jobOut.Nodes == 0 {
		t.Fatalf("ingest job: expected nodes_ingested > 0, got %+v", jobOut)
	}

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

	queries := []string{
		"tastastas sidecar pool embedder",
		"agentic memory",
		"sqlite vec bge embedding",
	}
	for _, q := range queries {
		callTool(t, sess, "recall", map[string]any{
			"project_id": "full-workspace",
			"stage":      "e2e-test",
			"query":      q,
			"limit":      5,
		}, &recallOut)

		t.Logf("=== QUERY: %q ===", q)
		t.Logf("Results: %d", len(recallOut.Results))
		for i, r := range recallOut.Results {
			t.Logf("  [%d] ID=%s | Title=%s | Type=%s | Score=%.4f", i, r.ID, r.Title, r.MatchType, r.Score)
		}
		if len(recallOut.Results) == 0 {
			t.Fatalf("FAIL: zero results for %q", q)
		}
	}
}
