package libsql

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mystaline/tastastas/internal/scope"
	"github.com/mystaline/tastastas/internal/store"
)

func openStageTest(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), dbPath, 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustEncode(t *testing.T, base, stage string) string {
	t.Helper()
	id, err := scope.Encode(base, stage)
	if err != nil {
		t.Fatalf("scope.Encode(%q,%q): %v", base, stage, err)
	}
	return id
}

func TestStage_IsolatedIngestion(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature/auth")

	nMain := store.Node{ID: main + "/code:function/pkg.Foo", ProjectID: main, NodeType: "code:function", Title: "Foo", Content: "func Foo() {}"}
	nFeature := store.Node{ID: feature + "/code:function/pkg.Foo", ProjectID: feature, NodeType: "code:function", Title: "Foo", Content: "func Foo() { /* changed */ }"}

	if err := s.UpsertNode(ctx, nMain); err != nil {
		t.Fatalf("UpsertNode main: %v", err)
	}
	if err := s.UpsertNode(ctx, nFeature); err != nil {
		t.Fatalf("UpsertNode feature: %v", err)
	}

	if nMain.ID == nFeature.ID {
		t.Fatalf("expected distinct node IDs, got same: %s", nMain.ID)
	}

	got, err := s.GetNode(ctx, nMain.ID)
	if err != nil {
		t.Fatalf("GetNode main: %v", err)
	}
	if got.Content != nMain.Content {
		t.Fatalf("main content mismatch: %q", got.Content)
	}
	got2, err := s.GetNode(ctx, nFeature.ID)
	if err != nil {
		t.Fatalf("GetNode feature: %v", err)
	}
	if got2.Content != nFeature.Content {
		t.Fatalf("feature content mismatch: %q", got2.Content)
	}
}

func TestStage_ReingestOneStageLeavesOtherIntact(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature")

	nMain := store.Node{ID: main + "/x", ProjectID: main, NodeType: "generic-doc", Content: "v1"}
	nFeature := store.Node{ID: feature + "/x", ProjectID: feature, NodeType: "generic-doc", Content: "v1"}
	if err := s.UpsertNode(ctx, nMain); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(ctx, nFeature); err != nil {
		t.Fatal(err)
	}

	nMain.Content = "v2"
	if err := s.UpsertNode(ctx, nMain); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetNode(ctx, nFeature.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "v1" {
		t.Fatalf("sibling stage mutated: got content %q", got.Content)
	}
}

func TestStage_SearchScopedPerStage(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature")

	if err := s.UpsertNode(ctx, store.Node{ID: main + "/doc1", ProjectID: main, NodeType: "generic-doc", Title: "widget", Content: "widget lexical content"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(ctx, store.Node{ID: feature + "/doc2", ProjectID: feature, NodeType: "generic-doc", Title: "widget", Content: "widget lexical content"}); err != nil {
		t.Fatal(err)
	}

	mainResults, err := s.SearchLexical(ctx, main, "widget", 10)
	if err != nil {
		if err.Error() == "libsql: FTS5 not available (nodes_fts table missing)" {
			t.Skip("FTS5 not available in tursogo local mode, skipping lexical search test")
		}
		t.Fatal(err)
	}
	if len(mainResults) != 1 || mainResults[0].ID != main+"/doc1" {
		t.Fatalf("expected 1 result scoped to main, got %+v", mainResults)
	}

	legacyResults, err := s.SearchLexical(ctx, "repo-a", "widget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyResults) != 0 {
		t.Fatalf("legacy repo-a query should return zero staged rows, got %+v", legacyResults)
	}
}

func TestStage_ClearProjectIsolatesStage(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature")

	if err := s.UpsertNode(ctx, store.Node{ID: main + "/x", ProjectID: main, NodeType: "generic-doc", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(ctx, store.Node{ID: feature + "/x", ProjectID: feature, NodeType: "generic-doc", Content: "c"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ClearProject(ctx, main, ""); err != nil {
		t.Fatalf("ClearProject: %v", err)
	}

	if _, err := s.GetNode(ctx, main+"/x"); err == nil {
		t.Fatal("expected main node to be cleared")
	}
	if _, err := s.GetNode(ctx, feature+"/x"); err != nil {
		t.Fatalf("expected sibling stage to survive clear: %v", err)
	}
}

func TestStage_ListProjectsDecodesStaged(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	if err := s.UpsertNode(ctx, store.Node{ID: main + "/x", ProjectID: main, NodeType: "generic-doc", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(ctx, store.Node{ID: "legacy/x", ProjectID: "legacy", NodeType: "generic-doc", Content: "c"}); err != nil {
		t.Fatal(err)
	}

	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foundStaged, foundLegacy bool
	for _, p := range projects {
		if p.EffectiveProjectID == main {
			foundStaged = true
			if p.ProjectID != "repo-a" || p.Stage != "main" {
				t.Fatalf("staged project decoded wrong: %+v", p)
			}
		}
		if p.ProjectID == "legacy" {
			foundLegacy = true
			if p.Stage != "" || p.EffectiveProjectID != "" {
				t.Fatalf("legacy project should have empty stage fields: %+v", p)
			}
		}
	}
	if !foundStaged || !foundLegacy {
		t.Fatalf("expected both staged and legacy projects present: %+v", projects)
	}
}

func TestStage_UpsertEdgeRejectsMismatch(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature")

	fromNode := store.Node{ID: main + "/a", ProjectID: main, NodeType: "code:function", Content: "a"}
	toNode := store.Node{ID: feature + "/b", ProjectID: feature, NodeType: "code:function", Content: "b"}
	if err := s.UpsertNode(ctx, fromNode); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode(ctx, toNode); err != nil {
		t.Fatal(err)
	}

	err := s.UpsertEdge(ctx, store.Edge{FromID: fromNode.ID, ToID: toNode.ID, EdgeType: "calls"})
	if err == nil {
		t.Fatal("expected UpsertEdge to reject stage-mismatched edge")
	}
}

func TestStage_UpsertEdgesSkipsMismatchAndCommitsRest(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature")

	a := store.Node{ID: main + "/a", ProjectID: main, NodeType: "code:function", Content: "a"}
	b := store.Node{ID: feature + "/b", ProjectID: feature, NodeType: "code:function", Content: "b"}
	c := store.Node{ID: main + "/c", ProjectID: main, NodeType: "code:function", Content: "c"}
	for _, n := range []store.Node{a, b, c} {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	edges := []store.Edge{
		{FromID: a.ID, ToID: b.ID, EdgeType: "calls"}, // mismatched, should skip
		{FromID: a.ID, ToID: c.ID, EdgeType: "calls"}, // ok, should commit
	}
	if err := s.UpsertEdges(ctx, edges); err != nil {
		t.Fatalf("UpsertEdges should not error on skip: %v", err)
	}

	got, err := s.GetEdgesFrom(ctx, a.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ToID != c.ID {
		t.Fatalf("expected only a->c edge to survive, got %+v", got)
	}
}

func TestStage_UpsertEdgeAcceptsValidCombinations(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")

	unscoped1 := store.Node{ID: "legacy/a", ProjectID: "legacy", NodeType: "code:function", Content: "a"}
	unscoped2 := store.Node{ID: "legacy/b", ProjectID: "legacy", NodeType: "code:function", Content: "b"}
	staged1 := store.Node{ID: main + "/c", ProjectID: main, NodeType: "code:function", Content: "c"}
	staged2 := store.Node{ID: main + "/d", ProjectID: main, NodeType: "code:function", Content: "d"}
	for _, n := range []store.Node{unscoped1, unscoped2, staged1, staged2} {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	cases := []store.Edge{
		{FromID: unscoped1.ID, ToID: unscoped2.ID, EdgeType: "calls"},   // unscoped/unscoped
		{FromID: unscoped1.ID, ToID: staged1.ID, EdgeType: "calls"},     // unscoped/staged
		{FromID: staged1.ID, ToID: staged2.ID, EdgeType: "calls"},       // staged/staged same
		{FromID: staged1.ID, ToID: "does-not-exist", EdgeType: "calls"}, // dangling to_id
	}
	for i, e := range cases {
		if err := s.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("case %d: expected accept, got error: %v", i, err)
		}
	}
}

func TestStage_ReadFilteringHidesMismatchedEdge(t *testing.T) {
	s := openStageTest(t)
	ctx := context.Background()

	main := mustEncode(t, "repo-a", "main")
	feature := mustEncode(t, "repo-a", "feature")

	a := store.Node{ID: main + "/a", ProjectID: main, NodeType: "code:function", Content: "a"}
	b := store.Node{ID: feature + "/b", ProjectID: feature, NodeType: "code:function", Content: "b"}
	legacy1 := store.Node{ID: "legacy1/x", ProjectID: "legacy1", NodeType: "code:function", Content: "x"}
	legacy2 := store.Node{ID: "legacy2/y", ProjectID: "legacy2", NodeType: "code:function", Content: "y"}
	for _, n := range []store.Node{a, b, legacy1, legacy2} {
		if err := s.UpsertNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO edges (from_id, to_id, edge_type, confidence, confidence_tier, bidirectional) VALUES (?, ?, 'calls', 1.0, 'INFERRED', 0)`,
		a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO edges (from_id, to_id, edge_type, confidence, confidence_tier, bidirectional) VALUES (?, ?, 'calls', 1.0, 'INFERRED', 0)`,
		legacy1.ID, legacy2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO edges (from_id, to_id, edge_type, confidence, confidence_tier, bidirectional) VALUES (?, ?, 'calls', 1.0, 'INFERRED', 0)`,
		legacy1.ID, "does-not-exist"); err != nil {
		t.Fatal(err)
	}

	fromA, err := s.GetEdgesFrom(ctx, a.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromA) != 0 {
		t.Fatalf("GetEdgesFrom should hide stage-mismatched edge, got %+v", fromA)
	}

	toB, err := s.GetEdgesTo(ctx, b.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(toB) != 0 {
		t.Fatalf("GetEdgesTo should hide stage-mismatched edge, got %+v", toB)
	}

	fromLegacy1, err := s.GetEdgesFrom(ctx, legacy1.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromLegacy1) != 2 {
		t.Fatalf("expected legacy cross-project edge + dangling edge visible, got %+v", fromLegacy1)
	}

	nodes, edges, _, err := s.neighborsBFS(ctx, legacy1.ID, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = nodes
	if len(edges) != 2 {
		t.Fatalf("neighborsBFS should surface legacy + dangling edges, got %+v", edges)
	}

	results, total, err := s.ListEdgesByProject(ctx, "legacy1", nil, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("ListEdgesByProject expected 1 visible edge for legacy1, got %d (%+v)", total, results)
	}
}
