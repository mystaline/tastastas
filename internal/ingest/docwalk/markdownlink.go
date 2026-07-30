package docwalk

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mystaline-dev/tastastas/internal/store"
)

var (
	inlineLinkRe  = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	wikiLinkRe    = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	refLinkRe     = regexp.MustCompile(`(?m)^\[([^\]]+)\]:\s*(\S+)`)
)

// ExtractMarkdownLinks parses .md files in root and creates references edges
// between docs for resolved internal links. Only emits edges whose target
// node ID exists in nodeSet.
func ExtractMarkdownLinks(root, projectID string, nodeSet map[string]bool) []store.Edge {
	var edges []store.Edge

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") && !strings.HasSuffix(d.Name(), ".mdx") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		srcID := fmt.Sprintf("%s/%s", projectID, strings.TrimSuffix(filepath.ToSlash(rel), ".md"))

		src := string(data)
		links := extractLinks(src)
		for _, target := range links {
			resolved := resolveDocLink(target, rel)
			if resolved == "" {
				continue
			}
			targetID := fmt.Sprintf("%s/%s", projectID, strings.TrimSuffix(filepath.ToSlash(resolved), ".md"))
			if nodeSet[targetID] {
				edges = append(edges, store.Edge{
					FromID:         srcID,
					ToID:           targetID,
					EdgeType:       "references",
					Confidence:     1.0,
					ConfidenceTier: "EXTRACTED",
				})
			}
		}
		return nil
	})

	return edges
}

// extractLinks extracts all markdown link targets (inline, wikilink, reference-style)
// from source text, excluding external URLs.
func extractLinks(src string) []string {
	var links []string
	seen := map[string]bool{}

	for _, m := range inlineLinkRe.FindAllStringSubmatch(src, -1) {
		target := strings.TrimSpace(m[2])
		target = strings.Split(target, "#")[0] // strip anchor
		target = strings.Split(target, "?")[0] // strip query
		if isValidDocLink(target) && !seen[target] {
			seen[target] = true
			links = append(links, target)
		}
	}

	for _, m := range wikiLinkRe.FindAllStringSubmatch(src, -1) {
		target := strings.TrimSpace(m[1])
		target = strings.Split(target, "|")[0] // strip display text
		target = strings.Split(target, "#")[0] // strip anchor
		if !seen[target] {
			seen[target] = true
			links = append(links, target)
		}
	}

	for _, m := range refLinkRe.FindAllStringSubmatch(src, -1) {
		target := strings.TrimSpace(m[2])
		target = strings.Split(target, "#")[0]
		target = strings.Split(target, "?")[0]
		if isValidDocLink(target) && !seen[target] {
			seen[target] = true
			links = append(links, target)
		}
	}

	return links
}

// isValidDocLink returns true if target is an internal doc link (not external,
// not an anchor-only, not an image, not a data/mailto URI).
func isValidDocLink(target string) bool {
	if target == "" {
		return false
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return false
	}
	if strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "data:") {
		return false
	}
	if strings.HasPrefix(target, "tel:") {
		return false
	}
	if strings.HasPrefix(target, "//") {
		return false
	}
	if strings.HasPrefix(target, "#") {
		return false
	}
	if strings.HasPrefix(target, "!") {
		return false
	}
	return true
}

// resolveDocLink resolves a markdown link target relative to the source file.
func resolveDocLink(target, sourceRel string) string {
	if strings.HasPrefix(target, "/") {
		// Absolute relative to root
		return strings.TrimPrefix(target, "/")
	}
	dir := filepath.Dir(sourceRel)
	resolved := filepath.Join(dir, target)
	resolved = filepath.Clean(resolved)
	return resolved
}
