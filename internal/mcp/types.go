// --- Input/Output types (Go types auto-generate MCP JSON schema) ---
package mcp

import (
	"github.com/mystaline/tastastas/internal/store"
)

// --- Input/Output types (Go types auto-generate MCP JSON schema) ---

type InitOutput struct {
	Help     string              `json:"help"`
	ModelID  string              `json:"model_id,omitempty"`
	Projects []store.ProjectInfo `json:"projects"`
}

type RememberInput struct {
	ProjectID  string   `json:"project_id,omitempty"`
	NodeType   string   `json:"node_type,omitempty"`
	ID         string   `json:"id,omitempty"`
	Title      string   `json:"title,omitempty"`
	Content    string   `json:"content"`
	Importance float64  `json:"importance,omitempty"`
	Links      []string `json:"links,omitempty"` // node IDs this fact references — creates "references" edges
}

type RememberOutput struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type RecallInput struct {
	ProjectID     string   `json:"project_id,omitempty"`
	ProjectIDs    []string `json:"project_ids,omitempty"` // explicit multi-project filter
	Query         string   `json:"query"`
	Limit         int      `json:"limit,omitempty"`
	LinkThreshold float64  `json:"link_threshold,omitempty"` // override default 0.75
	AllProjects   bool     `json:"all_projects,omitempty"`   // search all projects
}

type LinkProjectsInput struct {
	ProjectID string `json:"project_id"`
}

type LinkProjectsOutput struct {
	EdgesCreated int `json:"edges_created"`
}

type RecallItem struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	Excerpt        string       `json:"excerpt"`
	NodeType       string       `json:"node_type"`
	Score          float64      `json:"score"`
	MatchType      string       `json:"match_type"`
	PreviewChunks  []string     `json:"preview_chunks,omitempty"` // first 3 chunk contents
	TotalChunks    int          `json:"total_chunks"`             // e.g. 15
	MoreAvailable  bool         `json:"more_available"`           // total_chunks > len(preview_chunks)
	NextChunkStart int          `json:"next_chunk_start"`         // where to resume paging, or -1
	Edges          []RecallEdge `json:"edges,omitempty"`
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
	Warning string            `json:"warning,omitempty"`
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
	ReturnedRange  string            `json:"returned_range"` // e.g. "chunk 3-5 of 15"
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
	CWD           string `json:"cwd,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	Scope         string `json:"scope,omitempty"`
	Ref           string `json:"ref,omitempty"`
}

type IngestOutput struct {
	JobID               string `json:"job_id"`
	Status              string `json:"status"` // "running" | "done" | "error"
	NodesIngested       int    `json:"nodes_ingested"`
	EdgesCreated        int    `json:"edges_created"`
	ChunksCreated       int    `json:"chunks_created"`
	ConventionsInferred int    `json:"conventions_inferred,omitempty"`
	AutoLinked          int    `json:"auto_linked,omitempty"`
	ProposalsQueued     int    `json:"proposals_queued,omitempty"`
	Ref                 string `json:"ref,omitempty"`
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
	CWD           string `json:"cwd,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	Scope         string `json:"scope,omitempty"`
	Ref           string `json:"ref,omitempty"`
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
	Ref                 string   `json:"ref,omitempty"`
}

type OnboardCheckInput struct {
	ProjectID string `json:"project_id,omitempty"`
	ModelID   string `json:"model_id,omitempty"` // filter vectors to this model (empty = all models)
}

type OnboardCheckOutput struct {
	HasNodes       bool           `json:"has_nodes"`
	HasChunks      bool           `json:"has_chunks"`
	HasEmbeddings  bool           `json:"has_embeddings"`
	HasEdges       bool           `json:"has_edges"`
	HasConventions bool           `json:"has_conventions"`
	StaleCount     int            `json:"stale_count"`
	NodeCount      int            `json:"node_count"`
	EdgeCount      int            `json:"edge_count"`
	ChunkCount     int            `json:"chunk_count"`
	VecCount       int            `json:"vec_count"`
	EdgeTypeCounts map[string]int `json:"edge_type_counts,omitempty"`
}

type CheckRecentInput struct {
	ProjectID string `json:"project_id,omitempty"`
	Days      int    `json:"days,omitempty"` // default 7
}

type CheckRecentOutput struct {
	Nodes []CheckRecentNode `json:"nodes"`
}

type CheckRecentNode struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	NodeType  string `json:"node_type"`
	UpdatedAt string `json:"updated_at"`
}

type FindPathInput struct {
	FromID    string   `json:"from_id"`
	ToID      string   `json:"to_id"`
	EdgeTypes []string `json:"edge_types,omitempty"` // nil = all types
	MaxDepth  int      `json:"max_depth,omitempty"`  // default 10
}

type FindPathOutput struct {
	Path []PathHop `json:"path,omitempty"`
	Hops int       `json:"hops"`
}

type PathHop struct {
	NodeID    string `json:"node_id"`
	Title     string `json:"title"`
	EdgeType  string `json:"edge_type,omitempty"`
	Direction string `json:"direction,omitempty"`
}

type QueryGraphInput struct {
	NodeID    string   `json:"node_id"`
	EdgeTypes []string `json:"edge_types,omitempty"` // nil = all types
	Direction string   `json:"direction,omitempty"`  // "outgoing" | "incoming" | "both"; default "both"
	Limit     int      `json:"limit,omitempty"`      // default 20
}

type QueryGraphOutput struct {
	NodeID         string         `json:"node_id"`
	Title          string         `json:"title"`
	ContentExcerpt string         `json:"content_excerpt,omitempty"`
	NeighborCounts map[string]int `json:"neighbor_counts,omitempty"`
	Edges          []EdgeResult   `json:"edges"`
}

type EdgeResult struct {
	Direction  string  `json:"direction"` // "outgoing" | "incoming"
	NodeID     string  `json:"node_id"`   // the other node (to_id or from_id)
	NodeTitle  string  `json:"node_title"`
	NodeType   string  `json:"node_type"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

type ProjectGraphInput struct {
	ProjectID       string   `json:"project_id,omitempty"`
	MaxEdges        int      `json:"max_edges,omitempty"`  // default 5000
	EdgeTypes       []string `json:"edge_types,omitempty"` // empty = all non-proposed types
	ConfidenceTiers []string `json:"confidence_tiers,omitempty"`
}

type ProjectGraphOutput struct {
	ProjectID  string      `json:"project_id"`
	TotalEdges int         `json:"total_edges"`
	Returned   int         `json:"returned"`
	Nodes      []GraphNode `json:"nodes"`
	Edges      []GraphEdge `json:"edges"`
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

type ClearProjectInput struct {
	ProjectID string `json:"project_id"`
	ModelID   string `json:"model_id,omitempty"` // model to clear (default = current server model)
	Confirm   bool   `json:"confirm"`
	Purge     bool   `json:"purge"` // clear all models for this project
}

type ClearProjectOutput struct {
	Status        string `json:"status"`
	DeletedNodes  int    `json:"deleted_nodes"`
	DeletedEdges  int    `json:"deleted_edges"`
	DeletedChunks int    `json:"deleted_chunks"`
	DeletedVecs   int    `json:"deleted_vectors"`
}

type ListProjectsOutput struct {
	Projects []store.ProjectInfo `json:"projects"`
}

type JobStatusInput struct {
	JobID string `json:"id"`
}

type JobStatusOutput struct {
	ID              string `json:"id"`
	Status          string `json:"status"`          // "running" | "done" | "error" | "aborted"
	Phase           string `json:"phase,omitempty"` // "walking" | "syncing" | "waiting" | "chunking" | "embedding" | "persisting" | "linking" | "" (done/error)
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

type AbortInput struct {
	ProjectID string `json:"project_id,omitempty"` // empty = cancel all running jobs
}

type AbortOutput struct {
	Cancelled int    `json:"cancelled"`
	ProjectID string `json:"project_id,omitempty"`
}
