package main

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/retrieve"
	"github.com/mystaline-dev/tastastas/internal/store"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
)

// TestSidecarEndToEndRecall proves the --embed-backend=sidecar wiring works
// for real: spawn the sidecar via newEmbedder, embed two nodes with it,
// store their vectors, then confirm Recall's vector fusion actually uses
// the sidecar's embeddings to rank a semantically-related (but lexically
// dissimilar) query above an unrelated one.
func TestSidecarEndToEndRecall(t *testing.T) {
	embedder := newEmbedder("sidecar", 0, 0, 0, "", "", "", "", "", 0, 0)
	sc, ok := embedder.(*embed.SidecarEmbedder)
	if !ok {
		t.Skip("sidecar unavailable on this platform (embedder fell back to nil)")
	}
	defer sc.Close()

	ctx := context.Background()
	db, err := sqlitestore.Open(ctx, ":memory:", 384)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	nodes := []store.Node{
		{ID: "auth/middleware.go", NodeType: "go", Title: "middleware.go", Importance: 0.5,
			Content: "func ValidateJWT(token string) (*Claims, error) { return parseAndVerify(token) }"},
		{ID: "billing/invoice.go", NodeType: "go", Title: "invoice.go", Importance: 0.5,
			Content: "func GenerateInvoice(order Order) (*Invoice, error) { return computeTotals(order) }"},
	}
	for i := range nodes {
		vec, err := sc.Embed(ctx, nodes[i].Content)
		if err != nil {
			t.Fatalf("sidecar embed: %v", err)
		}
		nodes[i].Embedding = vec
		if err := db.UpsertNode(ctx, nodes[i]); err != nil {
			t.Fatalf("UpsertNode: %v", err)
		}
	}

	retriever := retrieve.New(db, retrieve.DefaultConfig())
	queryVec, err := sc.Embed(ctx, "how do we verify a bearer token's signature")
	if err != nil {
		t.Fatalf("sidecar embed query: %v", err)
	}

	result, err := retriever.Recall(ctx, retrieve.RecallParams{
		ProjectID: "default",
		Query:     "zzz_no_lexical_overlap_zzz", // deliberately no FTS5 hit
		Embedding: queryVec,
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(result.Nodes) == 0 {
		t.Fatal("expected at least one result via semantic-only match, got none")
	}
	if result.Nodes[0].ID != "auth/middleware.go" {
		t.Errorf("expected auth/middleware.go ranked first (semantically closer to bearer-token query), got %q", result.Nodes[0].ID)
	}
	if result.Nodes[0].MatchType != "semantic" {
		t.Errorf("expected MatchType=semantic (no lexical overlap by design), got %q", result.Nodes[0].MatchType)
	}
}
