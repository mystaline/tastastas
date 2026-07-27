// Package treesitter provides best-effort code extraction via tree-sitter.
// Used for TypeScript, Python, Rust where no embeddable Go compiler frontend exists.
// Calls are name-matched within file scope → Confidence 0.7 (vs Go's 1.0).
package treesitter

import (
	"fmt"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/mystaline-dev/tastastas/internal/store"
)

const maxDeclLen = 1 << 30 // effectively unlimited — full body flows to chunker

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

func Extract(projectID, sourcePath string, source []byte, langName string, ext Extractor) ([]store.Node, []store.Edge, error) {
	parser := sitter.NewParser()
	if err := parser.SetLanguage(ext.Language()); err != nil {
		return nil, nil, fmt.Errorf("ts %s: set lang: %w", langName, err)
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
			return nil, nil, fmt.Errorf("ts %s: query %s: %w", langName, qs, qErr)
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
			// ponytail: full declaration including body, capped below.
			if len(content) > maxDeclLen {
				content = content[:maxDeclLen] + "..."
			}

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
				FromID:     moduleID,
				ToID:       symID,
				EdgeType:   "defines",
				Confidence: 1.0,
			})
		}
		cursor.Close()
		q.Close()
	}

	// ── Import edges ──
	if r := ext.ImportRule(); r != nil {
		q, qErr := sitter.NewQuery(ext.Language(), r.query)
		if qErr == nil {
			capNames := q.CaptureNames()
			cursor := sitter.NewQueryCursor()
			matches := cursor.Matches(q, root, source)
			for {
				m := matches.Next()
				if m == nil {
					break
				}
				// Build a map: capture name string → node
				for _, cap := range m.Captures {
					idx := int(cap.Index)
					if idx >= len(capNames) {
						continue
					}
					_ = capNames[idx]
				}
				if path := r.srcFrom(m, source); path != "" {
					parts := strings.Split(path, "/")
					modName := parts[len(parts)-1]
					modID := fmt.Sprintf("%s/code:package/%s", projectID, strings.TrimSuffix(modName, ".ts"))
					edges = append(edges, store.Edge{
						FromID:     moduleID,
						ToID:       modID,
						EdgeType:   "imports",
						Confidence: 0.9,
					})
				}
			}
			cursor.Close()
			q.Close()
		}
	}

	return nodes, edges, nil
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
