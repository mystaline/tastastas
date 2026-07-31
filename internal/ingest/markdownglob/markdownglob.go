// Package markdownglob ingests all *.md files as generic-doc nodes.
// Fallback adapter for markdown content not covered by other adapters.
package markdownglob

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mystaline/tastastas/internal/store"
)

// Config holds markdown-glob ingestion settings.
type Config struct {
	// Root is the directory tree to walk.
	Root string

	// ProjectID scopes ingested nodes to this project.
	ProjectID string
}

// Ingest walks the root directory and creates generic-doc nodes for all *.md files.
// Heuristic: path containing /adr/ → architecture-decision, /design/ → visual-design, else generic-doc.
func Ingest(cfg Config) ([]store.Node, error) {
	if cfg.ProjectID == "" {
		cfg.ProjectID = "default"
	}

	var nodes []store.Node

	err := filepath.WalkDir(cfg.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}

		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".venv" || d.Name() == ".obsidian" {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(cfg.Root, path)
		rel = filepath.ToSlash(rel)

		nodeType := "generic-doc"
		if strings.Contains(strings.ToLower(rel), "/adr/") || strings.Contains(strings.ToLower(rel), "/adrs/") {
			nodeType = "architecture-decision"
		} else if strings.Contains(strings.ToLower(rel), "/design/") || strings.Contains(strings.ToLower(rel), "/visual/") || strings.Contains(strings.ToLower(rel), "/ui/") {
			nodeType = "visual-design"
		}

		id := cfg.ProjectID + "/" + strings.TrimSuffix(rel, ".md")

		nodes = append(nodes, store.Node{
			ID:            id,
			ProjectID:     cfg.ProjectID,
			NodeType:      nodeType,
			Title:         filepath.Base(rel),
			Content:       string(content),
			ContentHash:   fmt.Sprintf("%x", sha256.Sum256(content)),
			Status:        "current",
			SourceAdapter: "markdown-glob",
			SourcePath:    rel,
			Importance:    0.5,
		})

		return nil
	})

	return nodes, err
}
