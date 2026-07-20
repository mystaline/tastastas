package chunker

import (
	"fmt"
	"regexp"
	"strings"
)

// ChunkType represents the type of content being chunked.
type ChunkType string

const (
	ChunkTypeConversationFact ChunkType = "conversation_fact"
	ChunkTypeMarkdownSection  ChunkType = "markdown_section"
	ChunkTypeCodeFunction     ChunkType = "code_function"
	ChunkTypeCodeMethod       ChunkType = "code_method"
	ChunkTypeCodeStruct       ChunkType = "code_struct"
	ChunkTypeObsidianSection  ChunkType = "obsidian_section"
)

// Chunk represents a single chunk of content with metadata.
type Chunk struct {
	ID           string
	ParentNodeID string
	ChunkIndex   int
	Type         ChunkType
	HeadingPath  []string
	Content      string
	Language     string
	StartLine    int
	EndLine      int
	StartByte    int
	EndByte      int
}

// Config holds chunking configuration.
type Config struct {
	MaxChunkSize  int // max chars per chunk (default: 1200)
	OverlapSize   int // overlap between chunks (default: 150)
	MinChunkSize  int // minimum chunk size to keep (default: 100)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxChunkSize: 1200,
		OverlapSize:  150,
		MinChunkSize: 100,
	}
}

// ChunkMarkdown splits markdown content by headings (ATX style: # ## ###).
// Each chunk contains the heading path and content until the next same/higher level heading.
func ChunkMarkdown(parentNodeID, content string, cfg Config) ([]Chunk, error) {
	if cfg.MaxChunkSize == 0 {
		cfg = DefaultConfig()
	}

	// Regex to find ATX headings: # Heading, ## Heading, ### Heading, etc.
	headingRe := regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

	lines := strings.Split(content, "\n")
	var chunks []Chunk
	var currentChunk strings.Builder
	var currentHeadings []string
	chunkIndex := 0
	inCodeBlock := false

	for lineNum, line := range lines {
		// Track code blocks to avoid splitting inside them
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCodeBlock = !inCodeBlock
		}

		matches := headingRe.FindStringSubmatch(line)
		if matches != nil && !inCodeBlock {
			// Found a heading - flush current chunk if it has content
			if currentChunk.Len() > cfg.MinChunkSize {
				chunks = append(chunks, Chunk{
					ID:           fmt.Sprintf("%s/chunk/%d", parentNodeID, chunkIndex),
					ParentNodeID: parentNodeID,
					ChunkIndex:   chunkIndex,
					Type:         ChunkTypeMarkdownSection,
					HeadingPath:  append([]string{}, currentHeadings...),
					Content:      strings.TrimSpace(currentChunk.String()),
					Language:     "markdown",
					StartLine:    max(1, lineNum-currentChunk.Len()/80),
					EndLine:      lineNum,
				})
				chunkIndex++
				currentChunk.Reset()
			}

			// Update heading path
			level := len(matches[1]) // number of # characters
			title := strings.TrimSpace(matches[2])

			// Truncate heading path to this level
			if level <= len(currentHeadings) {
				currentHeadings = currentHeadings[:level-1]
			}
			currentHeadings = append(currentHeadings, title)
		}

		currentChunk.WriteString(line)
		currentChunk.WriteString("\n")
	}

	// Flush remaining content
	if currentChunk.Len() > cfg.MinChunkSize {
		chunks = append(chunks, Chunk{
			ID:           fmt.Sprintf("%s/chunk/%d", parentNodeID, chunkIndex),
			ParentNodeID: parentNodeID,
			ChunkIndex:   chunkIndex,
			Type:         ChunkTypeMarkdownSection,
			HeadingPath:  append([]string{}, currentHeadings...),
			Content:      strings.TrimSpace(currentChunk.String()),
			Language:     "markdown",
			StartLine:    max(1, len(lines)-currentChunk.Len()/80),
			EndLine:      len(lines),
		})
	}

	// Handle oversized chunks by splitting with overlap
	return splitOversizedChunks(chunks, cfg), nil
}

// splitOversizedChunks splits chunks that exceed MaxChunkSize with overlap.
func splitOversizedChunks(chunks []Chunk, cfg Config) []Chunk {
	var result []Chunk
	for _, chunk := range chunks {
		if len(chunk.Content) <= cfg.MaxChunkSize {
			result = append(result, chunk)
			continue
		}

		// Split by paragraphs first, then by sentences, then hard split
		parts := splitByParagraphs(chunk.Content, cfg.MaxChunkSize)
		for i, part := range parts {
			if len(part) < cfg.MinChunkSize {
				continue
			}
			newChunk := chunk
			newChunk.ID = fmt.Sprintf("%s/sub/%d", chunk.ID, i)
			newChunk.ChunkIndex = len(result)
			newChunk.Content = part
			result = append(result, newChunk)
		}
	}
	return result
}

// splitByParagraphs splits text by double newlines, then by sentences if needed.
func splitByParagraphs(text string, maxSize int) []string {
	paragraphs := strings.Split(text, "\n\n")
	var result []string
	var current strings.Builder

	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if current.Len()+len(p)+2 <= maxSize {
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(p)
		} else {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			// If single paragraph is still too big, split by sentences
			if len(p) > maxSize {
				sentences := splitBySentences(p, maxSize)
				for _, s := range sentences {
					if len(s) > maxSize {
						// Hard split
						for i := 0; i < len(s); i += maxSize {
							end := min(i+maxSize, len(s))
							result = append(result, s[i:end])
						}
					} else {
						result = append(result, s)
					}
				}
			} else {
				current.WriteString(p)
			}
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

// splitBySentences splits text by sentence boundaries.
func splitBySentences(text string, maxSize int) []string {
	// Simple sentence split - handles . ! ? followed by space or end
	re := regexp.MustCompile(`([.!?])\s+`)
	parts := re.Split(text, -1)
	delims := re.FindAllString(text, -1)

	var result []string
	var current strings.Builder

	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Add delimiter back (except for last)
		if i < len(delims) {
			part += delims[i]
		}

		if current.Len()+len(part)+1 <= maxSize {
			if current.Len() > 0 {
				current.WriteString(" ")
			}
			current.WriteString(part)
		} else {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			if len(part) > maxSize {
				// Hard split
				for j := 0; j < len(part); j += maxSize {
					end := min(j+maxSize, len(part))
					result = append(result, part[j:end])
				}
			} else {
				current.WriteString(part)
			}
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

// ChunkConversationFact creates a single chunk from an extracted conversation fact.
// This is used by extract_and_remember pipeline.
func ChunkConversationFact(parentNodeID, title, content string, importance float64, cfg Config) []Chunk {
	if cfg.MaxChunkSize == 0 {
		cfg = DefaultConfig()
	}
	return []Chunk{{
		ID:           fmt.Sprintf("%s/fact/0", parentNodeID),
		ParentNodeID: parentNodeID,
		ChunkIndex:   0,
		Type:         ChunkTypeConversationFact,
		HeadingPath:  []string{title},
		Content:      content,
		Language:     "text",
	}}
}