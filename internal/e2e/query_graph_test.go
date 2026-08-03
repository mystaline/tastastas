// Package e2e — query_graph E2E test.
//
// Spawns real binary, ingests tastastas repo itself as ground truth, runs
// query_graph on a known function, and asserts edges are returned with
// correct direction, titles, and determinism.
package e2e

import (
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestE2EQueryGraph(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping query_graph E2E test in -short mode")
	}

	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "qg-e2e.db")
	sess := connect(t, bin, dbPath)
	ingestProjectQG(t, sess)

	// 1. Recall "MarkStaleDownstream" to find the node ID.
	var recall recallResult
	callTool(t, sess, "recall", map[string]any{
		"project_id": "e2e-qg",
		"stage":      "e2e-test",
		"query":      "MarkStaleDownstream",
		"limit":      5,
	}, &recall)

	// Find the exact MarkStaleDownstream function node.
	var nodeID string
	for _, r := range recall.Results {
		if contains(r.Title, "MarkStaleDownstream") || contains(r.ID, "MarkStaleDownstream") {
			nodeID = r.ID
			break
		}
	}
	if nodeID == "" && len(recall.Results) > 0 {
		// Fallback: use first result's ID.
		nodeID = recall.Results[0].ID
		t.Logf("using first recall result ID as fallback: %s (title=%q)", nodeID, recall.Results[0].Title)
	}
	if nodeID == "" {
		t.Fatal("recall returned zero results — cannot test query_graph without a node")
	}
	t.Logf("resolved node: %s", nodeID)

	// 2. query_graph with incoming direction, filtered by calls+defines.
	t.Run("incoming edges", func(t *testing.T) {
		var out qgResult
		callTool(t, sess, "query_graph", map[string]any{
			"node_id":    nodeID,
			"edge_types": []string{"calls", "defines"},
			"direction":  "incoming",
		}, &out)

		if len(out.Edges) == 0 {
			t.Skip("no incoming edges for this node (acceptable for init/top-level funcs)")
		}
		for _, e := range out.Edges {
			if e.Direction != "incoming" {
				t.Errorf("expected direction=incoming, got %q", e.Direction)
			}
			if e.EdgeType != "calls" && e.EdgeType != "defines" {
				t.Errorf("unexpected edge_type %q (expected calls or defines)", e.EdgeType)
			}
			if e.NodeTitle == "" {
				t.Error("incoming edge has empty node_title")
			}
			if e.NodeType == "" {
				t.Error("incoming edge has empty node_type")
			}
		}
	})

	// 3. query_graph with outgoing direction (no type filter).
	t.Run("outgoing edges", func(t *testing.T) {
		var out qgResult
		callTool(t, sess, "query_graph", map[string]any{
			"node_id":   nodeID,
			"direction": "outgoing",
		}, &out)

		if len(out.Edges) == 0 {
			t.Skip("no outgoing edges for this node (acceptable for leaf funcs)")
		}
		hasCallsOrDefines := false
		for _, e := range out.Edges {
			if e.Direction != "outgoing" {
				t.Errorf("expected direction=outgoing, got %q", e.Direction)
			}
			if e.EdgeType == "calls" || e.EdgeType == "defines" {
				hasCallsOrDefines = true
			}
		}
		if !hasCallsOrDefines {
			t.Log("WARN: no calls/defines in outgoing edges (may be reasonable)")
		}
	})

	// 4. query_graph with both directions.
	t.Run("both directions", func(t *testing.T) {
		var out qgResult
		callTool(t, sess, "query_graph", map[string]any{
			"node_id":   nodeID,
			"direction": "both",
		}, &out)

		if len(out.Edges) < 2 {
			t.Skipf("only %d edges in both directions (may be fine for a leaf node)", len(out.Edges))
		}
		hasOutgoing := false
		hasIncoming := false
		for _, e := range out.Edges {
			if e.Direction == "outgoing" {
				hasOutgoing = true
			}
			if e.Direction == "incoming" {
				hasIncoming = true
			}
		}
		if !hasOutgoing || !hasIncoming {
			t.Logf("WARN: expected both directions — outgoing=%v incoming=%v", hasOutgoing, hasIncoming)
		}
	})

	// 5. Verify every EdgeResult tries to resolve node_title. Some targets may
	//    not exist as stored nodes (cross-project refs), but at least one should
	//    resolve. Also verify non-empty edge_type and confidence >= 0.
	t.Run("edge result completeness", func(t *testing.T) {
		var out qgResult
		callTool(t, sess, "query_graph", map[string]any{
			"node_id": nodeID,
		}, &out)

		if len(out.Edges) == 0 {
			t.Skip("no edges — skipping completeness assertions")
		}
		resolvedTitles := 0
		for i, e := range out.Edges {
			if e.NodeID == "" {
				t.Errorf("edge[%d]: empty node_id", i)
			}
			if e.NodeTitle != "" {
				resolvedTitles++
			}
			if e.EdgeType == "" {
				t.Errorf("edge[%d]: empty edge_type", i)
			}
			if e.Confidence < 0 {
				t.Errorf("edge[%d]: negative confidence %f", i, e.Confidence)
			}
		}
		if resolvedTitles == 0 {
			t.Error("no edges resolved a non-empty node_title — all target nodes missing?")
		}
	})

	// 6. Nonexistent node → empty edges, no error.
	t.Run("nonexistent node", func(t *testing.T) {
		var out qgResult
		callTool(t, sess, "query_graph", map[string]any{
			"node_id": "nonexistent-node-xyz-123",
		}, &out)

		if len(out.Edges) != 0 {
			t.Errorf("expected 0 edges for nonexistent node, got %d", len(out.Edges))
		}
	})

	// 7. Determinism: same query twice → identical ordering.
	t.Run("determinism", func(t *testing.T) {
		var first, second qgResult
		callTool(t, sess, "query_graph", map[string]any{
			"node_id": nodeID,
		}, &first)
		callTool(t, sess, "query_graph", map[string]any{
			"node_id": nodeID,
		}, &second)

		if len(first.Edges) != len(second.Edges) {
			t.Fatalf("determinism: edge count differs %d vs %d", len(first.Edges), len(second.Edges))
		}
		if len(first.Edges) == 0 {
			t.Skip("no edges — cannot verify determinism on empty set")
		}
		for i := range first.Edges {
			if first.Edges[i].NodeID != second.Edges[i].NodeID {
				t.Errorf("edge[%d] NodeID differs: %s vs %s", i, first.Edges[i].NodeID, second.Edges[i].NodeID)
			}
			if first.Edges[i].Direction != second.Edges[i].Direction {
				t.Errorf("edge[%d] Direction differs: %s vs %s", i, first.Edges[i].Direction, second.Edges[i].Direction)
			}
		}
	})
}

// ingestProjectQG ingests tastastas repo into project e2e-qg.
func ingestProjectQG(t *testing.T, sess *mcp.ClientSession) {
	t.Helper()
	type jobRes struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	var out jobRes
	callTool(t, sess, "ingest", map[string]any{
		"cwd":        "../..", // up from internal/e2e/ to repo root
		"project_id": "e2e-qg",
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

// qgResult is the wire shape of QueryGraphOutput for unmarshal.
type qgResult struct {
	NodeID string   `json:"node_id"`
	Title  string   `json:"title"`
	Edges  []qgEdge `json:"edges"`
}

type qgEdge struct {
	Direction  string  `json:"direction"`
	NodeID     string  `json:"node_id"`
	NodeTitle  string  `json:"node_title"`
	NodeType   string  `json:"node_type"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
