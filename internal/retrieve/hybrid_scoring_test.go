package retrieve

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline/tastastas/internal/embed"
	"github.com/mystaline/tastastas/internal/store"
	"github.com/mystaline/tastastas/internal/store/sqlite"
)

// TestRecallHybridScoring proves fused lexical+vector scoring (B2 in the
// unified embeddings plan) actually changes ranking vs. lexical-only:
// a node that matches BOTH the query's literal words AND its meaning
// should outrank one that only matches literal words but is semantically
// unrelated, and MatchType should reflect "hybrid" for the dual match.
// Uses the real ONNX sidecar — skips gracefully if unavailable.
func TestRecallHybridScoring(t *testing.T) {
	sc, err := embed.NewSidecar()
	if err != nil {
		t.Skipf("sidecar unavailable on this platform: %v", err)
	}
	defer sc.Close()

	ctx := context.Background()
	db, err := sqlite.Open(ctx, ":memory:", 384)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// "auth" node: matches query lexically (word "token") AND semantically
	// (about JWT/bearer token validation, closely related to the query).
	// "decoy" node: matches lexically too (also contains "token") but is
	// about an unrelated domain (game currency), so its vector should be
	// farther from the query's embedding.
	nodes := []store.Node{
		{ID: "auth/validate.go", ProjectID: "hybrid-test", NodeType: "go", Title: "validate.go", Importance: 0.5,
			Content: "func ValidateJWT(token string) (*Claims, error) { return parseAndVerify(token) }"},
		{ID: "game/currency.go", ProjectID: "hybrid-test", NodeType: "go", Title: "currency.go", Importance: 0.5,
			Content: "func SpendToken(playerID string, amount int) error { return deductBalance(playerID, amount) }"},
	}
	for i := range nodes {
		vec, err := sc.Embed(ctx, nodes[i].Content)
		if err != nil {
			t.Fatalf("embed node %s: %v", nodes[i].ID, err)
		}
		nodes[i].Embedding = vec
		if err := db.UpsertNode(ctx, nodes[i]); err != nil {
			t.Fatalf("UpsertNode: %v", err)
		}
	}

	retriever := New(db, DefaultConfig())
	queryVec, err := sc.Embed(ctx, "verify a JWT bearer token's signature")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}

	result, err := retriever.Recall(ctx, RecallParams{
		ProjectID: "hybrid-test",
		Query:     "token", // matches both nodes lexically
		Embedding: queryVec,
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(result.Nodes) < 2 {
		t.Fatalf("expected both nodes to surface (lexical match on both), got %d: %+v", len(result.Nodes), result.Nodes)
	}
	if result.Nodes[0].ID != "auth/validate.go" {
		t.Errorf("expected auth/validate.go ranked first (lexical+semantic match beats lexical-only), got %q first", result.Nodes[0].ID)
	}
	if result.Nodes[0].MatchType != "hybrid" {
		t.Errorf("expected top result MatchType=hybrid (matched both lexically and semantically), got %q", result.Nodes[0].MatchType)
	}
	if result.Nodes[0].Score <= result.Nodes[1].Score {
		t.Errorf("expected hybrid-matched node to outscore lexical-only node: got %.4f vs %.4f",
			result.Nodes[0].Score, result.Nodes[1].Score)
	}
}
