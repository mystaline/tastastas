// Package codeast provides code-aware ingestion for multiple languages.
// Go uses go/packages for exact symbol resolution; TS/Python/Rust use tree-sitter (best-effort).
package codeast

import (
	"context"
	"fmt"
	"go/ast"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mystaline/tastastas/internal/ingest/codeast/treesitter"
	"github.com/mystaline/tastastas/internal/store"
	"golang.org/x/tools/go/packages"
)

type Config struct {
	Root             string
	ProjectID        string
	Languages        []string
	ExcludeGlobs     []string
	Incremental      bool
	WorkspaceModules []string // P0: module paths from detectModuleRoots, used for cross-pkg call stubs in P2a
}

func extractEdgeTier(edgeType string) string {
	switch edgeType {
	case "defines", "calls", "imports":
		return "EXTRACTED"
	default:
		return "INFERRED"
	}
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
	nodes, edges, _, err := c.IngestWithCalls(ctx)
	return nodes, edges, err
}

// IngestWithCalls is like Ingest but also returns raw_calls for cross-file linking.
// A single language's ingestion failure (bad query, parse error, etc.) is
// logged and skipped rather than aborting every other language's results —
// one broken tree-sitter query for Rust must never take down Go/TS ingestion.
func (c *CodeastIngestor) IngestWithCalls(
	ctx context.Context,
) ([]store.Node, []store.Edge, []treesitter.RawCall, error) {
	var allNodes []store.Node
	var allEdges []store.Edge
	var allRawCalls []treesitter.RawCall
	hasTS := false
	var langErrs []string

	for _, lang := range c.cfg.Languages {
		switch lang {
		case "go":
			nodes, edges, err := c.ingestGo()
			if err != nil {
				langErrs = append(langErrs, fmt.Sprintf("go: %v", err))
				continue
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
		case "typescript", "ts", "javascript", "js":
			nodes, edges, rc, err := c.ingestTreeSitterWithCalls(lang)
			if err != nil {
				langErrs = append(langErrs, fmt.Sprintf("%s: %v", lang, err))
				continue
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
			allRawCalls = append(allRawCalls, rc...)
			hasTS = true
		case "python", "py":
			nodes, edges, rc, err := c.ingestTreeSitterWithCalls(lang)
			if err != nil {
				langErrs = append(langErrs, fmt.Sprintf("%s: %v", lang, err))
				continue
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
			allRawCalls = append(allRawCalls, rc...)
		case "rust", "rs":
			nodes, edges, rc, err := c.ingestTreeSitterWithCalls(lang)
			if err != nil {
				langErrs = append(langErrs, fmt.Sprintf("%s: %v", lang, err))
				continue
			}

			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
			allRawCalls = append(allRawCalls, rc...)
		default:
			langErrs = append(langErrs, fmt.Sprintf("unsupported language: %s", lang))
		}
	}

	if len(langErrs) > 0 {
		log.Printf("codeast: %d language(s) skipped due to errors: %s", len(langErrs), strings.Join(langErrs, "; "))
	}
	// Fail only if every language failed and nothing was ingested — a total
	// loss, not a partial one.
	if len(allNodes) == 0 && len(langErrs) > 0 && len(langErrs) == len(c.cfg.Languages) {
		return nil, nil, nil, fmt.Errorf("codeast: all languages failed: %s", strings.Join(langErrs, "; "))
	}

	// Free AST memory before post-processing — packages.Load (even without
	// NeedDeps) can hold hundreds of MB of type-checked ASTs that won't be
	// touched again until the next onboard call.
	runtime.GC()

	// Package manifest extraction for TS/JS projects (P2f)
	if hasTS {
		pkgNodes, pkgEdges := treesitter.ExtractPackageManifests(c.cfg.Root, c.cfg.ProjectID)
		allNodes = append(allNodes, pkgNodes...)
		allEdges = append(allEdges, pkgEdges...)
	}

	return allNodes, allEdges, allRawCalls, nil
}

func (c *CodeastIngestor) ingestGo() ([]store.Node, []store.Edge, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports,
		Dir: c.cfg.Root,
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, nil, fmt.Errorf("go/packages load: %w", err)
	}

	var nodes []store.Node
	var edges []store.Edge
	pkgNodes := make(map[string]string)

	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			continue
		}

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

		fileCache := make(map[string][]byte)

		for ident, obj := range pkg.TypesInfo.Defs {
			if obj == nil {
				continue
			}

			switch o := obj.(type) {
			case *types.Func:
				if o.Pkg() == pkg.Types {
					if isInterfaceMethod(o) {
						continue
					}
					n := c.makeFuncNode(pkg, ident.Name, o, fileCache)
					nodes = append(nodes, n)
					edges = append(edges, store.Edge{
						FromID: pkgNodeID, ToID: n.ID,
						EdgeType: "defines", Confidence: 1.0, ConfidenceTier: "EXTRACTED",
					})
				}
			case *types.TypeName:
				if o.Pkg() == pkg.Types {
					n := c.makeTypeNode(pkg, ident.Name, o)
					nodes = append(nodes, n)
					edges = append(edges, store.Edge{
						FromID: pkgNodeID, ToID: n.ID,
						EdgeType: "defines", Confidence: 1.0, ConfidenceTier: "EXTRACTED",
					})
				}
			}
		}

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

		for _, imp := range pkg.Imports {
			if toPkgID, ok := pkgNodes[imp.PkgPath]; ok {
				edges = append(edges, store.Edge{
					FromID:         pkgNodeID,
					ToID:           toPkgID,
					EdgeType:       "imports",
					Confidence:     1.0,
					ConfidenceTier: "EXTRACTED",
				})
			}
		}
	}

	// Type reference edges: parameter/return/field types (Go)
	typeNodeIndex := buildGoTypeNodeIndex(c.cfg.ProjectID, pkgs, pkgNodes)
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			continue
		}
		refs := c.extractGoTypeRefs(pkg, typeNodeIndex)
		edges = append(edges, refs...)
	}

	return nodes, edges, nil
}

// buildGoTypeNodeIndex builds a map of "pkgPath.TypeName" → node ID for all
// workspace type nodes, used for type reference edge resolution.
func buildGoTypeNodeIndex(projectID string, pkgs []*packages.Package, pkgNodes map[string]string) map[string]string {
	idx := make(map[string]string)
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			continue
		}
		for _, obj := range pkg.TypesInfo.Defs {
			if tn, ok := obj.(*types.TypeName); ok && tn.Pkg() != nil {
				if _, ok := pkgNodes[tn.Pkg().Path()]; ok {
					key := tn.Pkg().Path() + "." + tn.Name()
					id := fmt.Sprintf("%s/code:type/%s.%s", projectID, tn.Pkg().Path(), tn.Name())
					idx[key] = id
				}
			}
		}
	}
	return idx
}

// predeclaredTypes are Go built-in types that should never produce reference edges.
var predeclaredTypes = map[string]bool{
	"bool": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "float32": true, "float64": true, "complex64": true, "complex128": true,
	"string": true, "byte": true, "rune": true, "error": true,
	"any": true, "comparable": true, "true": true, "false": true, "iota": true, "nil": true,
}

func (c *CodeastIngestor) extractGoTypeRefs(pkg *packages.Package, typeIndex map[string]string) []store.Edge {
	var refs []store.Edge

	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if node.Type == nil {
					return true
				}
				// Find the func node ID
				fnObj, ok := pkg.TypesInfo.Defs[node.Name]
				if !ok || fnObj == nil {
					return true
				}
				fn, ok := fnObj.(*types.Func)
				if !ok {
					return true
				}
				fromID := c.funcNodeID(pkg, funcQualifiedName(fn))

				// Walk params and returns
				if node.Type.Params != nil {
					for _, field := range node.Type.Params.List {
						refs = append(refs, c.extractTypeRefFromExpr(field.Type, pkg, typeIndex, fromID)...)
					}
				}
				if node.Type.Results != nil {
					for _, field := range node.Type.Results.List {
						refs = append(refs, c.extractTypeRefFromExpr(field.Type, pkg, typeIndex, fromID)...)
					}
				}
			case *ast.GenDecl:
				if node.Tok.String() != "type" {
					return true
				}
				for _, spec := range node.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					typeObj, ok := pkg.TypesInfo.Defs[ts.Name]
					if !ok || typeObj == nil {
						continue
					}
					tn, ok := typeObj.(*types.TypeName)
					if !ok {
						continue
					}
					fromID := fmt.Sprintf("%s/code:type/%s.%s", c.cfg.ProjectID, pkg.PkgPath, tn.Name())

					// Walk struct fields
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, field := range st.Fields.List {
							refs = append(refs, c.extractTypeRefFromExpr(field.Type, pkg, typeIndex, fromID)...)
						}
					}
					// Walk interface methods
					if it, ok := ts.Type.(*ast.InterfaceType); ok {
						for _, method := range it.Methods.List {
							refs = append(refs, c.extractTypeRefFromExpr(method.Type, pkg, typeIndex, fromID)...)
						}
					}
				}
			}
			return true
		})
	}

	return refs
}

func (c *CodeastIngestor) extractTypeRefFromExpr(
	expr ast.Expr,
	pkg *packages.Package,
	typeIndex map[string]string,
	fromID string,
) []store.Edge {
	if pkg.TypesInfo == nil {
		return nil
	}
	tv, ok := pkg.TypesInfo.Types[expr]
	if !ok || tv.Type == nil {
		return nil
	}
	return c.resolveTypeRef(tv.Type, typeIndex, fromID)
}

func (c *CodeastIngestor) resolveTypeRef(t types.Type, typeIndex map[string]string, fromID string) []store.Edge {
	switch t := t.(type) {
	case *types.Named:
		if t.Obj().Pkg() == nil {
			return nil // predeclared type
		}
		if predeclaredTypes[t.Obj().Name()] {
			return nil
		}
		key := t.Obj().Pkg().Path() + "." + t.Obj().Name()
		if toID, ok := typeIndex[key]; ok {
			return []store.Edge{{
				FromID:         fromID,
				ToID:           toID,
				EdgeType:       "references",
				Confidence:     1.0,
				ConfidenceTier: "EXTRACTED",
			}}
		}
		return nil
	case *types.Pointer:
		return c.resolveTypeRef(t.Elem(), typeIndex, fromID)
	case *types.Slice:
		return c.resolveTypeRef(t.Elem(), typeIndex, fromID)
	case *types.Array:
		return c.resolveTypeRef(t.Elem(), typeIndex, fromID)
	case *types.Map:
		var refs []store.Edge
		refs = append(refs, c.resolveTypeRef(t.Key(), typeIndex, fromID)...)
		refs = append(refs, c.resolveTypeRef(t.Elem(), typeIndex, fromID)...)
		return refs
	case *types.Chan:
		return c.resolveTypeRef(t.Elem(), typeIndex, fromID)
	case *types.Signature:
		var refs []store.Edge
		for i := 0; i < t.Params().Len(); i++ {
			refs = append(refs, c.resolveTypeRef(t.Params().At(i).Type(), typeIndex, fromID)...)
		}
		for i := 0; i < t.Results().Len(); i++ {
			refs = append(refs, c.resolveTypeRef(t.Results().At(i).Type(), typeIndex, fromID)...)
		}
		return refs
	default:
		return nil
	}
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

func (c *CodeastIngestor) makeFuncNode(pkg *packages.Package, ident string, fn *types.Func, fileCache map[string][]byte) store.Node {
	qname := funcQualifiedName(fn)
	fnID := fmt.Sprintf("%s/code:function/%s.%s", c.cfg.ProjectID, pkg.PkgPath, qname)
	sig := fn.Type().String()
	doc := ""
	var bodySrc string

	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fnDecl, ok := decl.(*ast.FuncDecl)
			if !ok || fnDecl.Name.Name != fn.Name() {
				continue
			}
			if !declMatchesReceiver(fnDecl, fn) {
				continue
			}
			if fnDecl.Doc != nil {
				doc = fnDecl.Doc.Text()
			}
			if fnDecl.Body != nil && pkg.Fset != nil {
				start := pkg.Fset.Position(fnDecl.Body.Pos()).Offset
				end := pkg.Fset.Position(fnDecl.Body.End()).Offset
				fname := pkg.Fset.Position(fnDecl.Pos()).Filename
				if start >= 0 && end > start && fname != "" {
					src, ok := fileCache[fname]
					if !ok {
						data, err := os.ReadFile(fname)
						if err == nil {
							fileCache[fname] = data
							src = data
						}
					}
					if len(src) > 0 && end <= len(src) {
						bodySrc = string(src[start:end])
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

// calleeNodeID resolves the node ID for a callee function.
// If the callee is in a different package, it uses the callee's own package path
// (not the caller's). Stdlib and external deps are skipped (returns "").
func (c *CodeastIngestor) calleeNodeID(pkg *packages.Package, callee *types.Func) string {
	calleePkg := callee.Pkg()
	if calleePkg != nil && calleePkg.Path() != pkg.PkgPath {
		if !c.isWorkspacePackage(calleePkg.Path()) {
			return "" // skip stdlib (fmt, context, sync) and external deps
		}
		return fmt.Sprintf("%s/code:function/%s.%s",
			c.cfg.ProjectID, calleePkg.Path(), funcQualifiedName(callee))
	}
	return c.funcNodeID(pkg, funcQualifiedName(callee))
}

// isWorkspacePackage checks if pkgPath belongs to any detected workspace module.
func (c *CodeastIngestor) isWorkspacePackage(pkgPath string) bool {
	for _, mod := range c.cfg.WorkspaceModules {
		if pkgPath == mod || strings.HasPrefix(pkgPath, mod+"/") {
			return true
		}
	}
	return false
}

func (c *CodeastIngestor) extractCalls(pkg *packages.Package, fn *ast.FuncDecl, fromID string, edges *[]store.Edge) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if callee := resolveCallee(pkg.TypesInfo, call); callee != nil {
			toID := c.calleeNodeID(pkg, callee)
			if toID == "" {
				return true // skip stdlib/external, no stub edge
			}
			*edges = append(*edges, store.Edge{
				FromID:         fromID,
				ToID:           toID,
				EdgeType:       "calls",
				Confidence:     1.0,
				ConfidenceTier: "EXTRACTED",
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
	n, e, _, err := c.ingestTreeSitterWithCalls(lang)
	return n, e, err
}

func (c *CodeastIngestor) ingestTreeSitterWithCalls(
	lang string,
) ([]store.Node, []store.Edge, []treesitter.RawCall, error) {
	ext := treesitter.NewForLang(lang)
	if ext == nil {
		return nil, nil, nil, fmt.Errorf("unsupported tree-sitter language: %s", lang)
	}

	walkExts := treesitter.FileExts(lang)
	extSet := make(map[string]bool, len(walkExts))
	for _, e := range walkExts {
		extSet[e] = true
	}

	var allNodes []store.Node
	var allEdges []store.Edge
	var allRawCalls []treesitter.RawCall

	err := filepath.WalkDir(c.cfg.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != c.cfg.Root &&
				(strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == "dist" || d.Name() == "bin") {
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
		nodes, edges, rc, err := treesitter.Extract(c.cfg.ProjectID, rel, source, lang, ext, c.cfg.Root)
		if err != nil {
			return err
		}
		allNodes = append(allNodes, nodes...)
		allEdges = append(allEdges, edges...)
		allRawCalls = append(allRawCalls, rc...)
		return nil
	})
	return allNodes, allEdges, allRawCalls, err
}
