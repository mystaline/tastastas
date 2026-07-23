// Package codeast provides code-aware ingestion for multiple languages.
// Go uses go/packages for exact symbol resolution; TS/Python/Rust use tree-sitter (best-effort).
package codeast

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/mystaline-dev/tastastas/internal/ingest/codeast/treesitter"
	"github.com/mystaline-dev/tastastas/internal/store"
	"golang.org/x/tools/go/packages"
)

type Config struct {
	Root         string
	ProjectID    string
	Languages    []string
	ExcludeGlobs []string
	Incremental  bool
}

type CodeastIngestor struct {
	cfg Config
	db  store.Store
}

func New(db store.Store, cfg Config) *CodeastIngestor {
	return &CodeastIngestor{db: db, cfg: cfg}
}

// Ingest runs code ingestion for the configured languages.
func (c *CodeastIngestor) Ingest(ctx context.Context) ([]store.Node, []store.Edge, error) {
	var allNodes []store.Node
	var allEdges []store.Edge

	for _, lang := range c.cfg.Languages {
		switch lang {
		case "go":
			nodes, edges, err := c.ingestGo()
			if err != nil {
				return nil, nil, err
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
		case "typescript", "ts", "javascript", "js":
			nodes, edges, err := c.ingestTreeSitter(lang)
			if err != nil {
				return nil, nil, err
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
		case "python", "py":
			nodes, edges, err := c.ingestTreeSitter(lang)
			if err != nil {
				return nil, nil, err
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
		case "rust", "rs":
			nodes, edges, err := c.ingestTreeSitter(lang)
			if err != nil {
				return nil, nil, err
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
		default:
			return nil, nil, fmt.Errorf("unsupported language: %s", lang)
		}
	}

	return allNodes, allEdges, nil
}

func (c *CodeastIngestor) ingestGo() ([]store.Node, []store.Edge, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		Dir: c.cfg.Root,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, nil, fmt.Errorf("go/packages load: %w", err)
	}

	var nodes []store.Node
	var edges []store.Edge
	pkgNodes := make(map[string]string) // pkg path -> node ID

	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			// Skip packages with errors but don't fail entirely
			continue
		}

		// Create package node
		pkgNodeID := fmt.Sprintf("%s/code:package/%s", c.cfg.ProjectID, pkg.PkgPath)
		pkgNodes[pkg.PkgPath] = pkgNodeID

		nodes = append(nodes, store.Node{
			ID:            pkgNodeID,
			ProjectID:     c.cfg.ProjectID,
			NodeType:      "code:package",
			Title:         pkg.Name,
			Content:       fmt.Sprintf("package %s", pkg.Name),
			SourceAdapter: "codeast",
			SourcePath:    pkg.PkgPath,
			Language:      "go",
			Importance:    0.5,
		})

		// Extract functions and types
		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil {
				continue
			}

			switch o := obj.(type) {
			case *types.Func:
				if o.Pkg() == pkg.Types { // only functions defined in this package
					nodes = append(nodes, c.makeFuncNode(pkg, ident.Name, o))
				}
			case *types.TypeName:
				if o.Pkg() == pkg.Types { // only types defined in this package
					nodes = append(nodes, c.makeTypeNode(pkg, ident.Name, o))
				}
			}
		}

		// Extract call edges
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					fnID := c.funcNodeID(pkg, fn.Name.Name)
					c.extractCalls(pkg, fn, fnID, &edges)
				}
			}
		}

		// Import edges
		for _, imp := range pkg.Imports {
			if toPkgID, ok := pkgNodes[imp.PkgPath]; ok {
				edges = append(edges, store.Edge{
					FromID:     pkgNodeID,
					ToID:       toPkgID,
					EdgeType:   "imports",
					Confidence: 1.0,
				})
			}
		}
	}

	// Define edges from package to symbols
	for _, n := range nodes {
		// Format: "{projectID}/code:function/{pkgpath}.{name}" or "{projectID}/code:type/{pkgpath}.{name}"
		// We need to find the matching package node.
		var symType, rest string
		switch {
		case strings.HasPrefix(n.ID, c.cfg.ProjectID+"/code:function/"):
			symType = "code:function/"
			rest = strings.TrimPrefix(n.ID, c.cfg.ProjectID+"/code:function/")
		case strings.HasPrefix(n.ID, c.cfg.ProjectID+"/code:type/"):
			symType = "code:type/"
			rest = strings.TrimPrefix(n.ID, c.cfg.ProjectID+"/code:type/")
		default:
			continue
		}
		_ = symType

		// rest is "{pkgpath}.{name}" — split off last segment as name
		idx := strings.LastIndex(rest, ".")
		if idx < 0 {
			continue
		}
		pkgPath := rest[:idx]
		if pkgNodeID, ok := pkgNodes[pkgPath]; ok {
			edges = append(edges, store.Edge{
				FromID:     pkgNodeID,
				ToID:       n.ID,
				EdgeType:   "defines",
				Confidence: 1.0,
			})
		}
	}

	return nodes, edges, nil
}

func (c *CodeastIngestor) makeFuncNode(pkg *packages.Package, ident string, fn *types.Func) store.Node {
	fnID := fmt.Sprintf("%s/code:function/%s.%s", c.cfg.ProjectID, pkg.PkgPath, fn.Name())
	sig := fn.Type().String()
	doc := ""
	var bodySrc string

	// TODO: use hashmap instean of iterating over all files for each function
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if fnDecl, ok := decl.(*ast.FuncDecl); ok && fnDecl.Name.Name == fn.Name() {
				if fnDecl.Doc != nil {
					doc = fnDecl.Doc.Text()
				}
				if fnDecl.Body != nil && pkg.Fset != nil {
					start := pkg.Fset.Position(fnDecl.Body.Pos()).Offset
					end := pkg.Fset.Position(fnDecl.Body.End()).Offset
					if start >= 0 && end > start {
						// Read source bytes from the actual file
						if src, err := os.ReadFile(pkg.Fset.Position(fnDecl.Pos()).Filename); err == nil {
							if end <= len(src) {
								bodySrc = string(src[start:end])
							}
						}
					}
				}
				break
			}
		}
	}

	content := fmt.Sprintf("func %s %s", fn.Name(), sig)
	if bodySrc != "" {
		content += " " + truncateBody(bodySrc)
	}
	if doc != "" {
		content += " // " + doc
	}
	return store.Node{
		ID:            fnID,
		ProjectID:     c.cfg.ProjectID,
		NodeType:      "code:function",
		Title:         fn.Name(),
		Content:       content,
		ContentHash:   "",
		Status:        "current",
		SourceAdapter: "codeast",
		SourcePath:    pkg.PkgPath,
		Language:      "go",
		Importance:    0.7,
	}
}

func (c *CodeastIngestor) makeTypeNode(pkg *packages.Package, ident string, tn *types.TypeName) store.Node {
	typeID := fmt.Sprintf("%s/code:type/%s.%s", c.cfg.ProjectID, pkg.PkgPath, tn.Name())
	return store.Node{
		ID:            typeID,
		ProjectID:     c.cfg.ProjectID,
		NodeType:      "code:type",
		Title:         tn.Name(),
		Content:       fmt.Sprintf("type %s", tn.Type().String()),
		ContentHash:   "",
		Status:        "current",
		SourceAdapter: "codeast",
		SourcePath:    pkg.PkgPath,
		Language:      "go",
		Importance:    0.6,
	}
}

func (c *CodeastIngestor) funcNodeID(pkg *packages.Package, fnName string) string {
	return fmt.Sprintf("%s/code:function/%s.%s", c.cfg.ProjectID, pkg.PkgPath, fnName)
}

func (c *CodeastIngestor) extractCalls(pkg *packages.Package, fn *ast.FuncDecl, fromID string, edges *[]store.Edge) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Resolve callee using TypesInfo
		if callee := resolveCallee(pkg.TypesInfo, call); callee != nil {
			toID := c.funcNodeID(pkg, callee.Name())
			*edges = append(*edges, store.Edge{
				FromID:     fromID,
				ToID:       toID,
				EdgeType:   "calls",
				Confidence: 1.0,
			})
		}
		return true
	})
}

const maxBodyLen = 4096

func truncateBody(s string) string {
	if len(s) <= maxBodyLen {
		return s
	}
	return s[:maxBodyLen] + "..."
}

func resolveCallee(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if obj := info.Uses[fun]; obj != nil {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	case *ast.SelectorExpr:
		if obj := info.Uses[fun.Sel]; obj != nil {
			if fn, ok := obj.(*types.Func); ok {
				return fn
			}
		}
	}
	return nil
}

func (c *CodeastIngestor) ingestTreeSitter(lang string) ([]store.Node, []store.Edge, error) {
	ext := treesitter.NewForLang(lang)
	if ext == nil {
		return nil, nil, fmt.Errorf("unsupported tree-sitter language: %s", lang)
	}

	// Walk files matching the language extension
	walkExts := treesitter.FileExts(lang)
	extSet := make(map[string]bool, len(walkExts))
	for _, e := range walkExts {
		extSet[e] = true
	}

	var allNodes []store.Node
	var allEdges []store.Edge

	err := filepath.WalkDir(c.cfg.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != c.cfg.Root {
				return filepath.SkipDir
			}
			return nil
		}
		if !extSet[filepath.Ext(path)] {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(c.cfg.Root, path)
		nodes, edges, err := treesitter.Extract(c.cfg.ProjectID, rel, source, lang, ext)
		if err != nil {
			return err
		}
		allNodes = append(allNodes, nodes...)
		allEdges = append(allEdges, edges...)
		return nil
	})
	return allNodes, allEdges, err
}
