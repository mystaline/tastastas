package onboard

import (
	"testing"

	"github.com/mystaline-dev/tastastas/internal/store"
)

func TestBuildHierarchy_FlatDocProject(t *testing.T) {
	nodes := []store.Node{
		{ID: "proj/Acme/Alpha/PRD/rbac/00-index", NodeType: "prd", SourcePath: "Acme/Alpha/PRD/rbac/00-index.md"},
		{ID: "proj/Acme/Alpha/PRD/rbac/03-schema", NodeType: "prd-detail", SourcePath: "Acme/Alpha/PRD/rbac/03-schema.md"},
		{ID: "proj/Acme/Alpha/PRD/identity/00-index", NodeType: "prd", SourcePath: "Acme/Alpha/PRD/identity/00-index.md"},
		{ID: "proj/Acme/Charlie/PRD/resource/00-index", NodeType: "prd", SourcePath: "Acme/Charlie/PRD/resource/00-index.md"},
	}

	hierNodes, hierEdges := BuildHierarchy("proj", nodes)

	nodeByID := map[string]store.Node{}
	for _, n := range hierNodes {
		nodeByID[n.ID] = n
	}

	rootID := "proj/dir/"
	if _, ok := nodeByID[rootID]; !ok {
		t.Fatalf("repo-root node missing; got nodes: %+v", hierNodes)
	}

	tagID := "proj/dir/Acme"
	if _, ok := nodeByID[tagID]; !ok {
		t.Fatalf("Acme dir node missing")
	}

	// Verify contains-edge chain from root down to a leaf exists.
	edgeSet := map[string]bool{}
	for _, e := range hierEdges {
		if e.EdgeType != "contains" {
			t.Errorf("unexpected edge type %q, want contains", e.EdgeType)
		}
		edgeSet[e.FromID+"->"+e.ToID] = true
	}

	if !edgeSet[rootID+"->"+tagID] {
		t.Errorf("expected contains edge %s -> %s", rootID, tagID)
	}

	// Ensure no cycle: no edge points back to root or to itself.
	for _, e := range hierEdges {
		if e.ToID == rootID {
			t.Errorf("cycle detected: edge %s -> %s points back to root", e.FromID, e.ToID)
		}
		if e.FromID == e.ToID {
			t.Errorf("self-loop detected: %s -> %s", e.FromID, e.ToID)
		}
	}

	// A leaf node must be reachable via some contains edge.
	leafReachable := false
	for _, e := range hierEdges {
		if e.ToID == "proj/Acme/Alpha/PRD/rbac/00-index" {
			leafReachable = true
		}
	}
	if !leafReachable {
		t.Errorf("expected leaf node to be contained by some directory node")
	}
}

func TestBuildHierarchy_CollapsesPassThrough(t *testing.T) {
	// Acme has two modules (Alpha, Bravo) so it never
	// collapses. Bravo/PRD/single-feature is an unbroken single-child chain
	// (Bravo has 1 child + 0 leaves, PRD has 1 child + 0 leaves) — both
	// levels collapse per the fixed-point rule, so Acme links
	// directly to single-feature, skipping both Bravo and PRD as nodes.
	nodes := []store.Node{
		{ID: "proj/Acme/Alpha/PRD/rbac/00-index", NodeType: "prd", SourcePath: "Acme/Alpha/PRD/rbac/00-index.md"},
		{ID: "proj/Acme/Bravo/PRD/single-feature/00-index", NodeType: "prd", SourcePath: "Acme/Bravo/PRD/single-feature/00-index.md"},
	}

	hierNodes, hierEdges := BuildHierarchy("proj", nodes)

	nodeByID := map[string]store.Node{}
	for _, n := range hierNodes {
		nodeByID[n.ID] = n
	}

	tagID := "proj/dir/Acme"
	if _, ok := nodeByID[tagID]; !ok {
		t.Fatalf("Acme should survive (2 children: Alpha, Bravo), not present in %+v", hierNodes)
	}

	soloID := "proj/dir/Acme/Bravo"
	prdID := "proj/dir/Acme/Bravo/PRD"
	if _, ok := nodeByID[soloID]; ok {
		t.Errorf("expected Bravo pass-through folder to collapse (chained into PRD collapse), but it exists as a node")
	}
	if _, ok := nodeByID[prdID]; ok {
		t.Errorf("expected PRD pass-through folder to collapse, but it exists as a node")
	}

	featureID := "proj/dir/Acme/Bravo/PRD/single-feature"
	found := false
	for _, e := range hierEdges {
		if e.FromID == tagID && e.ToID == featureID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected collapsed edge %s -> %s (skipping Bravo and PRD), got edges: %+v", tagID, featureID, hierEdges)
	}
}

func TestBuildHierarchy_SkipsCodePackageNodes(t *testing.T) {
	// internal/ has two children (store, retrieve) so it survives the
	// collapse and gives us a stable node to assert on.
	nodes := []store.Node{
		{ID: "proj/code:package/github.com/org/repo/internal/store", NodeType: "code:package", SourcePath: "github.com/org/repo/internal/store"},
		{ID: "proj/code:function/internal/store.Get", NodeType: "code:function", SourcePath: "internal/store/store.go"},
		{ID: "proj/code:type/internal/store.Node", NodeType: "code:type", SourcePath: "internal/store/store.go"},
		{ID: "proj/code:function/internal/retrieve.Query", NodeType: "code:function", SourcePath: "internal/retrieve/retrieve.go"},
	}

	hierNodes, hierEdges := BuildHierarchy("proj", nodes)

	nodeByID := map[string]store.Node{}
	for _, n := range hierNodes {
		nodeByID[n.ID] = n
	}

	// No phantom branch from the Go import path should exist.
	for id := range nodeByID {
		if id == "proj/dir/github.com" {
			t.Errorf("phantom hierarchy branch created from code:package import path: %s", id)
		}
	}

	// Real file-path tree for code:function/code:type must still exist:
	// internal/ has two children (store, retrieve) so it does not collapse.
	internalID := "proj/dir/internal"
	if _, ok := nodeByID[internalID]; !ok {
		t.Errorf("expected real file-path dir node %q from code:function/code:type SourcePath", internalID)
	}
	storeID := "proj/dir/internal/store"
	if _, ok := nodeByID[storeID]; !ok {
		t.Errorf("expected real file-path dir node %q", storeID)
	}

	// The code:package node itself must not appear as a leaf in any edge.
	for _, e := range hierEdges {
		if e.ToID == "proj/code:package/github.com/org/repo/internal/store" {
			t.Errorf("code:package node should never be linked into the hierarchy, found edge to it: %+v", e)
		}
	}
}

func TestBuildHierarchy_EmptyNodes(t *testing.T) {
	hierNodes, hierEdges := BuildHierarchy("proj", nil)

	if len(hierNodes) != 1 {
		t.Fatalf("expected exactly 1 node (repo root) for empty input, got %d: %+v", len(hierNodes), hierNodes)
	}
	if hierNodes[0].ID != "proj/dir/" {
		t.Errorf("expected repo-root ID 'proj/dir/', got %q", hierNodes[0].ID)
	}
	if len(hierEdges) != 0 {
		t.Errorf("expected zero edges for empty input, got %d", len(hierEdges))
	}
}

func TestBuildHierarchy_DirectoryNodeFields(t *testing.T) {
	nodes := []store.Node{
		{ID: "proj/a/b/doc", NodeType: "generic-doc", SourcePath: "a/b/doc.md"},
	}
	hierNodes, _ := BuildHierarchy("proj", nodes)

	for _, n := range hierNodes {
		if n.NodeType != "directory" {
			t.Errorf("hierarchy node %s has NodeType %q, want %q", n.ID, n.NodeType, "directory")
		}
		if n.Content != "" {
			t.Errorf("hierarchy node %s has non-empty Content %q, want empty (must not be embedded)", n.ID, n.Content)
		}
		if n.SourceAdapter != "hierarchy" {
			t.Errorf("hierarchy node %s has SourceAdapter %q, want %q", n.ID, n.SourceAdapter, "hierarchy")
		}
	}
}
