package retrieve

import (
	"context"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

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
	result, err := r.Recall(ctx, RecallParams{ProjectID: "test", Query: "coupon", Limit: 10})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(result.Nodes) == 0 {
		t.Fatal("expected at least 1 result")
	}

	t.Logf("Recall results (sorted by score):")
	for _, s := range result.Nodes {
		t.Logf("  %s [score=%.4f] %s", s.ID, s.Score, s.Title)
	}

	if result.Nodes[0].Importance < result.Nodes[len(result.Nodes)-1].Importance {
		t.Errorf("results not sorted by score: top=%s (%.2f) < bottom=%s (%.2f)",
			result.Nodes[0].ID, result.Nodes[0].Score,
			result.Nodes[len(result.Nodes)-1].ID, result.Nodes[len(result.Nodes)-1].Score)
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
		Alpha:           1.0, // lexical-only for deterministic test
	})
	result, err := r.Recall(ctx, RecallParams{ProjectID: "test", Query: "coupon", Limit: 10})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	ids := map[string]bool{}
	for _, s := range result.Nodes {
		ids[s.ID] = true
	}

	directHits := 0
	pulledIn := 0
	for _, s := range result.Nodes {
		if s.NodeType == "prd" || s.NodeType == "fact" {
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

	d := r.recencyDecay(0)
	if d != 1.0 {
		t.Errorf("expected 1.0 for zero age, got %f", d)
	}

	d = r.recencyDecay(7 * 24 * time.Hour)
	if d < 0.49 || d > 0.51 {
		t.Errorf("expected ~0.5 at one half-life, got %f", d)
	}

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

	t.Logf("Stale downstream nodes after PRD update:")
	for _, n := range stale {
		t.Logf("  %s [%s] status=%s", n.ID, n.NodeType, n.Status)
	}

	staleIDs := map[string]bool{}
	for _, n := range stale {
		staleIDs[n.ID] = true
	}
	for _, expected := range []string{"api/coupon-redeem", "erd/coupon-redeem", "test/coupon-redeem"} {
		if !staleIDs[expected] {
			t.Errorf("expected %s to be marked stale", expected)
		}
	}

	for _, n := range stale {
		if n.Status != "stale" {
			t.Errorf("node %s: returned Status = %q, want \"stale\"", n.ID, n.Status)
		}
	}
}

func TestNeighborsWithConfidenceMatchesEdge(t *testing.T) {
	st, err := sqlite.Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	nodes := []store.Node{
		{ID: "root", ProjectID: "test", NodeType: "prd", Title: "Root"},
		{ID: "low-conf", ProjectID: "test", NodeType: "api-spec", Title: "Low"},
		{ID: "high-conf", ProjectID: "test", NodeType: "erd", Title: "High"},
	}
	for _, n := range nodes {
		if err := st.UpsertNode(ctx, n); err != nil {
			t.Fatalf("UpsertNode %s: %v", n.ID, err)
		}
	}
	edges := []store.Edge{
		{FromID: "root", ToID: "low-conf", EdgeType: "related", Confidence: 0.3},
		{FromID: "root", ToID: "high-conf", EdgeType: "related", Confidence: 0.95},
	}
	for _, e := range edges {
		if err := st.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}

	_, confidence, err := st.NeighborsWithConfidence(ctx, "root", nil, 1)
	if err != nil {
		t.Fatalf("NeighborsWithConfidence: %v", err)
	}
	if confidence["low-conf"] != 0.3 {
		t.Errorf("low-conf: confidence = %v, want 0.3", confidence["low-conf"])
	}
	if confidence["high-conf"] != 0.95 {
		t.Errorf("high-conf: confidence = %v, want 0.95", confidence["high-conf"])
	}
}
