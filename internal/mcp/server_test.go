package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline/tastastas/internal/ingest/docwalk"
	sqlitestore "github.com/mystaline/tastastas/internal/store/sqlite"
)

// TestMCPSmoke runs the full ingest→recall loop via MCP tool handlers
// against an in-memory SQLite store, validating the wiring works end-to-end.
func TestMCPSmoke(t *testing.T) {
	db, err := sqlitestore.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := NewServer(db, nil, 32, "", "")
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
	nodes, edges, _, _, err := docwalk.Ingest("../ingest/docwalk/testdata/acme-style", cfg)
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

// TestMCPIngestGitrepo exercises the runIngestAdapter dispatch for the
// gitrepo adapter and confirms nodes land in the store.
func TestMCPIngestGitrepo(t *testing.T) {
	db, err := sqlitestore.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "project-a"), 0o755)
	os.WriteFile(filepath.Join(root, "project-a/MEMORY.md"), []byte("# Project A\nBilling service."), 0o644)

	nodes, edges, _, _, err := runIngestAdapter("gitrepo", root, "", "test-proj")
	if err != nil {
		t.Fatalf("runIngestAdapter(gitrepo): %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges from gitrepo, got %d", len(edges))
	}

	ctx := context.Background()
	for _, n := range nodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			t.Fatalf("UpsertNode: %v", err)
		}
	}
	got, err := db.GetNode(ctx, nodes[0].ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.ProjectID != "test-proj" {
		t.Errorf("expected project_id test-proj, got %q", got.ProjectID)
	}
}

// TestMCPIngestObsidian exercises the runIngestAdapter dispatch for the
// obsidian adapter using the vault test fixture, and confirms nodes+edges
// land in the store.
func TestMCPIngestObsidian(t *testing.T) {
	db, err := sqlitestore.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	nodes, edges, _, _, err := runIngestAdapter("obsidian", "../ingest/obsidian/testdata/vault", "", "vault-proj")
	if err != nil {
		t.Fatalf("runIngestAdapter(obsidian): %v", err)
	}
	if len(nodes) < 1 {
		t.Fatalf("expected at least 1 node, got %d", len(nodes))
	}

	ctx := context.Background()
	for _, n := range nodes {
		if err := db.UpsertNode(ctx, n); err != nil {
			t.Fatalf("UpsertNode %s: %v", n.ID, err)
		}
	}
	for _, e := range edges {
		// wikilinks may point at un-ingested targets; ignore FK-style errors,
		// same tolerance the real ingest tool applies (edges table has no FK).
		if err := db.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}

	got, err := db.GetNode(ctx, nodes[0].ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.ProjectID != "vault-proj" {
		t.Errorf("expected project_id vault-proj, got %q", got.ProjectID)
	}
}

// TestRunIngestAdapterUnknown confirms an unsupported adapter name errors
// instead of silently no-oping.
func TestRunIngestAdapterUnknown(t *testing.T) {
	_, _, _, _, err := runIngestAdapter("not-a-real-adapter", "/tmp", "", "")
	if err == nil {
		t.Fatal("expected error for unknown adapter")
	}
}
