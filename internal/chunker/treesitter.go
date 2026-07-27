package chunker

import (
	"fmt"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// ChunkCode splits a source file into chunks by top-level declarations
// using tree-sitter for precise AST boundaries. Works with any tree-sitter
// grammar — caller provides the language and a heading extractor.
//
// The headingFn receives the node and source bytes; it must return the
// identifier name (function name, class name, etc.). If it returns "", the
// node is skipped (e.g. import statements, comments).
func ChunkCode(
	parentNodeID, source, language string,
	lang *sitter.Language,
	headingFn func(*sitter.Node, []byte) string,
	cfg Config,
) ([]Chunk, error) {
	if cfg.MaxChunkSize == 0 {
		cfg = DefaultConfig()
	}

	parser := sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		return nil, fmt.Errorf("chunker: set %s language: %w", language, err)
	}
	defer parser.Close()

	sourceBytes := []byte(source)
	tree := parser.Parse(sourceBytes, nil)
	defer tree.Close()

	root := tree.RootNode()
	lines := strings.Split(source, "\n")

	var chunks []Chunk
	chunkIndex := 0

	// Determine chunk type from node grammar name.
	chunkType := func(name string) ChunkType {
		switch name {
		case "class_declaration":
			return ChunkTypeCodeStruct
		default:
			return ChunkTypeCodeFunction
		}
	}

	for i := uint(0); i < root.ChildCount(); i++ {
		node := root.Child(i)
		name := node.GrammarName()

		// Unwrap export_statement → get the inner declaration
		if name == "export_statement" {
			for j := uint(0); j < node.ChildCount(); j++ {
				inner := node.Child(j)
				if inner.IsNamed() && inner.GrammarName() != "export" {
					node = inner
					name = inner.GrammarName()
					break
				}
			}
		}

		switch name {
		case "function_declaration", "method_declaration", "method_definition", "class_declaration":
			sub := extractOrDescend(
				parentNodeID,
				sourceBytes,
				lines,
				node,
				&chunkIndex,
				chunkType(name),
				language,
				cfg,
				headingFn,
				nil,
			)
			chunks = append(chunks, sub...)

		case "type_declaration":
			// Split compound type declarations (Go-specific)
			for j := uint(0); j < node.ChildCount(); j++ {
				child := node.Child(j)
				if child.GrammarName() == "type_spec" {
					sub := extractOrDescend(
						parentNodeID,
						sourceBytes,
						lines,
						child,
						&chunkIndex,
						ChunkTypeCodeStruct,
						language,
						cfg,
						headingFn,
						nil,
					)
					chunks = append(chunks, sub...)
				}
			}
		}
	}

	return splitOversizedChunks(chunks, cfg), nil
}

// extractOrDescend extracts a code chunk from a tree-sitter node.
// If the node is oversized (> MaxChunkSize), tries to descend into its
// named children to split at structural boundaries instead of hard-splitting.
// headingPrefix accumulates parent headings (e.g. ["ClassName"] for methods inside a class).
func extractOrDescend(
	parentNodeID string,
	source []byte,
	lines []string,
	node *sitter.Node,
	idx *int,
	ctype ChunkType,
	language string,
	cfg Config,
	headingFn func(*sitter.Node, []byte) string,
	headingPrefix []string,
) []Chunk {
	heading := headingFn(node, source)
	if heading == "" {
		return nil
	}

	startByte := node.StartByte()
	endByte := node.EndByte()
	size := int(endByte - startByte)
	text := string(source[startByte:endByte])
	if len(strings.TrimSpace(text)) < cfg.MinChunkSize {
		return nil
	}

	// If within budget, extract as a single chunk with full heading path
	if size <= cfg.MaxChunkSize {
		fullHeading := make([]string, 0, len(headingPrefix)+1)
		fullHeading = append(fullHeading, headingPrefix...)
		fullHeading = append(fullHeading, heading)
		chunk := &Chunk{
			ID:           fmt.Sprintf("%s/chunk/%d", parentNodeID, *idx),
			ParentNodeID: parentNodeID,
			ChunkIndex:   *idx,
			Type:         ctype,
			HeadingPath:  fullHeading,
			Content:      strings.TrimSpace(text),
			Language:     language,
			StartLine:    int(node.StartPosition().Row) + 1,
			EndLine:      int(node.EndPosition().Row) + 1,
			StartByte:    int(startByte),
			EndByte:      int(endByte),
		}
		*idx++
		return []Chunk{*chunk}
	}

	// Oversized — try to descend into named children that have valid headings
	childHeading := append(headingPrefix, heading)
	var subChunks []Chunk
	for j := uint(0); j < node.ChildCount(); j++ {
		child := node.Child(j)
		if !child.IsNamed() {
			continue
		}
		childSize := int(child.EndByte() - child.StartByte())
		if childSize < cfg.MinChunkSize {
			continue
		}
		ch := headingFn(child, source)
		if ch == "" {
			continue
		}
		// Recurse into child
		sub := extractOrDescend(parentNodeID, source, lines, child, idx, ctype, language, cfg, headingFn, childHeading)
		if len(sub) > 0 {
			subChunks = append(subChunks, sub...)
		}
	}

	if len(subChunks) > 0 {
		return subChunks
	}

	// No valid structural descendants — hard-split as last resort.
	// This handles leaf nodes (a single 3000-char string, a minified line).
	for j := 0; j < len(text); j += cfg.MaxChunkSize {
		end := j + cfg.MaxChunkSize
		if end > len(text) {
			end = len(text)
		}
		fullHeading := make([]string, 0, len(headingPrefix)+1)
		fullHeading = append(fullHeading, headingPrefix...)
		fullHeading = append(fullHeading, heading)
		sub := &Chunk{
			ID:           fmt.Sprintf("%s/chunk/%d", parentNodeID, *idx),
			ParentNodeID: parentNodeID,
			ChunkIndex:   *idx,
			Type:         ctype,
			HeadingPath:  fullHeading,
			Content:      strings.TrimSpace(text[j:end]),
			Language:     language,
			StartLine:    int(node.StartPosition().Row) + 1,
			EndLine:      int(node.EndPosition().Row) + 1,
			StartByte:    int(startByte) + j,
			EndByte:      int(startByte) + end,
		}
		*idx++
		subChunks = append(subChunks, *sub)
	}
	return subChunks
}

// deriveHeading extracts the identifier from a tree-sitter node.
// Language-agnostic: checks for identifier/type_identifier children.
func deriveHeading(node *sitter.Node, source []byte) string {
	for i := uint(0); i < node.ChildCount(); i++ {
		child := node.Child(i)
		switch child.GrammarName() {
		case "identifier", "field_identifier", "type_identifier", "property_identifier":
			return child.Utf8Text(source)
		}
	}
	return ""
}

// ChunkGoCode is a convenience wrapper that uses ChunkCode with Go grammar.
func ChunkGoCode(parentNodeID, source string, lang *sitter.Language, cfg Config) ([]Chunk, error) {
	return ChunkCode(parentNodeID, source, "go", lang, deriveHeading, cfg)
}

// ChunkTypeScript splits a TypeScript/JavaScript source file into chunks
// by functions, classes, and exported declarations.
func ChunkTypeScript(parentNodeID, source string, lang *sitter.Language, cfg Config) ([]Chunk, error) {
	return ChunkCode(parentNodeID, source, "typescript", lang, deriveHeading, cfg)
}
