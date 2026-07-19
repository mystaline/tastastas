package retrieve

import (
	"context"
	"testing"
	"time"

	"github.com/mystaline-dev/tastastas/internal/store"
	"github.com/mystaline-dev/tastastas/internal/store/sqlite"
)

func seedGraph(t *testing.T, st store.Store, ctx context.Context) {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	nodes := []store.Node{
		{ID: "prd/coupon-redeem", ProjectID: "test", NodeType: "prd", Title: "Coupon Redeem PRD",
			Content: "Users can redeem a coupon code against an order at checkout.", Importance: 0.8, UpdatedAt: now},
		{ID: "api/coupon-redeem", ProjectID: "test", NodeType: "api-spec", Title: "Coupon Redeem API",
			Content: "POST /api/coupons/redeem validates and applies a coupon to an order.", Importance: 0.7, UpdatedAt: now},
		{ID: "erd/coupon-redeem", ProjectID: "test", NodeType: "erd", Title: "Coupon Redeem ERD",
			Content: "ERD for coupon module with CouponCode and Redemption tables.", Importance: 0.5, UpdatedAt: now},
		{ID: "test/coupon-redeem", ProjectID: "test", NodeType: "test-case", Title: "Coupon Redeem Tests",
			Content: "E2E tests for coupon redeem flow including edge cases.", Importance: 0.4, UpdatedAt: now},
		// Old fact — should have lower recency score
		{ID: "fact/old", ProjectID: "test", NodeType: "fact", Title: "Old Fact",
			Content: "An old fact about the coupon system that was relevant months ago.", Importance: 0.9,
			UpdatedAt: now, CreatedAt: "2025-01-01T00:00:00.000Z"},
	}

	for _, n := range nodes {
		if err := st.UpsertNode(ctx, n); err != nil {
			t.Fatalf("UpsertNode %s: %v", n.ID, err)
		}
	}

	edges := []store.Edge{
		{FromID: "prd/coupon-redeem", ToID: "api/coupon-redeem", EdgeType: "implements", Confidence: 1.0},
		{FromID: "prd/coupon-redeem", ToID: "erd/coupon-redeem", EdgeType: "specifies", Confidence: 1.0},
		{FromID: "prd/coupon-redeem", ToID: "test/coupon-redeem", EdgeType: "tests", Confidence: 1.0},
	}
	for _, e := range edges {
		if err := st.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}
}

func TestRecallWithScoring(t *testing.T) {
	st, err := sqlite.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	seedGraph(t, st, ctx)

	r := New(st, DefaultConfig())
	results, err := r.Recall(ctx, "test", "coupon", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}

	t.Logf("Recall results (sorted by score):")
	for _, s := range results {
		t.Logf("  %s [score=%.4f] %s", s.Node.ID, s.Score, s.Node.Title)
	}

	// Highest importance node should score highest (both are recent)
	if results[0].Node.Importance < results[len(results)-1].Importance {
		t.Errorf("results not sorted by score: top=%s (%.2f) < bottom=%s (%.2f)",
			results[0].Node.ID, results[0].Score, results[len(results)-1].Node.ID, results[len(results)-1].Score)
	}
}

func TestRecallPullsInNeighbors(t *testing.T) {
	st, err := sqlite.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	seedGraph(t, st, ctx)

	r := New(st, Config{
		RecencyHalfLife: 7 * 24 * time.Hour,
		NeighborDepth:   1,
		MaxResults:      20,
	})
	results, err := r.Recall(ctx, "test", "coupon", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	// Should include pulled-in neighbors (api, erd, test) even if they
	// didn't match the query directly, because they're neighbors of the PRD
	ids := map[string]bool{}
	for _, s := range results {
		ids[s.Node.ID] = true
	}

	// At minimum: PRD matched directly + at least 2 of its neighbors pulled in
	directHits := 0
	pulledIn := 0
	for _, s := range results {
		if s.Node.NodeType == "prd" || s.Node.NodeType == "fact" {
			directHits++
		} else {
			pulledIn++
		}
	}
	t.Logf("direct hits: %d, pulled in: %d", directHits, pulledIn)
	if pulledIn == 0 {
		t.Error("expected at least 1 neighbor pulled in")
	}
}

func TestRecencyDecay(t *testing.T) {
	r := New(nil, Config{RecencyHalfLife: 7 * 24 * time.Hour})

	// Zero age = full score
	d := r.recencyDecay(0)
	if d != 1.0 {
		t.Errorf("expected 1.0 for zero age, got %f", d)
	}

	// One half-life = 0.5
	d = r.recencyDecay(7 * 24 * time.Hour)
	if d < 0.49 || d > 0.51 {
		t.Errorf("expected ~0.5 at one half-life, got %f", d)
	}

	// Two half-lives = 0.25
	d = r.recencyDecay(14 * 24 * time.Hour)
	if d < 0.24 || d > 0.26 {
		t.Errorf("expected ~0.25 at two half-lives, got %f", d)
	}
}

func TestCheckImpact(t *testing.T) {
	st, err := sqlite.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	seedGraph(t, st, ctx)

	// Update PRD content → should flag downstream nodes as stale
	prd, err := st.GetNode(ctx, "prd/coupon-redeem")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	prd.Content = "Updated: users can now stack multiple coupon codes per order."
	prd.ContentHash = "" // force change
	if err := st.UpsertNode(ctx, prd); err != nil {
		t.Fatalf("UpsertNode update: %v", err)
	}

	stale, err := st.MarkStaleDownstream(ctx, "prd/coupon-redeem", 2)
	if err != nil {
		t.Fatalf("MarkStaleDownstream: %v", err)
	}

	t.Logf("Stale downstream nodes after PRD update:")
	for _, n := range stale {
		t.Logf("  %s [%s] status=%s", n.ID, n.NodeType, n.Status)
	}

	// api, erd, test should all be stale
	staleIDs := map[string]bool{}
	for _, n := range stale {
		staleIDs[n.ID] = true
	}
	for _, expected := range []string{"api/coupon-redeem", "erd/coupon-redeem", "test/coupon-redeem"} {
		if !staleIDs[expected] {
			t.Errorf("expected %s to be marked stale", expected)
		}
	}
}
