// Package e2e spawns the real compiled tastastas binary and speaks the
// actual MCP wire protocol to it — the one test tier that catches
// wire-format bugs (struct tag typos, SDK serialization surprises,
// contract drift) that Tier 1/2 tests calling Go functions directly cannot
// see by construction. See Standing Rule 7 in the plan file.
package e2e

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildBinary compiles cmd/tastastas once per test run into a temp dir and
// returns its path. Skips the test if the build fails for environmental
// reasons (e.g. no network for go build in a sandboxed CI) rather than
// failing outright — the goal is a fast fail for genuine wire bugs, not a
// flaky build-tooling gate.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tastastas")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/tastastas")
	cmd.Dir = "." // internal/e2e -> repo root is two levels up via ../../
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build cmd/tastastas: %v\n%s", err, out)
	}
	return bin
}

// connect spawns the binary over stdio via the real MCP CommandTransport
// and returns a live client session. dbPath is a fresh per-test SQLite file
// so tests don't share state.
func connect(t *testing.T, bin, dbPath string) *mcp.ClientSession {
	t.Helper()
	return connectWithTimeout(t, bin, dbPath, 30*time.Second)
}

// connectWithTimeout is connect with a caller-chosen session budget. The
// timeout matters: exec.CommandContext kills the spawned server when the
// session context fires, so tests that kick off long async jobs (ingest)
// must pass a budget larger than the job itself.
func connectWithTimeout(t *testing.T, bin, dbPath string, timeout time.Duration) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, bin, "-db", dbPath, "-embed-backend", "none")
	transport := &mcp.CommandTransport{Command: cmd}

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-test", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect to spawned binary: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// callTool calls an MCP tool over the real wire and unmarshals its
// StructuredContent into out. Fails the test on any transport, tool, or
// unmarshal error — this is the point of the test: any of those failing
// means the wire contract broke.
func callTool(t *testing.T, sess *mcp.ClientSession, name string, args any, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): transport error: %v", name, err)
	}
	if res.IsError {
		b, _ := json.Marshal(res.Content)
		t.Fatalf("CallTool(%s): tool returned error result: %s", name, b)
	}
	if out == nil {
		return
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("CallTool(%s): re-marshal StructuredContent: %v", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("CallTool(%s): unmarshal StructuredContent into %T: %v (raw: %s)", name, out, err, b)
	}
}

// TestE2EStdioToolSequence spawns the real binary over real stdio JSON-RPC
// and drives the full remember -> recall -> link -> check_impact -> forget
// sequence, asserting on the actual wire-format JSON at each step. This is
// the test that would have caught the check_impact wire-shape bug (input
// field named "changed_id" vs plan's "id", output field "stale" vs plan's
// "stale_nodes") on day one instead of via manual review.
func TestE2EStdioToolSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E binary-spawn test in -short mode")
	}
	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	sess := connect(t, bin, dbPath)

	// remember: insert a fact with an explicit ID, confirm wire shape.
	var rememberOut struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	callTool(t, sess, "remember", map[string]any{
		"id":         "e2e/fact/prd-001",
		"project_id": "e2e",
		"node_type":  "fact",
		"title":      "PRD: coupon redeem",
		"content":    "Coupon redeem feature description for E2E test.",
	}, &rememberOut)
	if rememberOut.ID != "e2e/fact/prd-001" {
		t.Fatalf("remember: expected id echoed back, got %+v", rememberOut)
	}
	if rememberOut.Status == "" {
		t.Fatalf("remember: expected non-empty status, got %+v", rememberOut)
	}

	// remember a second node to link against.
	callTool(t, sess, "remember", map[string]any{
		"id":         "e2e/fact/api-001",
		"project_id": "e2e",
		"node_type":  "fact",
		"title":      "API: coupon redeem endpoint",
		"content":    "API spec for coupon redemption, depends on the PRD.",
	}, nil)

	// recall: lexical search should surface at least one of the above.
	var recallOut struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	callTool(t, sess, "recall", map[string]any{
		"project_id": "e2e",
		"query":      "coupon redeem",
	}, &recallOut)
	if len(recallOut.Results) == 0 {
		t.Fatalf("recall: expected at least 1 result, got 0")
	}

	// link: prd-001 implements api-001 — MarkStaleDownstream walks OUTGOING
	// edges from the changed node, so the edge must point prd -> api for
	// changing prd-001 to mark api-001 stale (see Store.Neighbors/
	// MarkStaleDownstream in internal/store/sqlite/crud.go: impact types
	// are "implements", "tests", "specifies", "depends-on" — hyphenated,
	// not "depends_on").
	var linkOut struct {
		Status string `json:"status"`
	}
	callTool(t, sess, "link", map[string]any{
		"from_id":    "e2e/fact/prd-001",
		"to_id":      "e2e/fact/api-001",
		"edge_type":  "implements",
		"confidence": 1.0,
	}, &linkOut)
	if linkOut.Status == "" {
		t.Fatalf("link: expected non-empty status, got %+v", linkOut)
	}

	// check_impact: changing prd-001 should mark api-001 stale. This is the
	// exact wire contract the plan specifies: input field "id" (not
	// "changed_id"), output field "stale_nodes" (not "stale").
	var impactOut struct {
		StaleNodes []struct {
			ID       string `json:"id"`
			NodeType string `json:"node_type"`
		} `json:"stale_nodes"`
	}
	callTool(t, sess, "check_impact", map[string]any{
		"id": "e2e/fact/prd-001",
	}, &impactOut)
	found := false
	for _, n := range impactOut.StaleNodes {
		if n.ID == "e2e/fact/api-001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("check_impact: expected e2e/fact/api-001 marked stale, got %+v", impactOut.StaleNodes)
	}

	// forget: delete prd-001, confirm wire shape distinguishes deleted vs not_found.
	var forgetOut struct {
		Status string `json:"status"`
	}
	callTool(t, sess, "forget", map[string]any{"id": "e2e/fact/prd-001"}, &forgetOut)
	if forgetOut.Status != "deleted" {
		t.Fatalf("forget: expected status=deleted for existing node, got %+v", forgetOut)
	}

	callTool(t, sess, "forget", map[string]any{"id": "e2e/fact/prd-001"}, &forgetOut)
	if forgetOut.Status != "not_found" {
		t.Fatalf("forget: expected status=not_found on second call (already gone), got %+v", forgetOut)
	}
}
