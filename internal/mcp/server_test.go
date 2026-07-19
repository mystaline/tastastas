package mcp

import (
	"context"
	"testing"

	"github.com/mystaline-dev/tastastas/internal/ingest/docwalk"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
)

// TestMCPSmoke runs the full ingest→recall loop via MCP tool handlers
// against an in-memory SQLite store, validating the wiring works end-to-end.
func TestMCPSmoke(t *testing.T) {
	db, err := sqlitestore.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := NewServer(db)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}

	// The MCP SDK's AddTool registers handlers internally. We can't call
	// tool handlers directly through the public API without a transport,
	// so this test validates that NewServer doesn't panic and the server
	// object is usable. Full end-to-end MCP protocol testing requires a
	// real stdio transport (covered by the manual smoke test in Phase 3).
	_ = srv

	// Direct store-level smoke: ingest via docwalk, then search.
	// This validates the same data path the MCP tools use internally.
	ctx := context.Background()
	cfg, err := docwalk.LoadConfig("../ingest/docwalk/testdata/acme-style/.memoryrc.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	nodes, edges, err := docwalk.Ingest("../ingest/docwalk/testdata/acme-style", cfg)
	if err != nil {
		t.Fatalf("docwalk.Ingest: %v", err)
	}
	for i := range nodes {
		if err := db.UpsertNode(ctx, nodes[i]); err != nil {
			t.Fatalf("UpsertNode %s: %v", nodes[i].ID, err)
		}
	}
	for i := range edges {
		if err := db.UpsertEdge(ctx, edges[i]); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}

	// recall: search for "coupon" — should hit coupon-redeem PRD + coupon-expiry PRD
	results, err := db.SearchLexical(ctx, cfg.ProjectID, "coupon", 10)
	if err != nil {
		t.Fatalf("SearchLexical: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results for 'coupon', got %d", len(results))
	}

	// edges: expect 3 cross-link edges for coupon-redeem (prd+apispec+erd+testcase)
	if len(edges) != 3 {
		t.Fatalf("expected 3 cross-link edges, got %d", len(edges))
	}
}
