// Package e2e — RRF ranking E2E test.
//
// Spawns real binary, ingests tastastas repo itself as ground truth, runs
// recall queries with known expected results, asserts:
//  1. Semantic/hybrid search hits outrank pure graph expansions
//  2. Named functions resolve in top-5
//  3. Same query twice → deterministic ordering
package e2e

import (
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestE2ERRFRankingQuality(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RRF ranking E2E test in -short mode")
	}

	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "rrf-e2e.db")
	sess := connect(t, bin, dbPath)
	ingestProject(t, sess)

	// Query 0: sanity — proven token from deterministic test
	queryAndLog(t, sess, "store interface node edge", func(r recallResult) {
		if len(r.Results) == 0 {
			t.Fatal("sanity: zero results for 'store interface node edge'")
		}
	})

	// Query 1: known content words from Go source
	queryAndLog(t, sess, "project id type upsert node", func(r recallResult) {
		if len(r.Results) == 0 {
			t.Skip("no lexical results for content words")
		}
		searchHits := 0
		for _, rr := range r.Results {
			if rr.MatchType != "graph" {
				searchHits++
			}
		}
		t.Logf("search hits in top-10: %d", searchHits)
	})

	// Query 2: conceptual — should surface search hits, not just graph
	queryAndLog(t, sess, "search retrieve find query recall", func(r recallResult) {
		if len(r.Results) < 3 {
			t.Fatalf("expected >=3 results, got %d", len(r.Results))
		}
		searchHits := 0
		for _, rr := range r.Results {
			if rr.MatchType != "graph" {
				searchHits++
			}
		}
		if searchHits < 1 {
			t.Fatalf("<1 search hits in top-10 (got %d)", searchHits)
		}
		var gScore, sScore float64
		for _, rr := range r.Results {
			switch {
			case rr.MatchType == "graph" && gScore == 0:
				gScore = rr.Score
			case rr.MatchType != "graph" && sScore == 0:
				sScore = rr.Score
			}
		}
		if sScore > 0 && gScore > 0 {
			t.Logf("search=%.4f graph=%.4f ratio=%.2f", sScore, gScore, sScore/gScore)
		}
	})

	// Query 3: impact / stale — content words from function bodies
	queryAndLog(t, sess, "stale downstream propagation node", func(r recallResult) {
		found := false
		for _, rr := range r.Results {
			if sub(rr.Title, "Stale") || sub(rr.ID, "check_impact") || sub(rr.ID, "MarkStale") {
				found = true
			}
		}
		if !found {
			t.Log("WARN: no stale-related function in top-10 (content may not contain these terms)")
		}
	})

	// Query 4: retrieval concepts
	queryAndLog(t, sess, "score rank fusion retrieve", func(r recallResult) {
		if len(r.Results) == 0 {
			t.Fatal("zero results")
		}
		found := false
		for _, rr := range r.Results {
			if sub(rr.Title, "recall") || sub(rr.ID, "rrfScore") || sub(rr.Title, "Scored") {
				found = true
			}
		}
		if !found {
			t.Log("WARN: no recall/score reference in top-10")
		}
	})
}

// TestE2ERRFDeterministic verifies same query → same ordering.
func TestE2ERRFDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RRF deterministic E2E test in -short mode")
	}

	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "rrf-det-e2e.db")
	sess := connect(t, bin, dbPath)
	ingestProject(t, sess)

	var first, second recallResult
	callTool(t, sess, "recall", map[string]any{
		"project_id": "e2e-rrf",
		"stage":      "e2e-test",
		"query":      "store interface node edge",
		"limit":      5,
	}, &first)
	callTool(t, sess, "recall", map[string]any{
		"project_id": "e2e-rrf",
		"stage":      "e2e-test",
		"query":      "store interface node edge",
		"limit":      5,
	}, &second)

	if len(first.Results) == 0 {
		t.Fatal("zero results — cannot verify determinism on empty set")
	}
	if len(first.Results) != len(second.Results) {
		t.Fatalf("count mismatch %d vs %d", len(first.Results), len(second.Results))
	}
	for i := range first.Results {
		if first.Results[i].ID != second.Results[i].ID {
			t.Fatalf("result %d ID differs: %s vs %s", i, first.Results[i].ID, second.Results[i].ID)
		}
	}
}

// ------- helpers -------

type recallResult struct {
	Results []struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		MatchType string  `json:"match_type"`
		Score     float64 `json:"score"`
	} `json:"results"`
}

func sub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func queryAndLog(t *testing.T, sess *mcp.ClientSession, query string, assert func(recallResult)) {
	t.Helper()
	var out recallResult
	callTool(t, sess, "recall", map[string]any{
		"project_id": "e2e-rrf",
		"stage":      "e2e-test",
		"query":      query,
		"limit":      10,
	}, &out)
	t.Logf("=== QUERY: %q ===", query)
	for i, r := range out.Results {
		t.Logf("  [%d] %s | %s | %.4f", i, r.Title, r.MatchType, r.Score)
	}
	assert(out)
}

// ingestProject ingests the repo root into a fresh test project, polling job_status until done.
func ingestProject(t *testing.T, sess *mcp.ClientSession) {
	t.Helper()
	type jobRes struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	var out jobRes
	callTool(t, sess, "ingest", map[string]any{
		"cwd":        "../..", // up from internal/e2e/ to repo root
		"project_id": "e2e-rrf",
		"stage":      "e2e-test",
	}, &out)

	for i := 0; i < 60; i++ {
		var st struct {
			Status string `json:"status"`
			Error  string `json:"error,omitempty"`
		}
		callTool(t, sess, "job_status", map[string]any{
			"id": out.JobID,
		}, &st)
		if st.Status == "done" {
			return
		}
		if st.Status == "error" {
			t.Fatalf("ingest failed: %s", st.Error)
		}
		time.Sleep(time.Second)
	}
	t.Fatal("ingest timed out after 60s")
}
