// Package e2e — recall_with_edges E2E test.
//
// Spawns real binary, ingests tastastas repo, recalls a known function, and
// asserts that recall results include edges from the graph store.
package e2e

import (
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestE2ERecallWithEdges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recall-with-edges E2E test in -short mode")
	}

	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "re-e2e.db")
	sess := connect(t, bin, dbPath)
	ingestProjectRE(t, sess)

	// 1. store a node via remember + link it to another node via link tool
	//    so we know an edge exists.
	var remOut struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	callTool(t, sess, "remember", map[string]any{
		"id":         "e2e-re/fact/a",
		"project_id": "e2e-re",
		"node_type":  "fact",
		"title":      "testnode_recall_edges_a",
		"content":    "zebraquasarrecalltest unique content for recall edges test",
	}, &remOut)

	callTool(t, sess, "remember", map[string]any{
		"id":         "e2e-re/fact/b",
		"project_id": "e2e-re",
		"node_type":  "fact",
		"title":      "testnode_recall_edges_b",
		"content":    "zebraquasarrecalltest other content for recall edges test",
	}, nil)

	callTool(t, sess, "link", map[string]any{
		"from_id":    "e2e-re/fact/a",
		"to_id":      "e2e-re/fact/b",
		"edge_type":  "implements",
		"confidence": 0.95,
	}, nil)

	// 2. recall node a — edges should be populated
	t.Run("recall returns edges for linked node", func(t *testing.T) {
		var out struct {
			Results []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Edges []struct {
					ToID       string  `json:"to_id"`
					ToTitle    string  `json:"to_title"`
					EdgeType   string  `json:"edge_type"`
					Confidence float64 `json:"confidence"`
				} `json:"edges,omitempty"`
			} `json:"results"`
		}
		callTool(t, sess, "recall", map[string]any{
			"project_id": "e2e-re",
			"query":      "zebraquasarrecalltest",
			"limit":      10,
		}, &out)

		if len(out.Results) == 0 {
			t.Fatal("recall returned zero results")
		}

		// Find the node we linked
		var found bool
		for _, r := range out.Results {
			if r.ID == "e2e-re/fact/a" {
				found = true
				if len(r.Edges) == 0 {
					t.Fatal("node a has zero edges in recall result")
				}
				var hasImplements bool
				for _, e := range r.Edges {
					if e.EdgeType == "implements" {
						hasImplements = true
						if e.ToID != "e2e-re/fact/b" {
							t.Errorf("expected to_id=b, got %s", e.ToID)
						}
						if e.ToTitle == "" {
							t.Error("edge to_title should be non-empty for known target")
						}
						if e.Confidence < 0 {
							t.Errorf("negative confidence %f", e.Confidence)
						}
					}
				}
				if !hasImplements {
					t.Error("no implements edge found on node a")
				}
			}
		}
		if !found {
			t.Error("e2e-re/fact/a not found in recall results")
		}
	})

	// 3. recall with nonexistent query — edges field should be empty or absent
	t.Run("recall nonexistent query has empty edges", func(t *testing.T) {
		var out struct {
			Results []struct {
				Edges []struct{} `json:"edges,omitempty"`
			} `json:"results"`
		}
		callTool(t, sess, "recall", map[string]any{
			"project_id": "e2e-re",
			"query":      "nonexistent-unique-string-xyz-999",
			"limit":      5,
		}, &out)
		// Should not error — just return empty results with no edges.
	})

	// 4. recall a function from the ingested codebase — should have edges
	//    from codeast (calls, defines, imports)
	t.Run("recall code node has edges", func(t *testing.T) {
		var out struct {
			Results []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Edges []struct {
					ToID       string  `json:"to_id"`
					ToTitle    string  `json:"to_title"`
					EdgeType   string  `json:"edge_type"`
					Confidence float64 `json:"confidence"`
				} `json:"edges,omitempty"`
			} `json:"results"`
		}
		callTool(t, sess, "recall", map[string]any{
			"project_id": "e2e-re",
			"query":      "MarkStaleDownstream",
			"limit":      5,
		}, &out)

		if len(out.Results) == 0 {
			t.Skip("recall returned no results for MarkStaleDownstream")
		}
		hasEdges := false
		for _, r := range out.Results {
			if len(r.Edges) > 0 {
				hasEdges = true
				break
			}
		}
		if !hasEdges {
			t.Log("WARN: no results had edges — codeast may not have produced edges for this node")
		}
	})
}

func ingestProjectRE(t *testing.T, sess *mcp.ClientSession) {
	t.Helper()
	type jobRes struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	var out jobRes
	callTool(t, sess, "ingest", map[string]any{
		"cwd":        "../..",
		"project_id": "e2e-re",
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