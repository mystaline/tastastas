package obsidian

import (
	"testing"
)

func TestIngestObsidian(t *testing.T) {
	nodes, edges, err := Ingest(Config{
		Root:      "testdata/vault",
		ProjectID: "test-vault",
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}

	// Check node types from frontmatter
	typeMap := map[string]string{}
	for _, n := range nodes {
		typeMap[n.Title] = n.NodeType
		t.Logf("node: %s type=%s title=%q", n.ID, n.NodeType, n.Title)
	}

	if typeMap["Architecture Overview"] != "architecture-decision" {
		t.Errorf("expected 'architecture-decision', got %q", typeMap["Architecture Overview"])
	}
	if typeMap["Database Design"] != "erd" {
		t.Errorf("expected 'erd', got %q", typeMap["Database Design"])
	}
	if typeMap["API Specification"] != "api-spec" {
		t.Errorf("expected 'api-spec', got %q", typeMap["API Specification"])
	}
	if typeMap["Random Note"] != "generic-doc" {
		t.Errorf("expected 'generic-doc' (no type in frontmatter), got %q", typeMap["Random Note"])
	}

	// Check wikilink edges
	if len(edges) < 5 {
		t.Errorf("expected at least 5 wikilink edges, got %d", len(edges))
	}
	for _, e := range edges {
		if e.EdgeType != "related" {
			t.Errorf("expected edge_type 'related', got %q", e.EdgeType)
		}
		if e.Confidence != 0.8 {
			t.Errorf("expected confidence 0.8, got %f", e.Confidence)
		}
		t.Logf("edge: %s -> %s [%s]", e.FromID, e.ToID, e.EdgeType)
	}

	// Architecture Overview has 2 wikilinks → 2 edges
	archEdges := 0
	for _, e := range edges {
		if e.FromID == "test-vault/obsidian/Architecture Overview" {
			archEdges++
		}
	}
	if archEdges != 2 {
		t.Errorf("expected 2 outgoing edges from Architecture Overview, got %d", archEdges)
	}
}

func TestParseFrontmatter(t *testing.T) {
	input := "---\nname: Test\ntype: fact\n---\n\nBody content here."
	fm, body := parseFrontmatter(input)
	if fm.Name != "Test" || fm.Type != "fact" {
		t.Errorf("unexpected frontmatter: %+v", fm)
	}
	if body != "Body content here." {
		t.Errorf("unexpected body: %q", body)
	}
}

func TestParseFrontmatterNone(t *testing.T) {
	input := "Just plain markdown with no frontmatter."
	fm, body := parseFrontmatter(input)
	if fm.Name != "" || fm.Type != "" {
		t.Errorf("expected empty frontmatter, got %+v", fm)
	}
	if body != input {
		t.Errorf("expected full text as body")
	}
}
