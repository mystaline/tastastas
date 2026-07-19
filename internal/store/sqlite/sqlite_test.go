package sqlite

import (
	"context"
	"testing"

	"github.com/mystaline-dev/tastastas/internal/store"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndGetNode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	n := store.Node{
		ID:        "acme/prd/coupon-redeem",
		NodeType:  "prd",
		Title:     "Coupon Redeem PRD",
		Content:   "Users can redeem a coupon code against an order at checkout.",
		Embedding: []float32{0.1, 0.2, 0.3, 0.4},
	}
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	got, err := s.GetNode(ctx, n.ID)
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Title != n.Title || got.Content != n.Content || got.ProjectID != "default" || got.Status != "current" {
		t.Fatalf("roundtrip mismatch: got %+v", got)
	}

	// re-upsert with new content should update, not duplicate
	n.Content = "Updated: redemption now requires a minimum order total."
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatalf("UpsertNode (update): %v", err)
	}
	got2, err := s.GetNode(ctx, n.ID)
	if err != nil {
		t.Fatalf("GetNode after update: %v", err)
	}
	if got2.Content != n.Content {
		t.Fatalf("expected updated content, got %q", got2.Content)
	}
}

func TestGetNodeNotFound(t *testing.T) {
	s := openTest(t)
	_, err := s.GetNode(context.Background(), "does/not/exist")
	if err == nil {
		t.Fatal("expected error for missing node, got nil")
	}
}

func TestSearchLexical(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	seed := []store.Node{
		{ID: "a", NodeType: "prd", Title: "Coupon Redeem", Content: "redeem a coupon code against an order at checkout"},
		{ID: "b", NodeType: "prd", Title: "Coupon Expiry", Content: "coupons expire and become invalid after their expiry date"},
		{ID: "c", NodeType: "prd", Title: "Login Flow", Content: "authenticate users via SSO"},
	}
	for _, n := range seed {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatalf("seed UpsertNode(%s): %v", n.ID, err)
		}
	}

	results, err := s.SearchLexical(ctx, "default", "coupon", 10)
	if err != nil {
		t.Fatalf("SearchLexical: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 matches for 'coupon', got %d: %+v", len(results), results)
	}
	ids := map[string]bool{}
	for _, r := range results {
		ids[r.ID] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Fatalf("expected matches a and b, got %+v", ids)
	}
}

func TestSearchVector(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	seed := []store.Node{
		{ID: "near", NodeType: "fact", Title: "near", Content: "x", Embedding: []float32{1, 0, 0, 0}},
		{ID: "far", NodeType: "fact", Title: "far", Content: "x", Embedding: []float32{0, 0, 0, 1}},
	}
	for _, n := range seed {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatalf("seed UpsertNode(%s): %v", n.ID, err)
		}
	}

	results, err := s.SearchVector(ctx, "default", []float32{1, 0, 0, 0}, 2)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "near" {
		t.Fatalf("expected 'near' to rank first, got %q first", results[0].ID)
	}
}

// seedImpactGraph builds: prd --(implements: api-spec->prd)-- api-spec
//
//	prd --(tests: test-case->prd)-- test-case
//	prd --(specifies: erd->prd)-- erd
//	prd --(depends-on: prd->shared-lib)-- shared-lib (outgoing from prd itself)
func seedImpactGraph(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	nodes := []store.Node{
		{ID: "prd", NodeType: "prd"},
		{ID: "api-spec", NodeType: "api-spec"},
		{ID: "test-case", NodeType: "test-case"},
		{ID: "erd", NodeType: "erd"},
		{ID: "unrelated", NodeType: "generic-doc"},
	}
	for _, n := range nodes {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatalf("seed node %s: %v", n.ID, err)
		}
	}
	edges := []store.Edge{
		{FromID: "prd", ToID: "api-spec", EdgeType: "implements"},
		{FromID: "prd", ToID: "test-case", EdgeType: "tests"},
		{FromID: "prd", ToID: "erd", EdgeType: "specifies"},
		{FromID: "prd", ToID: "unrelated", EdgeType: "related"}, // not an impact type, should NOT propagate
	}
	for _, e := range edges {
		if err := s.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("seed edge %+v: %v", e, err)
		}
	}
}

func TestNeighbors(t *testing.T) {
	s := openTest(t)
	seedImpactGraph(t, s)

	nodes, edges, err := s.Neighbors(context.Background(), "prd", nil, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 neighbors (api-spec, test-case, erd, unrelated), got %d: %+v", len(nodes), nodes)
	}
	if len(edges) != 4 {
		t.Fatalf("expected 4 edges, got %d", len(edges))
	}
}

func TestMarkStaleDownstream(t *testing.T) {
	s := openTest(t)
	seedImpactGraph(t, s)
	ctx := context.Background()

	stale, err := s.MarkStaleDownstream(ctx, "prd", 1)
	if err != nil {
		t.Fatalf("MarkStaleDownstream: %v", err)
	}

	staleIDs := map[string]bool{}
	for _, n := range stale {
		staleIDs[n.ID] = true
	}
	for _, want := range []string{"api-spec", "test-case", "erd"} {
		if !staleIDs[want] {
			t.Errorf("expected %s to be flagged stale, got %+v", want, staleIDs)
		}
	}
	if staleIDs["unrelated"] {
		t.Error("unrelated (edge_type=related) should NOT be flagged stale")
	}

	// verify persisted, not just returned
	n, err := s.GetNode(ctx, "api-spec")
	if err != nil {
		t.Fatalf("GetNode(api-spec): %v", err)
	}
	if n.Status != "stale" {
		t.Errorf("expected api-spec.status=stale in DB, got %q", n.Status)
	}
	unrelated, err := s.GetNode(ctx, "unrelated")
	if err != nil {
		t.Fatalf("GetNode(unrelated): %v", err)
	}
	if unrelated.Status != "current" {
		t.Errorf("expected unrelated.status to remain current, got %q", unrelated.Status)
	}
}
