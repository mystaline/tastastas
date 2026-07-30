package treesitter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mystaline-dev/tastastas/internal/store"
)

// ExtractPackageManifests walks root looking for package.json files and
// creates package nodes with depends_on edges for workspace packages.
func ExtractPackageManifests(root, projectID string) ([]store.Node, []store.Edge) {
	var nodes []store.Node
	var edges []store.Edge

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() != "package.json" {
			return nil
		}
		n, e := parsePackageJSON(path, root, projectID)
		nodes = append(nodes, n...)
		edges = append(edges, e...)
		return nil
	})

	return nodes, edges
}

type pkgJSON struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	Workspaces           []string          `json:"workspaces"`
}

func parsePackageJSON(pkgPath, root, projectID string) ([]store.Node, []store.Edge) {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil, nil
	}
	var pkg pkgJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, nil
	}
	if pkg.Name == "" {
		return nil, nil
	}

	pkgNodeID := fmt.Sprintf("%s/code:package/%s", projectID, pkg.Name)
	rel, _ := filepath.Rel(root, pkgPath)
	dir := filepath.Dir(rel)

	nodes := []store.Node{{
		ID:            pkgNodeID,
		ProjectID:     projectID,
		NodeType:      "code:package",
		Title:         pkg.Name,
		Content:       fmt.Sprintf("package %s", pkg.Name),
		SourceAdapter: "codeast",
		SourcePath:    filepath.ToSlash(dir),
		Language:      "typescript",
		Importance:    0.5,
	}}

	// File node for this directory (if it has an index file)
	indexID := fmt.Sprintf("%s/code:file/%s", projectID, filepath.ToSlash(dir))
	nodes = append(nodes, store.Node{
		ID:            indexID,
		ProjectID:     projectID,
		NodeType:      "code:file",
		Title:         filepath.ToSlash(dir),
		Content:       fmt.Sprintf("package %s directory", pkg.Name),
		SourceAdapter: "codeast",
		SourcePath:    filepath.ToSlash(dir),
		Language:      "typescript",
		Importance:    0.3,
	})

	var edges []store.Edge
	allDeps := make(map[string]bool)
	for dep := range pkg.Dependencies {
		allDeps[dep] = true
	}
	for dep := range pkg.DevDependencies {
		allDeps[dep] = true
	}
	for dep := range pkg.PeerDependencies {
		allDeps[dep] = true
	}

	for dep := range allDeps {
		depID := fmt.Sprintf("%s/code:package/%s", projectID, dep)
		edges = append(edges, store.Edge{
			FromID:         pkgNodeID,
			ToID:           depID,
			EdgeType:       "depends_on",
			Confidence:     1.0,
			ConfidenceTier: "EXTRACTED",
		})
	}

	return nodes, edges
}

func isWorkspaceDir(root, pkgDir string) bool {
	abs := filepath.Join(root, pkgDir)
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// CollectPackageNames returns a set of all package names from package.json
// files in root, used for import resolution.
func CollectPackageNames(root string) map[string]string {
	names := make(map[string]string)
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() != "package.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var pkg struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil || pkg.Name == "" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		names[pkg.Name] = filepath.Dir(rel)
		return nil
	})
	return names
}
