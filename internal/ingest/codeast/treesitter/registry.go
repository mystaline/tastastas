// Package treesitter provides best-effort code extraction via tree-sitter.
// Used for TypeScript, Python, Rust where no embeddable Go compiler frontend exists.
// Calls are name-matched within file scope → Confidence 0.7 (vs Go's 1.0).
package treesitter

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mystaline/tastastas/internal/store"
	sitter "github.com/tree-sitter/go-tree-sitter"
)

const maxDeclLen = 1 << 30 // effectively unlimited — full body stored in DB, truncated in-memory in caller

type importRule struct {
	query   string
	srcFrom func(m *sitter.QueryMatch, src []byte) string
}

type Extractor interface {
	Language() *sitter.Language
	Queries() map[string]string
	SymbolKind(grammarName string) string
	ImportRule() *importRule
}

// CallExtractor is an optional interface for extractors that can resolve
// call expressions (calls, member calls, new expressions).
type CallExtractor interface {
	CallQueries() map[string]string // call expression capture queries
}

// TypeRefExtractor is an optional interface for extractors that can emit
// type reference edges (parameter types, return types, field types, etc.).
type TypeRefExtractor interface {
	TypeRefQueries() map[string]string // type annotation capture queries
}

// RawCall represents a call that could not be resolved within the same file.
// Collected for cross-file resolution by the global linker.
type RawCall struct {
	CalleeName   string // normalized callee identifier
	CallerNodeID string // node ID of the calling function/method
	SourceFile   string // relative path
	SourceLoc    string // line number
	Lang         string // "typescript" | "javascript" | etc
	IsMemberCall bool   // obj.method() vs plain func()
	IsNewCall    bool   // new Foo()
	Receiver     string // for member calls: the object name (empty for plain calls)
}

func Extract(projectID, sourcePath string, source []byte, langName string, ext Extractor, projectRoot string) ([]store.Node, []store.Edge, []RawCall, error) {
	parser := sitter.NewParser()
	if err := parser.SetLanguage(ext.Language()); err != nil {
		return nil, nil, nil, fmt.Errorf("ts %s: set lang: %w", langName, err)
	}
	defer parser.Close()

	tree := parser.Parse(source, nil)
	defer tree.Close()
	root := tree.RootNode()

	moduleID := fmt.Sprintf("%s/code:file/%s", projectID, sourcePath)
	var nodes []store.Node
	var edges []store.Edge
	symMap := map[string]string{} // symbol name → node ID

	// ── File node ──
	nodes = append(nodes, store.Node{
		ID:            moduleID,
		ProjectID:     projectID,
		NodeType:      "code:file",
		Title:         sourcePath,
		Content:       fmt.Sprintf("file %s (%s)", sourcePath, langName),
		SourceAdapter: "codeast",
		SourcePath:    sourcePath,
		Language:      langName,
		Importance:    0.4,
	})

	// ── Symbol queries ──
	for label, qs := range ext.Queries() {
		_ = label
		q, qErr := sitter.NewQuery(ext.Language(), qs)
		if qErr != nil {
			return nil, nil, nil, fmt.Errorf("ts %s: query %s: %w", langName, qs, qErr)
		}

		capNames := q.CaptureNames()
		cursor := sitter.NewQueryCursor()
		matches := cursor.Matches(q, root, source)

		for {
			m := matches.Next()
			if m == nil {
				break
			}

			var symName string
			var declNode *sitter.Node
			var grammarName string

			for _, cap := range m.Captures {
				idx := int(cap.Index)
				if idx >= len(capNames) {
					continue
				}
				switch capNames[idx] {
				case "name":
					symName = cap.Node.Utf8Text(source)
				case "node":
					declNode = &cap.Node
					grammarName = cap.Node.GrammarName()
				}
			}
			if symName == "" || declNode == nil {
				continue
			}

			kind := ext.SymbolKind(grammarName)
			qualifier := strings.ReplaceAll(sourcePath, "/", ".")
			symID := fmt.Sprintf("%s/%s/%s.%s", projectID, kind, qualifier, symName)
			symMap[symName] = symID

			content := declNode.Utf8Text(source)

			nodes = append(nodes, store.Node{
				ID:            symID,
				ProjectID:     projectID,
				NodeType:      kind,
				Title:         symName,
				Content:       content,
				SourceAdapter: "codeast",
				SourcePath:    sourcePath,
				Language:      langName,
				Importance:    0.7,
			})

			edges = append(edges, store.Edge{
				FromID:         moduleID,
				ToID:           symID,
				EdgeType:       "defines",
				Confidence:     1.0,
				ConfidenceTier: "EXTRACTED",
			})
		}
		cursor.Close()
		q.Close()
	}

	// ── Import edges ──
	if r := ext.ImportRule(); r != nil {
		q, qErr := sitter.NewQuery(ext.Language(), r.query)
		if qErr == nil {
			cursor := sitter.NewQueryCursor()
			treeMatches := cursor.Matches(q, root, source)
			sourceDir := filepath.Dir(sourcePath)
			for {
				m := treeMatches.Next()
				if m == nil {
					break
				}
				if spec := r.srcFrom(m, source); spec != "" {
					targetID := resolveImport(spec, sourceDir, projectRoot, projectID)
					if targetID != "" {
						edges = append(edges, store.Edge{
							FromID:         moduleID,
							ToID:           targetID,
							EdgeType:       "imports",
							Confidence:     1.0,
							ConfidenceTier: "EXTRACTED",
						})
					}
				}
			}
			cursor.Close()
			q.Close()
		}
	}

	// ── Type reference edges ──
	if typeRefExt, ok := ext.(TypeRefExtractor); ok {
		for label, qs := range typeRefExt.TypeRefQueries() {
			_ = label
			q, qErr := sitter.NewQuery(ext.Language(), qs)
			if qErr != nil {
				continue
			}
			capNames := q.CaptureNames()
			cursor := sitter.NewQueryCursor()
			matches := cursor.Matches(q, root, source)
			for {
				m := matches.Next()
				if m == nil {
					break
				}
				cm := buildCaptureMap(m, capNames)
				typeIdent := cm["type"]
				enclosing := cm["enclosing"]
				if typeIdent == nil {
					continue
				}
				typeName := typeIdent.Utf8Text(source)
				switch langName {
				case "typescript", "ts", "javascript", "js":
					if isPrimitiveTS(typeName) {
						continue
					}
				case "python", "py":
					if isPrimitivePython(typeName) {
						continue
					}
				case "rust", "rs":
					if isPrimitiveRust(typeName) {
						continue
					}
				}
				if symID, ok := symMap[typeName]; ok {
					// Same-file type reference: resolve immediately
					_ = enclosing
					edges = append(edges, store.Edge{
						FromID:         moduleID,
						ToID:           symID,
						EdgeType:       "references",
						Confidence:     1.0,
						ConfidenceTier: "EXTRACTED",
					})
				}
			}
			cursor.Close()
			q.Close()
		}
	}

	// ── Call resolution (same-file) ──
	var rawCalls []RawCall
	if callExt, ok := ext.(CallExtractor); ok {
		for label, qs := range callExt.CallQueries() {
			_ = label
			q, qErr := sitter.NewQuery(ext.Language(), qs)
			if qErr != nil {
				continue
			}
			capNames := q.CaptureNames()
			cursor := sitter.NewQueryCursor()
			matches := cursor.Matches(q, root, source)
			for {
				m := matches.Next()
				if m == nil {
					break
				}
				cm := buildCaptureMap(m, capNames)

				// Determine call type from captures
				calleeNode := cm["callee"]
				callNode := cm["node"]
				memberNode := cm["member"]
				if calleeNode == nil || callNode == nil {
					continue
				}

				calleeName := calleeNode.Utf8Text(source)

				// For same-file calls: attach to file node; linker in P2d refines caller
				isMember := memberNode != nil
				isNew := strings.Contains(qs, "new_expression")

				if symID, ok := symMap[calleeName]; ok {
					// Same-file: resolve immediately
					edges = append(edges, store.Edge{
						FromID:         moduleID, // file-level edge by default
						ToID:           symID,
						EdgeType:       "calls",
						Confidence:     1.0,
						ConfidenceTier: "EXTRACTED",
					})
				} else {
					rawCalls = append(rawCalls, RawCall{
						CalleeName:   calleeName,
						CallerNodeID: moduleID,
						SourceFile:   sourcePath,
						SourceLoc:    fmt.Sprintf("%d", callNode.StartPosition().Row+1),
						Lang:         langName,
						IsMemberCall: isMember,
						IsNewCall:    isNew,
						Receiver:     "", // populated for member calls if available
					})
				}
			}
			cursor.Close()
			q.Close()
		}
	}

	return nodes, edges, rawCalls, nil
}

// NewForLang returns the appropriate Extractor for the given language name, or nil.
func NewForLang(lang string) Extractor {
	switch lang {
	case "typescript", "ts", "javascript", "js":
		return &TSExt{}
	case "python", "py":
		return &PyExt{}
	case "rust", "rs":
		return &RsExt{}
	default:
		return nil
	}
}

// FileExts returns file extensions for a language.
func FileExts(lang string) []string {
	switch lang {
	case "typescript", "ts":
		return []string{".ts", ".tsx"}
	case "javascript", "js":
		return []string{".js", ".jsx"}
	case "python", "py":
		return []string{".py"}
	case "rust", "rs":
		return []string{".rs"}
	default:
		return nil
	}
}

// isPrimitiveTS returns true for TS/JS built-in types that should not
// produce reference edges.
func isPrimitiveTS(name string) bool {
	switch name {
	case "string", "number", "boolean", "void", "any", "never", "unknown",
		"null", "undefined", "bigint", "symbol", "object",
		"Array", "Map", "Set", "Promise", "Record", "Partial", "Required",
		"Readonly", "Pick", "Omit", "Exclude", "Extract", "NonNullable",
		"ReturnType", "InstanceType", "ThisType", "OmitThisParameter",
		"ThisParameterType", "typeof", "keyof":
		return true
	}
	return false
}

// buildCaptureMap builds a map of capture name → node for a match.
func buildCaptureMap(m *sitter.QueryMatch, capNames []string) map[string]*sitter.Node {
	mc := make(map[string]*sitter.Node, len(m.Captures))
	for _, cap := range m.Captures {
		idx := int(cap.Index)
		if idx >= 0 && idx < len(capNames) {
			mc[capNames[idx]] = &cap.Node
		}
	}
	return mc
}

func isPrimitivePython(name string) bool {
	switch name {
	case "str", "int", "float", "bool", "list", "dict", "tuple", "set",
		"None", "Optional", "Union", "Any", "Callable", "Iterable", "Type",
		"bytes", "complex", "frozenset", "range", "slice", "type":
		return true
	}
	return false
}

func isPrimitiveRust(name string) bool {
	switch name {
	case "i8", "i16", "i32", "i64", "i128", "isize",
		"u8", "u16", "u32", "u64", "u128", "usize",
		"f32", "f64", "bool", "char", "str",
		"String", "Vec", "Option", "Result",
		"HashMap", "BTreeMap", "HashSet", "BTreeSet",
		"Box", "Rc", "Arc", "Cell", "RefCell", "Mutex":
		return true
	}
	return false
}
