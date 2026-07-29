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
					// Skip interface method declarations (no body, Recv().Underlying()
					// belongs to the interface, not a concrete type) — they're not
					// separately-callable symbols and would collide with the
					// concrete type's method of the same name.
					if isInterfaceMethod(o) {
						continue
					}
					n := c.makeFuncNode(pkg, ident.Name, o)
					nodes = append(nodes, n)
					edges = append(edges, store.Edge{
						FromID: pkgNodeID, ToID: n.ID,
						EdgeType: "defines", Confidence: 1.0,
					})
				}
			case *types.TypeName:
				if o.Pkg() == pkg.Types { // only types defined in this package
					n := c.makeTypeNode(pkg, ident.Name, o)
					nodes = append(nodes, n)
					edges = append(edges, store.Edge{
						FromID: pkgNodeID, ToID: n.ID,
						EdgeType: "defines", Confidence: 1.0,
					})
				}
			}
		}

		// Extract call edges — resolve each FuncDecl's *types.Func via
		// TypesInfo.Defs so the from-ID matches makeFuncNode's qualified
		// (receiver-aware) naming exactly.
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fnDecl, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				fnObj, ok := pkg.TypesInfo.Defs[fnDecl.Name]
				if !ok || fnObj == nil {
					continue
				}
				fn, ok := fnObj.(*types.Func)
				if !ok {
					continue
				}
				fnID := c.funcNodeID(pkg, funcQualifiedName(fn))
				c.extractCalls(pkg, fnDecl, fnID, &edges)
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

	return nodes, edges, nil
}

// funcQualifiedName returns a name unique within a package even when
// methods on different receiver types share a name (Close, String, Error,
// ...). Plain functions return just fn.Name(); methods return
// "{RecvType}.{MethodName}" with any pointer "*" stripped.
func funcQualifiedName(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return fn.Name()
	}
	recvType := sig.Recv().Type().String()
	if idx := strings.LastIndexByte(recvType, '.'); idx >= 0 {
		recvType = recvType[idx+1:] // strip package qualifier if any
	}
	recvType = strings.TrimPrefix(recvType, "*")
	return recvType + "." + fn.Name()
}

// isInterfaceMethod reports whether fn is an interface method declaration
// (no concrete receiver — the receiver type's underlying type is itself an
// interface). These aren't separately-callable symbols and would collide
// with a concrete type's method of the same name if ingested.
func isInterfaceMethod(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	_, isIface := sig.Recv().Type().Underlying().(*types.Interface)
	return isIface
}

func (c *CodeastIngestor) makeFuncNode(pkg *packages.Package, ident string, fn *types.Func) store.Node {
	qname := funcQualifiedName(fn)
	fnID := fmt.Sprintf("%s/code:function/%s.%s", c.cfg.ProjectID, pkg.PkgPath, qname)
	sig := fn.Type().String()
	doc := ""
	var bodySrc string

	// TODO: use hashmap instean of iterating over all files for each function
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fnDecl, ok := decl.(*ast.FuncDecl)
			if !ok || fnDecl.Name.Name != fn.Name() {
				continue
			}
			// Match receiver too — a bare name match alone conflates
			// distinct methods that share a name across types.
			if !declMatchesReceiver(fnDecl, fn) {
				continue
			}
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

	filePath := resolveGoFilePath(pkg, fn, c.cfg.Root)

	content := fmt.Sprintf("func %s %s", qname, sig)
	if bodySrc != "" {
		content += " " + bodySrc
	}
	if doc != "" {
		content += " // " + doc
	}
	return store.Node{
		ID:            fnID,
		ProjectID:     c.cfg.ProjectID,
		NodeType:      "code:function",
		Title:         qname,
		Content:       content,
		ContentHash:   "",
		Status:        "current",
		SourceAdapter: "codeast",
		SourcePath:    filePath,
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

// funcNodeID builds a function/method node ID from its already-qualified
// name (see funcQualifiedName — includes "{RecvType}." prefix for methods).
func (c *CodeastIngestor) funcNodeID(pkg *packages.Package, qualifiedName string) string {
	return fmt.Sprintf("%s/code:function/%s.%s", c.cfg.ProjectID, pkg.PkgPath, qualifiedName)
}

// declMatchesReceiver reports whether an *ast.FuncDecl and a resolved
// *types.Func refer to the same declaration, disambiguating same-named
// methods on different receiver types (both may have fnDecl.Name.Name ==
// fn.Name()). Plain functions (no receiver on either side) always match.
func declMatchesReceiver(fnDecl *ast.FuncDecl, fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return fnDecl.Recv == nil
	}
	if fnDecl.Recv == nil || len(fnDecl.Recv.List) == 0 {
		return false
	}
	declRecvName := recvTypeName(fnDecl.Recv.List[0].Type)
	funcRecvName := strings.TrimPrefix(sig.Recv().Type().String(), "*")
	if idx := strings.LastIndexByte(funcRecvName, '.'); idx >= 0 {
		funcRecvName = funcRecvName[idx+1:]
	}
	return declRecvName == funcRecvName
}

// recvTypeName extracts the bare type name from a receiver's AST type
// expression, stripping the leading "*" for pointer receivers.
func recvTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func (c *CodeastIngestor) extractCalls(pkg *packages.Package, fn *ast.FuncDecl, fromID string, edges *[]store.Edge) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Resolve callee using TypesInfo
		if callee := resolveCallee(pkg.TypesInfo, call); callee != nil {
			toID := c.funcNodeID(pkg, funcQualifiedName(callee))
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

// resolveGoFilePath extracts the relative file path for a Go function node.
func resolveGoFilePath(pkg *packages.Package, fn *types.Func, root string) string {
	// iterate syntax files to find this function's position
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if fnDecl, ok := decl.(*ast.FuncDecl); ok && fnDecl.Name.Name == fn.Name() && pkg.Fset != nil {
				if f := pkg.Fset.Position(fnDecl.Pos()).Filename; f != "" {
					if rel, err := filepath.Rel(root, f); err == nil {
						return rel
					}
					return f
				}
			}
		}
	}
	return pkg.PkgPath
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
			if path != c.cfg.Root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == "dist" || d.Name() == "bin") {
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
