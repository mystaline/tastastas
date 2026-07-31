package docwalk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mystaline/tastastas/internal/store"
)

func TestLoadConfig(t *testing.T) {
	cfg, err := LoadConfig("testdata/acme-style/.memoryrc.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ProjectID != "acme-docs" {
		t.Fatalf("expected project_id acme-docs, got %q", cfg.ProjectID)
	}
	if len(cfg.Mappings) != 5 {
		t.Fatalf("expected 5 mappings, got %d", len(cfg.Mappings))
	}
}

func TestIngestWithConfig(t *testing.T) {
	cfg, err := LoadConfig("testdata/acme-style/.memoryrc.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	nodes, edges, _, _, err := Ingest("testdata/acme-style", cfg)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Expect: 4 coupon-redeem docs (prd, api-spec, erd, test-case) + 1
	// coupon-expiry prd = 5 nodes. README.md, .memoryrc.yaml match no
	// mapping and must be skipped.
	if len(nodes) != 5 {
		t.Fatalf("expected 5 nodes, got %d: %+v", len(nodes), nodeIDs(nodes))
	}

	byType := map[string]int{}
	for _, n := range nodes {
		byType[n.NodeType]++
		if n.ProjectID != "acme-docs" {
			t.Errorf("node %s: expected project_id acme-docs, got %q", n.ID, n.ProjectID)
		}
		if n.SourceAdapter != "docwalk" {
			t.Errorf("node %s: expected source_adapter docwalk, got %q", n.ID, n.SourceAdapter)
		}
		if n.ContentHash == "" {
			t.Errorf("node %s: expected non-empty content hash", n.ID)
		}
	}
	want := map[string]int{"prd": 2, "api-spec": 1, "erd": 1, "test-case": 1}
	for typ, count := range want {
		if byType[typ] != count {
			t.Errorf("expected %d nodes of type %s, got %d", count, typ, byType[typ])
		}
	}

	// Cross-link edges: only coupon-redeem has all 4 types, so exactly 3
	// edges expected (implements, tests, specifies), all pointing at its PRD.
	if len(edges) != 3 {
		t.Fatalf("expected 3 cross-link edges, got %d: %+v", len(edges), edges)
	}
	wantTypes := map[string]bool{"implements": false, "tests": false, "specifies": false}
	prdID := relatedPRD(nodes)
	for _, e := range edges {
		if _, ok := wantTypes[e.EdgeType]; !ok {
			t.Errorf("unexpected edge type %q", e.EdgeType)
			continue
		}
		wantTypes[e.EdgeType] = true
		if e.ToID != prdID {
			t.Errorf("edge %s: expected ToID %s (coupon-redeem PRD), got %s", e.EdgeType, prdID, e.ToID)
		}
		if e.Confidence != 1.0 {
			t.Errorf("edge %s: expected confidence 1.0, got %v", e.EdgeType, e.Confidence)
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Errorf("expected an edge of type %q, none found", typ)
		}
	}
}

func TestIngestWithoutConfig(t *testing.T) {
	// No mapping supplied -> every file becomes generic-doc, no cross-linking.
	nodes, edges, _, _, err := Ingest("testdata/acme-style", Config{ProjectID: "no-config"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges without mapping config, got %d", len(edges))
	}
	for _, n := range nodes {
		if n.NodeType != "generic-doc" {
			t.Errorf("node %s: expected generic-doc without config, got %q", n.ID, n.NodeType)
		}
	}
	// includes README.md and .memoryrc.yaml this time, since nothing is filtered
	if len(nodes) < 7 {
		t.Fatalf("expected at least 7 nodes (all files), got %d", len(nodes))
	}
}

func nodeIDs(nodes []store.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}

// TestIngestSkipsNoiseDirs proves large-directory ingest optimization: VCS
// internals, dependency trees, and build output are skipped entirely (not
// walked into), so a repo with node_modules/vendor/.git doesn't get any
// slower or noisier than one without.
func TestIngestSkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "keep.md", "# Keep\n\nThis should be ingested.\n")
	mustWriteFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	mustWriteFile(t, root, "node_modules/pkg/index.js", "module.exports = {}\n")
	mustWriteFile(t, root, "vendor/lib/lib.go", "package lib\n")
	mustWriteFile(t, root, "dist/bundle.js", "//! bundled\n")

	nodes, _, _, _, err := Ingest(root, Config{ProjectID: "skip-test"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node (keep.md only), got %d: %+v", len(nodes), nodeIDs(nodes))
	}
	if nodes[0].SourcePath != "keep.md" {
		t.Fatalf("expected keep.md, got %s", nodes[0].SourcePath)
	}
}

// TestIngestSkipsOversizedAndBinaryFiles proves the size cap and binary
// sniff both work: a file over maxFileSize and a file with embedded null
// bytes are both excluded even though nothing in path/glob rules would
// catch them.
func TestIngestSkipsOversizedAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "normal.md", "# Normal\n\nFine.\n")

	big := make([]byte, maxFileSize+1)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "huge.md"), big, 0o644); err != nil {
		t.Fatalf("write huge.md: %v", err)
	}

	binary := []byte("some text\x00with a null byte\x00in it")
	if err := os.WriteFile(filepath.Join(root, "binaryish.md"), binary, 0o644); err != nil {
		t.Fatalf("write binaryish.md: %v", err)
	}

	nodes, _, _, _, err := Ingest(root, Config{ProjectID: "size-test"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node (normal.md only), got %d: %+v", len(nodes), nodeIDs(nodes))
	}
	if nodes[0].SourcePath != "normal.md" {
		t.Fatalf("expected normal.md, got %s", nodes[0].SourcePath)
	}
}

func mustWriteFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// relatedPRD is a test-only helper: with only one prd sharing the
// "coupon-redeem" feature slug, find it by matching source path prefix.
func relatedPRD(nodes []store.Node) string {
	for _, n := range nodes {
		if n.NodeType == "prd" && strings.Contains(n.SourcePath, "coupon-redeem") {
			return n.ID
		}
	}
	return ""
}
