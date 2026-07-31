// Package obsidian ingests an Obsidian vault into typed nodes and edges.
// Parses YAML frontmatter (name, type, metadata.*) and [[wikilink]] syntax
// to create nodes with typed edges. Generic — works on any vault structure.
package obsidian

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/mystaline/tastastas/internal/store"
)

// Config holds obsidian ingestion settings.
type Config struct {
	// Root is the vault root directory.
	Root string

	// ProjectID scopes ingested nodes to this project.
	ProjectID string
}

// frontmatter represents the YAML frontmatter of an Obsidian note.
type frontmatter struct {
	Name     string            `yaml:"name"`
	Type     string            `yaml:"type"`
	Metadata map[string]string `yaml:"metadata"`
}

// Ingest walks an Obsidian vault and returns typed nodes and edges.
func Ingest(cfg Config) ([]store.Node, []store.Edge, error) {
	if cfg.ProjectID == "" {
		cfg.ProjectID = "default"
	}

	var nodes []store.Node
	var edges []store.Edge
	wikilinkRe := regexp.MustCompile(`\[\[([^\]|]+?)(?:\|[^\]]+)?\]\]`)

	err := filepath.WalkDir(cfg.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".obsidian" || d.Name() == ".git" || d.Name() == ".trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		text := string(content)
		rel, _ := filepath.Rel(cfg.Root, path)
		rel = filepath.ToSlash(rel)
		fm, body := parseFrontmatter(text)

		nodeType := "generic-doc"
		title := strings.TrimSuffix(d.Name(), ".md")
		if fm.Type != "" {
			nodeType = fm.Type
		}
		if fm.Name != "" {
			title = fm.Name
		}

		id := fmt.Sprintf("%s/obsidian/%s", cfg.ProjectID, strings.TrimSuffix(rel, ".md"))

		nodes = append(nodes, store.Node{
			ID:            id,
			ProjectID:     cfg.ProjectID,
			NodeType:      nodeType,
			Title:         title,
			Content:       body,
			ContentHash:   fmt.Sprintf("%x", sha256.Sum256(content)),
			Status:        "current",
			SourceAdapter: "obsidian",
			SourcePath:    rel,
			Importance:    0.5,
		})

		// Extract [[wikilinks]] as "related" edges
		matches := wikilinkRe.FindAllStringSubmatch(body, -1)
		for _, m := range matches {
			targetName := strings.TrimSpace(m[1])
			targetID := fmt.Sprintf("%s/obsidian/%s", cfg.ProjectID, targetName)
			edges = append(edges, store.Edge{
				FromID:     id,
				ToID:       targetID,
				EdgeType:   "related",
				Confidence: 0.8, // wikilinks are author-declared but not typed
			})
		}

		return nil
	})

	return nodes, edges, err
}

// parseFrontmatter extracts YAML frontmatter and the remaining body.
// If no frontmatter exists, returns empty frontmatter and full text.
func parseFrontmatter(text string) (frontmatter, string) {
	if !strings.HasPrefix(text, "---") {
		return frontmatter{}, text
	}

	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return frontmatter{}, text
	}

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		return frontmatter{}, text
	}

	return fm, strings.TrimSpace(parts[2])
}
