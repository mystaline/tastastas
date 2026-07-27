package onboard

import (
	"sort"
	"strings"

	"github.com/mystaline-dev/tastastas/internal/store"
)

// dirNode tracks a candidate directory during tree-building: its prefix
// (empty string = repo root), the child directory prefixes and leaf node
// IDs it directly owns, and whether it owns any leaf directly (used for
// pass-through collapse).
type dirNode struct {
	prefix       string
	childDirs    map[string]bool
	childLeaves  []string
	parentPrefix string
	hasParent    bool
}

// BuildHierarchy generates a synthetic directory backbone from the
// SourcePath of every node in nodes: one "directory" node per meaningful
// folder level, connected top-down via "contains" edges, rooted at a single
// repo-root node. Purely additive — never mutates or reads existing
// nodes/edges, only derives new ones from SourcePath strings already
// present on the input slice.
//
// code:package nodes are skipped: codeast sets their SourcePath to a Go
// import path (pkg.PkgPath), not a filesystem path, which would otherwise
// produce a phantom branch disconnected from the real file-path tree built
// from code:function/code:type nodes.
//
// Single-child pass-through folders (a directory with exactly one child and
// zero directly-owned leaves) are collapsed: their child is reattached to
// their parent, and the pass-through node itself is dropped. The repo root
// is never collapsed, even with zero children.
func BuildHierarchy(projectID string, nodes []store.Node) ([]store.Node, []store.Edge) {
	dirs := map[string]*dirNode{
		"": {prefix: "", childDirs: map[string]bool{}},
	}

	getOrCreateDir := func(prefix string) *dirNode {
		if d, ok := dirs[prefix]; ok {
			return d
		}
		d := &dirNode{prefix: prefix, childDirs: map[string]bool{}}
		dirs[prefix] = d
		return d
	}

	// ensureChain walks from root down to prefix, creating any missing
	// intermediate directory nodes and wiring parent->child relationships.
	ensureChain := func(segments []string) string {
		parent := ""
		for _, seg := range segments {
			var child string
			if parent == "" {
				child = seg
			} else {
				child = parent + "/" + seg
			}
			pd := getOrCreateDir(parent)
			cd := getOrCreateDir(child)
			if !cd.hasParent {
				cd.parentPrefix = parent
				cd.hasParent = true
			}
			pd.childDirs[child] = true
			parent = child
		}
		return parent
	}

	for _, n := range nodes {
		if n.NodeType == "code:package" {
			continue
		}
		if n.SourcePath == "" {
			continue
		}
		rel := strings.TrimPrefix(strings.ReplaceAll(n.SourcePath, "\\", "/"), "/")
		segs := strings.Split(rel, "/")
		if len(segs) == 0 {
			continue
		}
		// Drop the filename (last segment) to get ancestor folder segments.
		folderSegs := segs[:len(segs)-1]
		leafParentPrefix := ensureChain(folderSegs)
		pd := getOrCreateDir(leafParentPrefix)
		pd.childLeaves = append(pd.childLeaves, n.ID)
	}

	// Collapse pass-through folders: a non-root directory with exactly one
	// total child (one child dir, zero leaves, OR zero child dirs and one
	// leaf is NOT collapsed — leaves are real content, only single-child-dir
	// chains with no direct leaves collapse) and zero directly-owned leaves.
	collapsed := map[string]bool{}
	changed := true
	for changed {
		changed = false
		for prefix, d := range dirs {
			if prefix == "" || collapsed[prefix] {
				continue
			}
			if len(d.childLeaves) != 0 {
				continue
			}
			if len(d.childDirs) != 1 {
				continue
			}
			var onlyChild string
			for c := range d.childDirs {
				onlyChild = c
			}
			if !d.hasParent {
				continue
			}
			// Reattach onlyChild to d's parent, drop d.
			parent := dirs[d.parentPrefix]
			if parent == nil {
				continue
			}
			delete(parent.childDirs, prefix)
			parent.childDirs[onlyChild] = true
			cd := dirs[onlyChild]
			if cd != nil {
				cd.parentPrefix = d.parentPrefix
				cd.hasParent = true
			}
			collapsed[prefix] = true
			changed = true
		}
	}

	var hierNodes []store.Node
	var hierEdges []store.Edge

	dirNodeID := func(prefix string) string {
		return projectID + "/dir/" + prefix
	}

	var prefixes []string
	for prefix := range dirs {
		if collapsed[prefix] {
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	for _, prefix := range prefixes {
		d := dirs[prefix]
		title := projectID
		if prefix != "" {
			segs := strings.Split(prefix, "/")
			title = segs[len(segs)-1]
		}
		hierNodes = append(hierNodes, store.Node{
			ID:            dirNodeID(prefix),
			ProjectID:     projectID,
			NodeType:      "directory",
			Title:         title,
			Content:       "",
			Status:        "current",
			SourceAdapter: "hierarchy",
			SourcePath:    prefix,
			Importance:    0.3,
		})

		// The collapse fixed-point loop above rewires every parent's
		// childDirs to skip collapsed prefixes as soon as they collapse, so
		// by convergence no remaining dirNode's childDirs references a
		// collapsed prefix — safe to emit directly.
		for child := range d.childDirs {
			hierEdges = append(hierEdges, store.Edge{
				FromID:     dirNodeID(prefix),
				ToID:       dirNodeID(child),
				EdgeType:   "contains",
				Confidence: 1.0,
			})
		}
		for _, leafID := range d.childLeaves {
			hierEdges = append(hierEdges, store.Edge{
				FromID:     dirNodeID(prefix),
				ToID:       leafID,
				EdgeType:   "contains",
				Confidence: 1.0,
			})
		}
	}

	return hierNodes, hierEdges
}
