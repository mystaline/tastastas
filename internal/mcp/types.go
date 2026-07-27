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
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	NodeType  string  `json:"node_type"`
	Score     float64 `json:"score"`
	MatchType string  `json:"match_type"`
}

type RecallOutput struct {
	Results []RecallItem      `json:"results"`
	Links   []ImplicitMCPLink `json:"links,omitempty"`
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

type OnboardInput struct {
	CWD       string `json:"cwd,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Scope     string `json:"scope,omitempty"` // "cwd" | "subtree"
}

type OnboardOutput struct {
	ProjectID           string   `json:"project_id"`
	JobID               string   `json:"job_id,omitempty"`
	Status              string   `json:"status,omitempty"`
	AlreadyOnboarded    bool     `json:"already_onboarded,omitempty"`
	DetectedAdapters    []string `json:"detected_adapters"`
	CodeSymbols         int      `json:"code_symbols"`
	CallGraphEdges      int      `json:"call_graph_edges"`
	ImportEdges         int      `json:"import_edges"`
	GenericDocs         int      `json:"generic_docs"`
	ConventionsInferred int      `json:"conventions_inferred"`
	AutoLinked          int      `json:"auto_linked"`
	ProposalsQueued     int      `json:"proposals_queued"`
	FilesWalked         int      `json:"files_walked"`
	DurationMs          int64    `json:"duration_ms"`
}

type OnboardCheckInput struct {
	ProjectID string `json:"project_id,omitempty"`
}

type OnboardCheckOutput struct {
	HasNodes       bool `json:"has_nodes"`
	HasChunks      bool `json:"has_chunks"`
	HasEmbeddings  bool `json:"has_embeddings"`
	HasEdges       bool `json:"has_edges"`
	HasConventions bool `json:"has_conventions"`
	StaleCount     int  `json:"stale_count"`
	NodeCount      int  `json:"node_count"`
	EdgeCount      int  `json:"edge_count"`
	ChunkCount     int  `json:"chunk_count"`
	VecCount       int  `json:"vec_count"`
}

type BuildGraphInput struct {
	ProjectID string `json:"project_id,omitempty"`
}

type BuildGraphOutput struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

type JobStatusInput struct {
	JobID string `json:"id"`
}

type JobStatusOutput struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Nodes     int    `json:"nodes_ingested,omitempty"`
	Edges     int    `json:"edges_created,omitempty"`
	Chunks    int    `json:"chunks_created,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// chunkForNode splits a node's content into chunks suitable for embedding.
// For Go code nodes, uses tree-sitter to split by function/type declarations.
// For Markdown-like nodes, uses heading-based chunking.
// All other types get a single chunk.
func chunkForNode(
	n store.Node,
	cfg chunker.Config,
	goLang, tsLang *sitter.Language,
) []store.Chunk {
	switch n.NodeType {
	case "prd", "api-spec", "erd", "test-case", "generic-doc", "obsidian-note":
		chunks, _ := chunker.ChunkMarkdown(n.ID, n.Content, cfg)
		result := make([]store.Chunk, len(chunks))
		for i, c := range chunks {
			result[i] = store.Chunk{
				ID:            c.ID,
				ParentNodeID:  c.ParentNodeID,
				ChunkIndex:    c.ChunkIndex,
				Type:          string(c.Type),
				HeadingPath:   c.HeadingPath,
				Content:       c.Content,
				Language:      c.Language,
				SourceAdapter: n.SourceAdapter,
			}
		}
		return result

	case "go":
		if goLang != nil {
			chunks, err := chunker.ChunkGoCode(n.ID, n.Content, goLang, cfg)
			if err == nil && len(chunks) > 0 {
				return chunkSlice(chunks, n.SourceAdapter)
			}
		}
		fallthrough

	case "typescript", "javascript":
		if tsLang != nil {
			chunks, err := chunker.ChunkTypeScript(n.ID, n.Content, tsLang, cfg)
			if err == nil && len(chunks) > 0 {
				return chunkSlice(chunks, n.SourceAdapter)
			}
		}
		fallthrough

	default:
		if n.Content == "" {
			return nil
		}
		return []store.Chunk{{
			ID:            n.ID + "/chunk/0",
			ParentNodeID:  n.ID,
			ChunkIndex:    0,
			Type:          "conversation_fact",
			HeadingPath:   []string{},
			Content:       n.Content,
			Language:      "text",
			SourceAdapter: n.SourceAdapter,
		}}
	}
}

// chunkSlice converts chunker.Chunk slice to store.Chunk slice,
// linking prev/next IDs along the way.
func chunkSlice(chunks []chunker.Chunk, sourceAdapter string) []store.Chunk {
	result := make([]store.Chunk, len(chunks))
	for i, c := range chunks {
		sc := store.Chunk{
			ID:            c.ID,
			ParentNodeID:  c.ParentNodeID,
			ChunkIndex:    c.ChunkIndex,
			Type:          string(c.Type),
			HeadingPath:   c.HeadingPath,
			Content:       c.Content,
			Language:      c.Language,
			SourceAdapter: sourceAdapter,
		}
		if i > 0 {
			sc.PrevChunkID = result[i-1].ID
		}
		if i+1 < len(chunks) {
			sc.NextChunkID = chunks[i+1].ID
		}
		result[i] = sc
	}
	return result
}
