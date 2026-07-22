package libsql

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mystaline-dev/tastastas/internal/store"
)

func TestLibsqlLocalOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Basic CRUD test
	ctx := context.Background()
	n := store.Node{
		ID:            "test/fact/abc",
		ProjectID:     "test",
		NodeType:      "fact",
		Title:         "Test Fact",
		Content:       "Test content for libsql backend",
		SourceAdapter: "test",
		Importance:    0.7,
	}
	if err := db.UpsertNode(ctx, n); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetNode(ctx, n.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != n.Title {
		t.Errorf("title: got %q, want %q", got.Title, n.Title)
	}
}

func TestLibsqlEdgeCRUD(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	for _, id := range []string{"a/b/1", "a/b/2"} {
		if err := db.UpsertNode(ctx, store.Node{
			ID: id, ProjectID: "a", NodeType: "fact", Title: id, SourceAdapter: "test",
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if err := db.UpsertEdge(ctx, store.Edge{FromID: "a/b/1", ToID: "a/b/2", EdgeType: "calls", Confidence: 1.0}); err != nil {
		t.Fatalf("upsert edge: %v", err)
	}

	nodes, _, err := db.Neighbors(ctx, "a/b/1", nil, 1)
	if err != nil {
		t.Fatalf("neighbors: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("expected 1 neighbor, got %d", len(nodes))
	}
}

func TestLibsqlSearchLexical(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.UpsertNode(ctx, store.Node{
		ID: "p/f/1", ProjectID: "p", NodeType: "fact",
		Title: "Coupon redeem", Content: "applies a coupon at checkout",
		SourceAdapter: "test",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.UpsertNode(ctx, store.Node{
		ID: "p/f/2", ProjectID: "p", NodeType: "fact",
		Title: "Other doc", Content: "completely unrelated text",
		SourceAdapter: "test",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hits, err := db.SearchLexical(ctx, "p", "coupon", 10)
	if err != nil {
		// FTS5 not available in tursogo local mode - skip test
		if err.Error() == "libsql: FTS5 not available (nodes_fts table missing)" {
			t.Skip("FTS5 not available in tursogo local mode, skipping lexical search test")
		}
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least 1 hit for 'coupon'")
	}
	found := false
	for _, h := range hits {
		if h.ID == "p/f/1" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find p/f/1 in results")
	}
}

func TestLibsqlDeleteNode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.UpsertNode(ctx, store.Node{ID: "x", ProjectID: "x", NodeType: "fact", SourceAdapter: "t"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.DeleteNode(ctx, "x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = db.GetNode(ctx, "x")
	if err == nil {
		t.Fatal("expected error on get deleted node")
	}
}