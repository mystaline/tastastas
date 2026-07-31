// Package gitrepo ingests MEMORY.md-like files from a directory tree of git
// repositories. Walks subdirectories, finds files matching a configurable glob
// (default: MEMORY.md), and creates generic-doc nodes with IDs derived from
// the directory structure.
package gitrepo

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mystaline/tastastas/internal/store"
)

// Config holds gitrepo ingestion settings.
type Config struct {
	// Root is the directory tree to walk (e.g. ~/Workspace/)
	Root string

	// ProjectID scopes ingested nodes to this project.
	ProjectID string

	// Glob is the file pattern to match (default: "**/MEMORY.md").
	// Supports ** for recursive matching.
	Glob string
}

// Ingest walks the root directory, finds files matching the glob pattern,
// and returns nodes. Each node's content is the full file content, ID is
// derived from the relative path.
func Ingest(cfg Config) ([]store.Node, error) {
	if cfg.Glob == "" {
		cfg.Glob = "MEMORY.md"
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = "default"
	}

	var nodes []store.Node

	err := filepath.WalkDir(cfg.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}

		base := filepath.Base(path)
		matched, _ := filepath.Match(cfg.Glob, base)
		if !matched {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		// Derive ID from relative path: ~/Workspace/foo/bar/MEMORY.md → repo/bar/memory
		rel, _ := filepath.Rel(cfg.Root, path)
		rel = filepath.ToSlash(rel)
		id := pathToID(cfg.ProjectID, rel)

		nodes = append(nodes, store.Node{
			ID:            id,
			ProjectID:     cfg.ProjectID,
			NodeType:      "generic-doc",
			Title:         deriveTitle(rel),
			Content:       string(content),
			ContentHash:   fmt.Sprintf("%x", sha256.Sum256(content)),
			Status:        "current",
			SourceAdapter: "gitrepo",
			SourcePath:    rel,
			Importance:    0.5,
		})

		return nil
	})

	return nodes, err
}

// pathToID converts a relative file path to a qualified node ID, using the
// full parent directory path (not just the top-level segment) to avoid
// collisions when multiple matching files exist at different depths under
// the same top-level directory (e.g. "repoA/MEMORY.md" and
// "repoA/sub/MEMORY.md" must not both map to the same node).
// Example: "sandbox/cmd/server/MEMORY.md" -> "project/sandbox/cmd/server/memory"
func pathToID(projectID, rel string) string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return fmt.Sprintf("%s/memory", projectID)
	}
	return fmt.Sprintf("%s/%s/memory", projectID, dir)
}

// deriveTitle extracts a readable title from the relative path.
func deriveTitle(rel string) string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return "memory"
	}
	return dir + " memory"
}
