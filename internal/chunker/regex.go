package chunker

import (
	"fmt"
	"regexp"
	"strings"
)

// declarationStart matches a line that begins a top-level declaration
// in common programming languages. Used as fallback when tree-sitter fails.
var declarationStart = regexp.MustCompile(`^(func\s+|type\s+|const\s+|var\s+|function\s+|class\s+|interface\s+|struct\s+|enum\s+|trait\s+|impl\s+|def\s+|async\s+(fn|function)\s+)`)

// exportPrefix matches export/default export prefixes in TS/JS.
var exportPrefix = regexp.MustCompile(`^export(\s+default)?\s+`)

func ChunkCodeByPattern(parentNodeID, source, language string, cfg Config) ([]Chunk, error) {
	if cfg.MaxChunkSize == 0 {
		cfg = DefaultConfig()
	}
	lines := strings.Split(source, "\n")
	if len(lines) == 0 {
		return nil, nil
	}

	var chunks []Chunk
	chunkIndex := 0
	startLine := 0

	flush := func(endLine int) {
		if endLine-startLine < 1 {
			return
		}
		text := strings.Join(lines[startLine:endLine], "\n")
		if len(strings.TrimSpace(text)) < cfg.MinChunkSize {
			return
		}
		// Derive a heading from the first line
		heading := derivePatternHeading(lines[startLine])
		chunks = append(chunks, Chunk{
			ID:          fmt.Sprintf("%s/chunk/%d", parentNodeID, chunkIndex),
			ParentNodeID: parentNodeID,
			ChunkIndex:  chunkIndex,
			Type:        ChunkTypeCodeFunction,
			HeadingPath: []string{heading},
			Content:     strings.TrimSpace(text),
			Language:    language,
			StartLine:   startLine + 1,
			EndLine:     endLine,
		})
		chunkIndex++
		startLine = endLine
	}

	inBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track braces for struct/interface body detection
		if strings.HasPrefix(trimmed, "{") {
			inBlock = true
		}
		if strings.HasPrefix(trimmed, "}") {
			inBlock = false
		}

		// Check if this line starts a new declaration
		if isDeclarationLine(trimmed) && !inBlock {
			if i > 0 {
				flush(i)
			}
			startLine = i
		}
	}
	flush(len(lines))

	// Apply oversize splitting
	chunks = splitOversizedChunks(chunks, cfg)
	return chunks, nil
}

// isDeclarationLine checks if a trimmed line looks like a top-level declaration.
func isDeclarationLine(line string) bool {
	if declarationStart.MatchString(line) {
		return true
	}
	// Check exported declarations (TypeScript): export function, export class, etc.
	if strings.HasPrefix(line, "export ") {
		stripped := exportPrefix.ReplaceAllString(line, "")
		return declarationStart.MatchString(stripped)
	}
	return false
}

// derives the last word in a line that looks like an identifier.
var identSuffix = regexp.MustCompile(`[.\w]+\s*$`)

// derivePatternHeading extracts a short heading from the first line of a chunk.
// e.g. "func (s *Store) GetNodes" → "GetNodes"
// e.g. "export default function handler(req, res) {" → "handler"
func derivePatternHeading(line string) string {
	// Strip export and async prefixes before matching
	stripped := exportPrefix.ReplaceAllString(line, "")
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^(async\s+)?(func|function|def|fn)\s+(\([^)]*\)\s+)?([.\w]+)`),
		regexp.MustCompile(`^(type|class|struct|interface|enum|trait)\s+(\w+)`),
		regexp.MustCompile(`^(const|var|let)\s+(\w+)`),
	}
	for _, re := range patterns {
		m := re.FindStringSubmatch(stripped)
		if len(m) > 2 && m[len(m)-1] != "" {
			return m[len(m)-1]
		}
		if len(m) > 1 {
			return m[1]
		}
	}
	// Fallback on stripped line then original
	for _, s := range []string{stripped, line} {
		if m := identSuffix.FindString(s); m != "" && len(m) < 60 {
			return m
		}
	}
	if len(line) > 40 {
		return line[:40] + "..."
	}
	return line
}
