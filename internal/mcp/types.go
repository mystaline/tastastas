// --- Input/Output types (Go types auto-generate MCP JSON schema) ---
package mcp

import (
	sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/mystaline-dev/tastastas/internal/chunker"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// --- Input/Output types (Go types auto-generate MCP JSON schema) ---

type InitOutput struct {
	Help string `json:"help"`
}

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
	ProjectID    string  `json:"project_id,omitempty"`
	Query        string  `json:"query"`
	Limit        int     `json:"limit,omitempty"`
	LinkThreshold float64 `json:"link_threshold,omitempty"` // override default 0.75
}

type RecallItem struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Excerpt        string         `json:"excerpt"`
	NodeType       string         `json:"node_type"`
	Score          float64        `json:"score"`
	MatchType      string         `json:"match_type"`
	PreviewChunks  []string       `json:"preview_chunks,omitempty"`  // first 3 chunk contents
	TotalChunks    int            `json:"total_chunks"`              // e.g. 15
	MoreAvailable  bool           `json:"more_available"`            // total_chunks > len(preview_chunks)
	NextChunkStart int            `json:"next_chunk_start"`          // where to resume paging, or -1
	Edges          []RecallEdge   `json:"edges,omitempty"`
	InferredEdges  []RecallEdge `json:"inferred_edges,omitempty"`
}

type RecallEdge struct {
	ToID       string  `json:"to_id"`
	ToTitle    string  `json:"to_title"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

type ChunkOutputItem struct {
	ID           string   `json:"id"`
	ParentNodeID string   `json:"parent_node_id"`
	ChunkIndex   int      `json:"chunk_index"`
	Type         string   `json:"type"`
	HeadingPath  []string `json:"heading_path,omitempty"`
	Content      string   `json:"content"`
	Language     string   `json:"language"`
	PrevChunkID  string   `json:"prev_chunk_id,omitempty"`
	NextChunkID  string   `json:"next_chunk_id,omitempty"`
}

type RecallOutput struct {
	Results []RecallItem      `json:"results"`
	Links   []ImplicitMCPLink `json:"links,omitempty"`
}

type RecallChunksInput struct {
	ProjectID    string `json:"project_id,omitempty"`
	ParentNodeID string `json:"parent_node_id"`
	ChunkStart   int    `json:"chunk_start"` // 0-based, inclusive
	ChunkEnd     int    `json:"chunk_end"`   // 0-based, exclusive; default chunk_start+3
}

type RecallChunksOutput struct {
	ParentNodeID   string            `json:"parent_node_id"`
	ParentTitle    string            `json:"parent_title"`
	TotalChunks    int               `json:"total_chunks"`
	ReturnedRange  string            `json:"returned_range"`   // e.g. "chunk 3-5 of 15"
	Chunks         []ChunkOutputItem `json:"chunks"`
	MoreAvailable  bool              `json:"more_available"`   // chunk_end < total_chunks
	NextChunkStart int               `json:"next_chunk_start"` // chunk_end, or -1 if done
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
	CWD       string `json:"cwd,omitempty"`       // project root to detect + ingest files from
	ProjectID string `json:"project_id,omitempty"`
	Scope     string `json:"scope,omitempty"`     // "cwd" | "subtree"
}

type IngestOutput struct {
	JobID             string `json:"job_id"`
	Status            string `json:"status"` // "running" | "done" | "error"
	NodesIngested    int    `json:"nodes_ingested"`
	EdgesCreated     int    `json:"edges_created"`
	ChunksCreated    int    `json:"chunks_created"`
	ConventionsInferred int    `json:"conventions_inferred,omitempty"`
	AutoLinked       int    `json:"auto_linked,omitempty"`
	ProposalsQueued  int    `json:"proposals_queued,omitempty"`
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
	HasNodes       bool              `json:"has_nodes"`
	HasChunks      bool              `json:"has_chunks"`
	HasEmbeddings  bool              `json:"has_embeddings"`
	HasEdges       bool              `json:"has_edges"`
	HasConventions bool              `json:"has_conventions"`
	StaleCount     int               `json:"stale_count"`
	NodeCount      int               `json:"node_count"`
	EdgeCount      int               `json:"edge_count"`
	ChunkCount     int               `json:"chunk_count"`
	VecCount       int               `json:"vec_count"`
	EdgeTypeCounts map[string]int    `json:"edge_type_counts,omitempty"`
}

type QueryGraphInput struct {
	NodeID    string   `json:"node_id"`
	EdgeTypes []string `json:"edge_types,omitempty"` // nil = all types
	Direction string   `json:"direction,omitempty"`  // "outgoing" | "incoming" | "both"; default "both"
	Limit     int      `json:"limit,omitempty"`      // default 20
}

type QueryGraphOutput struct {
	NodeID string       `json:"node_id"`
	Title  string       `json:"title"`
	Edges  []EdgeResult `json:"edges"`
}

type EdgeResult struct {
	Direction  string  `json:"direction"`   // "outgoing" | "incoming"
	NodeID     string  `json:"node_id"`     // the other node (to_id or from_id)
	NodeTitle  string  `json:"node_title"`
	NodeType   string  `json:"node_type"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

type ProjectGraphInput struct {
	ProjectID    string   `json:"project_id,omitempty"`
	MaxEdges     int      `json:"max_edges,omitempty"`     // default 5000
	EdgeTypes    []string `json:"edge_types,omitempty"`    // empty = all non-proposed types
}

type ProjectGraphOutput struct {
	ProjectID  string       `json:"project_id"`
	TotalEdges int          `json:"total_edges"`
	Returned   int          `json:"returned"`
	Nodes      []GraphNode  `json:"nodes"`
	Edges      []GraphEdge  `json:"edges"`
}

type GraphNode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Type   string `json:"type"`
	Group  string `json:"group"`
	Weight int    `json:"weight"`
}

type GraphEdge struct {
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

type JobStatusInput struct {
	JobID string `json:"id"`
}

type JobStatusOutput struct {
	ID              string `json:"id"`
	Status          string `json:"status"` // "running" | "done" | "error"
	Phase           string `json:"phase,omitempty"` // "walking" | "embedding" | "persisting" | "" (done/error)
	Nodes           int    `json:"nodes_ingested,omitempty"`
	Edges           int    `json:"edges_created,omitempty"`
	Chunks          int    `json:"chunks_created,omitempty"`
	ChunksTotal     int    `json:"chunks_total,omitempty"` // total chunks queued for embedding, once known
	Conventions     int    `json:"conventions_inferred,omitempty"`
	AutoLinked      int    `json:"auto_linked,omitempty"`
	ProposalsQueued int    `json:"proposals_queued,omitempty"`
	Error           string `json:"error,omitempty"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at,omitempty"`
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
