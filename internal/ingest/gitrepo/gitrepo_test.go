package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIngestGitrepo(t *testing.T) {
	// Create temp dir structure
	root := t.TempDir()
	dirs := []string{"project-a/cmd/server", "project-b/internal/store"}
	for _, d := range dirs {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}

	// Write MEMORY.md files
	memories := map[string]string{
		"project-a/MEMORY.md":      "# Project A\nMain service for billing.",
		"project-a/cmd/server/MEMORY.md": "# Server\nEntry point for the API server.",
		"project-b/MEMORY.md":      "# Project B\nData store service.",
	}
	for path, content := range memories {
		os.WriteFile(filepath.Join(root, path), []byte(content), 0o644)
	}

	// Also write a non-MEMORY file (should be ignored)
	os.WriteFile(filepath.Join(root, "project-a/README.md"), []byte("readme"), 0o644)

	nodes, err := Ingest(Config{Root: root, ProjectID: "test"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	// Regression: project-a/MEMORY.md and project-a/cmd/server/MEMORY.md
	// must NOT collide on the same node ID (old pathToID used only the
	// top-level directory segment, silently merging nested MEMORY.md files
	// at upsert time even though this in-memory slice still had 3 items).
	seenIDs := map[string]bool{}
	for _, n := range nodes {
		if seenIDs[n.ID] {
			t.Fatalf("duplicate node ID %q — nested MEMORY.md files collided", n.ID)
		}
		seenIDs[n.ID] = true
	}

	// Verify node properties
	for _, n := range nodes {
		if n.ProjectID != "test" {
			t.Errorf("expected project_id 'test', got %q", n.ProjectID)
		}
		if n.NodeType != "generic-doc" {
			t.Errorf("expected node_type 'generic-doc', got %q", n.NodeType)
		}
		if n.SourceAdapter != "gitrepo" {
			t.Errorf("expected source_adapter 'gitrepo', got %q", n.SourceAdapter)
		}
		if n.ContentHash == "" {
			t.Error("expected non-empty content_hash")
		}
		t.Logf("node: %s title=%q", n.ID, n.Title)
	}
}

func TestIngestGitrepoSkipsGitDirs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "repo/.git/objects"), 0o755)
	os.WriteFile(filepath.Join(root, "repo/MEMORY.md"), []byte("content"), 0o644)
	os.WriteFile(filepath.Join(root, "repo/.git/config"), []byte("git config"), 0o644)

	nodes, err := Ingest(Config{Root: root})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node (should skip .git/), got %d", len(nodes))
	}
}
