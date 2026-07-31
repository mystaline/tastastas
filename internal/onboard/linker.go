package onboard

import (
	"context"
	"strings"

	"github.com/mystaline/tastastas/internal/ingest/codeast/treesitter"
	"github.com/mystaline/tastastas/internal/store"
)

// extractModuleRoot derives the module root from a package path.
// Go: domain/org/repo/subpkg → domain/org/repo (first 3 /-segments).
// NPM scoped: @scope/pkg/sub → @scope/pkg (first 2 /-segments).
// Bare packages: entire path.
func extractModuleRoot(pkgPath string) string {
	if strings.HasPrefix(pkgPath, "@") {
		parts := strings.SplitN(pkgPath, "/", 3)
		if len(parts) >= 3 {
			return strings.Join(parts[:2], "/")
		}
		return pkgPath
	}
	parts := strings.SplitN(pkgPath, "/", 4)
	if len(parts) >= 4 {
		return strings.Join(parts[:3], "/")
	}
	return pkgPath
}

// extractPkgPath extracts the package path from a code:package node ID.
// Input:  "proj/code:package/github.com/org/repo/pkg"
// Output: "github.com/org/repo/pkg"
func extractPkgPath(nodeID string) string {
	_, after, ok := strings.Cut(nodeID, "/code:package/")
	if !ok {
		return ""
	}
	return after
}

// buildModulePkgIndex builds a module-root → representative node ID map
// from code:package nodes, using the package path from the node ID.
func buildModulePkgIndex(nodes []store.Node) map[string]string {
	idx := map[string]string{}
	for _, n := range nodes {
		if n.NodeType == "code:package" && n.Title != "" {
			pkgPath := extractPkgPath(n.ID)
			if pkgPath == "" {
				continue
			}
			modRoot := extractModuleRoot(pkgPath)
			if _, exists := idx[modRoot]; !exists {
				idx[modRoot] = n.ID
			}
		}
	}
	return idx
}

type labelEntry struct {
	nodeID   string
	language string
	fileID   string // code:file node ID for this symbol's source file
}

// godNodeGuard skips names that match too many nodes (common names like
// "handle", "render", "init" that would produce false positives).
const godNodeGuard = 10

// crossProjectGodGuard skips names that appear in too many other projects.
const crossProjectGodGuard = 20

// ResolveCrossFileCalls resolves raw_calls from tree-sitter extraction using
// a global label index built from all ingested nodes. Returns resolved edges.
func ResolveCrossFileCalls(
	allNodes []store.Node,
	rawCalls []treesitter.RawCall,
	existingEdges []store.Edge,
) []store.Edge {
	if len(rawCalls) == 0 {
		return nil
	}

	// Build label index: normalizedName → []{nodeID, language}
	labelIndex := map[string][]labelEntry{}
	_ = labelIndex

	for _, n := range allNodes {
		if !isCodeSymbol(n.NodeType) {
			continue
		}
		normalized := n.Title
		normalized = strings.TrimSuffix(normalized, "()")
		normalized = strings.TrimPrefix(normalized, ".")
		if normalized == "" {
			continue
		}
		fileID := ""
		if n.ProjectID != "" && n.SourcePath != "" {
			fileID = n.ProjectID + "/code:file/" + n.SourcePath
		}
		labelIndex[normalized] = append(labelIndex[normalized], labelEntry{
			nodeID:   n.ID,
			language: n.Language,
			fileID:   fileID,
		})
	}

	// Build import evidence map: sourceFile → []targetFile
	importMap := buildImportMap(existingEdges)

	var resolved []store.Edge
	for _, rc := range rawCalls {
		candidates := labelIndex[rc.CalleeName]
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) > godNodeGuard {
			continue // god-node guard: too many matches
		}

		// Filter by language (cross-language guard)
		var matching []labelEntry
		for _, c := range candidates {
			if c.language == rc.Lang {
				matching = append(matching, c)
			}
		}
		if len(matching) == 0 {
			continue
		}
		if len(matching) == 1 {
			// Single candidate
			if isSameFile(rc.SourceFile, matching[0].fileID) {
				continue // already resolved in same-file pass
			}
			// JS/TS guard: cross-file call MUST have import evidence
			if rc.Lang == "typescript" || rc.Lang == "javascript" || rc.Lang == "ts" || rc.Lang == "js" {
				if fileEdges := importMap[rc.CallerNodeID]; len(fileEdges) > 0 && fileEdges[matching[0].fileID] {
					resolved = append(resolved, store.Edge{
						FromID:         rc.CallerNodeID,
						ToID:           matching[0].nodeID,
						EdgeType:       "calls",
						Confidence:     1.0,
						ConfidenceTier: "EXTRACTED",
					})
				}
			} else {
				resolved = append(resolved, store.Edge{
					FromID:         rc.CallerNodeID,
					ToID:           matching[0].nodeID,
					EdgeType:       "calls",
					Confidence:     0.8,
					ConfidenceTier: "INFERRED",
				})
			}
			continue
		}

		// Multiple candidates: use import evidence
		found := resolveViaImports(rc, matching, importMap)
		if found != "" {
			resolved = append(resolved, store.Edge{
				FromID:         rc.CallerNodeID,
				ToID:           found,
				EdgeType:       "calls",
				Confidence:     1.0,
				ConfidenceTier: "EXTRACTED",
			})
		}
	}

	return resolved
}

func isCodeSymbol(nodeType string) bool {
	return nodeType == "code:function" || nodeType == "code:method" || nodeType == "code:type"
}

func isSameFile(sourceFile string, fileID string) bool {
	if fileID == "" || sourceFile == "" {
		return false
	}
	return strings.HasSuffix(fileID, "/code:file/"+sourceFile)
}

func buildImportMap(edges []store.Edge) map[string]map[string]bool {
	m := make(map[string]map[string]bool)
	for _, e := range edges {
		if e.EdgeType == "imports" {
			if m[e.FromID] == nil {
				m[e.FromID] = make(map[string]bool)
			}
			m[e.FromID][e.ToID] = true
		}
	}
	return m
}

func resolveViaImports(rc treesitter.RawCall, candidates []labelEntry, importMap map[string]map[string]bool) string {
	callerFile := rc.CallerNodeID
	fileEdges := importMap[callerFile]
	if len(fileEdges) == 0 {
		return ""
	}

	var matched []string
	for _, c := range candidates {
		if c.fileID != "" && fileEdges[c.fileID] {
			matched = append(matched, c.nodeID)
		}
	}

	if len(matched) == 1 {
		return matched[0]
	}
	return ""
}

// BuildLabelIndex builds a normalized label → nodeID index for cross-project linking.
func BuildLabelIndex(nodes []store.Node) map[string]string {
	idx := make(map[string]string)
	for _, n := range nodes {
		if !isCodeSymbol(n.NodeType) {
			continue
		}
		normalized := n.Title
		normalized = strings.TrimSuffix(normalized, "()")
		normalized = strings.TrimPrefix(normalized, ".")
		if normalized != "" {
			idx[normalized] = n.ID
		}
	}
	return idx
}

// CrossProjectLink creates edges between a newly onboarded project and all
// other known projects. Uses three mechanisms:
//  1. Name-based: same-named functions/types → cross-project-call edges
//  2. Import-based: same-named packages → cross-project-reference edges
//  3. Embedding-based: high cosine similarity → auto-linked
//
// Returns all cross-project edges created.
func CrossProjectLink(
	ctx context.Context,
	db store.Store,
	newProjectID string,
	newNodes []store.Node,
) ([]store.Edge, error) {
	projectIDs, err := db.ListProjectIDs(ctx)
	if err != nil {
		return nil, err
	}

	// Collect label indices for all other projects
	type projectIndex struct {
		id   string
		idx  map[string]string // code symbol label → node ID
		pkgs map[string]string // module root → representative node ID
	}
	var others []projectIndex
	freq := map[string]int{} // normalized code name → count of projects containing it

	for _, otherID := range projectIDs {
		if otherID == newProjectID {
			continue
		}
		nodes, err := db.GetTopNodesByImportance(ctx, otherID, 100)
		if err != nil {
			continue
		}
		if len(nodes) == 0 {
			continue
		}
		idx := BuildLabelIndex(nodes)
		pkgs := buildModulePkgIndex(nodes)
		others = append(others, projectIndex{id: otherID, idx: idx, pkgs: pkgs})
		for name := range idx {
			freq[name]++
		}
	}

	var allEdges []store.Edge

	// Name-based matching with god-node guard
	for _, o := range others {
		for _, n := range newNodes {
			if !isCodeSymbol(n.NodeType) {
				continue
			}
			normalized := n.Title
			normalized = strings.TrimSuffix(normalized, "()")
			normalized = strings.TrimPrefix(normalized, ".")
			if normalized == "" {
				continue
			}
			if freq[normalized] > crossProjectGodGuard {
				continue
			}
			if targetID, ok := o.idx[normalized]; ok {
				allEdges = append(allEdges, store.Edge{
					FromID:         n.ID,
					ToID:           targetID,
					EdgeType:       "cross-project-call",
					Confidence:     0.7,
					ConfidenceTier: "INFERRED",
					Bidirectional:  true,
				})
			}
		}
	}

	// Import-based: link projects sharing the same module root
	newModPkgs := buildModulePkgIndex(newNodes)
	if len(newModPkgs) > 0 {
		for _, o := range others {
			for modRoot, newID := range newModPkgs {
				if targetID, ok := o.pkgs[modRoot]; ok {
					allEdges = append(allEdges, store.Edge{
						FromID:         newID,
						ToID:           targetID,
						EdgeType:       "depends_on",
						Confidence:     1.0,
						ConfidenceTier: "EXTRACTED",
						Bidirectional:  true,
					})
				}
			}
		}
	}

	// Embedding-based: cosine similarity on top signature nodes
	sigNodes := make([]store.Node, 0, 100)
	for _, n := range newNodes {
		if isCodeSymbol(n.NodeType) {
			sigNodes = append(sigNodes, n)
			if len(sigNodes) >= 100 {
				break
			}
		}
	}
	if len(sigNodes) > 0 {
		newIDs := make([]string, len(sigNodes))
		for i, n := range sigNodes {
			newIDs[i] = n.ID
		}
		newEmbeds, err := db.GetNodeEmbeddings(ctx, newIDs)
		if err == nil && len(newEmbeds) > 0 {
			for _, o := range others {
				otherIDs := make([]string, 0, len(o.idx))
				for _, id := range o.idx {
					otherIDs = append(otherIDs, id)
				}
				otherEmbeds, err := db.GetNodeEmbeddings(ctx, otherIDs)
				if err != nil || len(otherEmbeds) == 0 {
					continue
				}
				for newID, newVec := range newEmbeds {
					for otherID, otherVec := range otherEmbeds {
						cos := cosineSimilarity(newVec, otherVec)
						if cos > 0.80 {
							allEdges = append(allEdges, store.Edge{
								FromID:         newID,
								ToID:           otherID,
								EdgeType:       "auto-linked",
								Confidence:     cos,
								ConfidenceTier: "INFERRED",
								Bidirectional:  true,
							})
						}
					}
				}
				// Limit to top 100 other nodes to keep O(100x100)
				if len(otherEmbeds) > 100 {
					break
				}
			}
		}
	}

	return allEdges, nil
}
