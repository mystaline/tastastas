package retrieve

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline/tastastas/internal/store"
	"github.com/mystaline/tastastas/internal/store/sqlite"
)

// TestCheckImpactUnaffectedByChunks proves check_impact's graph traversal
// (MarkStaleDownstream) is entirely blind to the chunk layer — chunks are a
// leaf-level search-index concern, not part of the node/edge impact graph.
// Attaches chunks (with fake but well-formed embeddings, no sidecar
// dependency needed since this test is about graph traversal not real
// semantic search) to every node in the graph before running the same
// impact scenario as TestCheckImpact, and asserts identical results.
func TestCheckImpactUnaffectedByChunks(t *testing.T) {
	st, err := sqlite.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	seedGraph(t, st, ctx)

	// Attach a chunk to every node in the graph — proves chunks existing
	// doesn't change graph traversal outcomes.
	fakeVec := []float32{0.1, 0.2, 0.3, 0.4} // dim=4, matches store.Open above
	chunks := []store.Chunk{
		{ID: "prd/coupon-redeem#0", ParentNodeID: "prd/coupon-redeem", ChunkIndex: 0, Type: "generic", Content: "chunk of prd", Embedding: fakeVec},
		{ID: "api/coupon-redeem#0", ParentNodeID: "api/coupon-redeem", ChunkIndex: 0, Type: "generic", Content: "chunk of api", Embedding: fakeVec},
		{ID: "erd/coupon-redeem#0", ParentNodeID: "erd/coupon-redeem", ChunkIndex: 0, Type: "generic", Content: "chunk of erd", Embedding: fakeVec},
		{ID: "test/coupon-redeem#0", ParentNodeID: "test/coupon-redeem", ChunkIndex: 0, Type: "generic", Content: "chunk of test", Embedding: fakeVec},
	}
	if err := st.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	prd, err := st.GetNode(ctx, "prd/coupon-redeem")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	prd.Content = "Updated: users can now stack multiple coupon codes per order."
	prd.ContentHash = ""
	if err := st.UpsertNode(ctx, prd); err != nil {
		t.Fatalf("UpsertNode update: %v", err)
	}

	stale, err := st.MarkStaleDownstream(ctx, "prd/coupon-redeem", 2)
	if err != nil {
		t.Fatalf("MarkStaleDownstream: %v", err)
	}

	// Identical assertions to TestCheckImpact — chunk layer must be
	// completely invisible to graph traversal.
	staleIDs := map[string]bool{}
	for _, n := range stale {
		staleIDs[n.ID] = true
	}
	for _, expected := range []string{"api/coupon-redeem", "erd/coupon-redeem", "test/coupon-redeem"} {
		if !staleIDs[expected] {
			t.Errorf("expected %s to be marked stale (chunks should not affect graph traversal)", expected)
		}
	}
	if len(stale) != 3 {
		t.Errorf("expected exactly 3 stale nodes (same as TestCheckImpact without chunks), got %d: %+v", len(stale), stale)
	}
	for _, n := range stale {
		if n.Status != "stale" {
			t.Errorf("node %s: returned Status = %q, want \"stale\"", n.ID, n.Status)
		}
	}

	// Sanity: the chunks we attached are still there and untouched — impact
	// marking must not have deleted or mutated them.
	hits, err := st.SearchChunks(ctx, "test", fakeVec, 10, "")
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(hits) != 4 {
		t.Errorf("expected all 4 chunks still present after impact marking, got %d", len(hits))
	}
}
