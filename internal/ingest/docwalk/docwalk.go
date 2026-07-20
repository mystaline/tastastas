// Package docwalk is a config-driven document ingestion adapter: walk a root
// directory, map file paths to canonical node types via glob patterns
// (never hardcoded folder conventions), and optionally cross-link documents
// that share a captured "feature" slug across types (e.g. a PRD, its
// api-spec, its erd, and its test-case all named for the same feature).
//
// No specific team's directory layout is assumed. A `.memoryrc.yaml` config
// supplies the mapping; without one, everything ingests as `generic-doc`
// with no cross-linking (still searchable/embeddable, just untyped).
package docwalk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/goccy/go-yaml"

	"github.com/mystaline-dev/tastastas/internal/store"
)

// Mapping maps files matching PathGlob to NodeType, optionally capturing a
// "feature" slug (via GroupBy, a regex with a named capture group) used to
// auto cross-link documents of different types that describe the same
// feature.
type Mapping struct {
	PathGlob string `yaml:"path_glob"`
	Type     string `yaml:"type"`
	GroupBy  string `yaml:"group_by,omitempty"` // regex against the file path, must have a named group (?P<feature>...)
}

// Config is the parsed shape of a `.memoryrc.yaml` file.
type Config struct {
	ProjectID string    `yaml:"project_id"`
	Mappings  []Mapping `yaml:"mappings"`
}

// LoadConfig reads and parses a .memoryrc.yaml file at path.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("docwalk: read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("docwalk: parse config %s: %w", path, err)
	}
	if cfg.ProjectID == "" {
		cfg.ProjectID = "default"
	}
	return cfg, nil
}

// edgeRule maps (fromType) -> (edgeType, toType) for auto cross-linking
// documents that share a feature slug. Direction: fromType --edgeType--> toType.
var edgeRule = map[string]struct {
	EdgeType string
	ToType   string
}{
	"api-spec":  {"implements", "prd"},
	"test-case": {"tests", "prd"},
	"erd":       {"specifies", "prd"},
}

// Ingest walks root, matches every file against cfg.Mappings (first match
// wins), and returns typed nodes plus any feature-slug cross-link edges.
// Files matching no mapping are skipped entirely if cfg has mappings
// configured; if cfg has zero mappings, every regular file becomes a
// generic-doc node (the ungated default: point it at any folder, get
// something searchable, no config required).
func Ingest(root string, cfg Config) ([]store.Node, []store.Edge, error) {
	var nodes []store.Node
	featureIndex := map[string]map[string]string{} // feature -> nodeType -> nodeID

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		nodeType, feature, matched := classify(rel, cfg.Mappings)
		if !matched {
			if len(cfg.Mappings) > 0 {
				return nil // mappings configured but this file matched none — skip
			}
			nodeType = "generic-doc" // no config at all: everything is generic-doc
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("docwalk: read %s: %w", path, err)
		}
		id := nodeID(cfg.ProjectID, rel)
		hash := sha256.Sum256(content)

		n := store.Node{
			ID:            id,
			ProjectID:     cfg.ProjectID,
			NodeType:      nodeType,
			Title:         filepath.Base(rel),
			Content:       string(content),
			ContentHash:   hex.EncodeToString(hash[:]),
			Status:        "current",
			Importance:    0.5,
			SourceAdapter: "docwalk",
			SourcePath:    rel,
		}
		nodes = append(nodes, n)

		if feature != "" {
			if featureIndex[feature] == nil {
				featureIndex[feature] = map[string]string{}
			}
			featureIndex[feature][nodeType] = id
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("docwalk: walk %s: %w", root, err)
	}

	edges := crossLink(featureIndex)
	return nodes, edges, nil
}

// classify finds the first mapping whose PathGlob matches rel, returns its
// node type and (if GroupBy set) the captured "feature" slug.
func classify(rel string, mappings []Mapping) (nodeType, feature string, matched bool) {
	for _, m := range mappings {
		ok, err := doublestar.Match(m.PathGlob, rel)
		if err != nil || !ok {
			continue
		}
		f := ""
		if m.GroupBy != "" {
			if re, err := regexp.Compile(m.GroupBy); err == nil {
				if sub := re.FindStringSubmatch(rel); sub != nil {
					if idx := re.SubexpIndex("feature"); idx >= 0 && idx < len(sub) {
						f = sub[idx]
					}
				}
			}
		}
		return m.Type, f, true
	}
	return "", "", false
}

// crossLink emits edges between nodes that share a feature slug, per edgeRule.
func crossLink(featureIndex map[string]map[string]string) []store.Edge {
	var edges []store.Edge
	for _, byType := range featureIndex {
		for fromType, rule := range edgeRule {
			fromID, ok := byType[fromType]
			if !ok {
				continue
			}
			toID, ok := byType[rule.ToType]
			if !ok {
				continue
			}
			edges = append(edges, store.Edge{
				FromID:     fromID,
				ToID:       toID,
				EdgeType:   rule.EdgeType,
				Confidence: 1.0, // config/convention-derived, authoritative
			})
		}
	}
	return edges
}

func nodeID(projectID, relPath string) string {
	slug := strings.TrimSuffix(relPath, filepath.Ext(relPath))
	return projectID + "/" + slug
}
