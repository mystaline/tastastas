// --- Input/Output types (Go types auto-generate MCP JSON schema) ---
package mcp

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/mystaline-dev/tastastas/internal/chunker"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// --- Input/Output types (Go types auto-generate MCP JSON schema) ---

type RememberInput struct {
	ProjectID  string  `json:"project_id,omitempty"`
	NodeType   string  `json:"node_type,omitempty"`
	ID         string  `json:"id,omitempty"`
	Title      string  `json:"title,omitempty"`
	Content    string  `json:"content"`
	Importance float64 `json:"importance,omitempty"`
}
type RememberOutput struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type RecallInput struct {
	ProjectID string `json:"project_id,omitempty"`
	Query     string `json:"query"`
	Limit     int    `json:"limit,omitempty"`
}
type RecallItem struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Content  string  `json:"content"`
	NodeType string  `json:"node_type"`
	Score    float64 `json:"score"`
}
type RecallOutput struct {
	Results []RecallItem       `json:"results"`
	Links   []ImplicitMCPLink  `json:"links,omitempty"`
}

type ImplicitMCPLink struct {
	FromChunkID string  `json:"from_chunk_id"`
	ToChunkID   string  `json:"to_chunk_id"`
	Cosine      float64 `json:"cosine"`
}

type ForgetInput struct {
	ID string `json:"id"`
}
type ForgetOutput struct {
	Status string `json:"status"`
}

type LinkInput struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence,omitempty"`
}
type LinkOutput struct {
	Status string `json:"status"`
}

type IngestInput struct {
	Adapter    string `json:"adapter"`
	Root       string `json:"root"`
	ConfigPath string `json:"config_path,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}
type IngestOutput struct {
	NodesIngested int `json:"nodes_ingested"`
	EdgesCreated  int `json:"edges_created"`
	ChunksCreated int `json:"chunks_created"`
}

type ExtractAndRememberInput struct {
	ProjectID    string `json:"project_id,omitempty"`
	Conversation string `json:"conversation"`
}
type ExtractAndRememberOutput struct {
	Facts []ExtractedFactResult `json:"facts"`
}
type ExtractedFactResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "created" | "merged"
}

type CheckImpactInput struct {
	ID       string `json:"id"`
	MaxDepth int    `json:"max_depth,omitempty"`
}
type CheckImpactOutput struct {
	StaleNodes []StaleNode `json:"stale_nodes"`
}
type StaleNode struct {
	ID       string `json:"id"`
	NodeType string `json:"node_type"`
}

// chunkForNode splits a node's content into chunks suitable for embedding.
// For Go code nodes, uses tree-sitter to split by function/type declarations.
// For Markdown-like nodes, uses heading-based chunking.
// All other types get a single chunk.
func chunkForNode(n store.Node, cfg chunker.Config, goLang, tsLang *sitter.Language) []store.Chunk {
	switch n.NodeType {
	case "prd", "api-spec", "erd", "test-case", "generic-doc", "obsidian-note":
		chunks, _ := chunker.ChunkMarkdown(n.ID, n.Content, cfg)
		result := make([]store.Chunk, len(chunks))
		for i, c := range chunks {
			result[i] = store.Chunk{
				ID:           c.ID,
				ParentNodeID: c.ParentNodeID,
				ChunkIndex:   c.ChunkIndex,
				Type:         string(c.Type),
				HeadingPath:  c.HeadingPath,
				Content:      c.Content,
				Language:     c.Language,
			}
		}
		return result

	case "go":
		if goLang != nil {
			chunks, err := chunker.ChunkGoCode(n.ID, n.Content, goLang, cfg)
			if err == nil && len(chunks) > 0 {
				return chunkSlice(n.ID, chunks)
			}
		}
		fallthrough

	case "typescript", "javascript":
		if tsLang != nil {
			chunks, err := chunker.ChunkTypeScript(n.ID, n.Content, tsLang, cfg)
			if err == nil && len(chunks) > 0 {
				return chunkSlice(n.ID, chunks)
			}
		}
		fallthrough

	default:
		return []store.Chunk{{
			ID:           n.ID + "/chunk/0",
			ParentNodeID: n.ID,
			ChunkIndex:   0,
			Type:         "conversation_fact",
			HeadingPath:  []string{},
			Content:      n.Content,
			Language:     "text",
		}}
	}
}

// chunkSlice converts chunker.Chunk slice to store.Chunk slice.
func chunkSlice(parentID string, chunks []chunker.Chunk) []store.Chunk {
	result := make([]store.Chunk, len(chunks))
	for i, c := range chunks {
		result[i] = store.Chunk{
			ID:           c.ID,
			ParentNodeID: c.ParentNodeID,
			ChunkIndex:   c.ChunkIndex,
			Type:         string(c.Type),
			HeadingPath:  c.HeadingPath,
			Content:      c.Content,
			Language:     c.Language,
		}
	}
	return result
}
