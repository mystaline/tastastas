// Package store defines the storage interface tastastas is built against.
// The default implementation is SQLite (internal/store/sqlite); this
// interface exists so a Postgres/pgvector or Kuzu backend can be swapped in
// later without touching ingestion, extraction, dedupe, or retrieval logic.
package store

import (
	"context"
	"errors"
	"math"
	"strings"
)

// ErrNotFound is returned by GetNode when the id doesn't exist.
var ErrNotFound = errors.New("node not found")

// ErrVectorSkipped wraps a non-fatal condition: node/chunk metadata was
// persisted successfully, but its embedding vector was invalid (NaN/Inf,
// wrong dimension, or unmarshalable) and was skipped rather than written.
// A log line alone is not enough here — an MCP caller only ever sees the
// tool's JSON response, never the server's stdout, so callers MUST check
// errors.Is(err, ErrVectorSkipped) and surface it as a response warning
// instead of silently treating a skip as full success.
var ErrVectorSkipped = errors.New("vector skipped: invalid embedding")

// HasNonFiniteFloat32 reports whether emb contains any NaN or Inf value.
// Both persist (Upsert*) and read (GetNodeEmbeddings, blob decode) paths
// must guard against this — a corrupt vector that reaches vec_f32() or a
// cosine similarity computation fails loudly or silently ranks garbage,
// depending on where it's caught.
func HasNonFiniteFloat32(emb []float32) bool {
	for _, v := range emb {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return true
		}
	}
	return false
}

// DisplayName derives a human-readable label from a node ID when the node
// itself wasn't ingested (stdlib symbols, cross-project refs — GetNode misses).
// IDs look like "proj/code:function/pkg/path.Qualified.Name"; the last
// slash-segment ("path.Qualified.Name") is the useful part. Falls back to the
// whole id if there's no slash.
func DisplayName(id string) string {
	if id == "" {
		return ""
	}
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		return id[i+1:]
	}
	return id
}

// Node is a typed unit of memory: a doc, repo, service, extracted fact, etc.
type Node struct {
	ID            string // qualified id, source-adapter-derived, globally unique
	ProjectID     string
	NodeType      string
	Title         string
	Content       string
	ContentHash   string
	Status        string // current, stale, superseded
	SourceAdapter string
	SourcePath    string
	Importance    float64
	Embedding     []float32 // optional; nil if not embedded yet
	ModelID       string    `json:"-"` // set during embedding for composite vec0 PK
	Language      string    // programming language for code files (go, python, etc.)
	HasChunks     bool      // true if this node has been chunked+embedded
	CreatedAt     string
	UpdatedAt     string
}

// Edge is a typed, directed relation between two nodes.
type Edge struct {
	FromID         string
	ToID           string
	EdgeType       string
	Confidence     float64
	ConfidenceTier string // EXTRACTED, INFERRED, or PROPOSED
	Bidirectional  bool   // if true, edge is traversable in both directions
}

// EdgeResult bundles an edge with resolved metadata for both endpoints.
// Used by project_graph for visualization payloads.
type EdgeResult struct {
	FromID         string  `json:"from_id"`
	FromTitle      string  `json:"from_title"`
	FromType       string  `json:"from_type"`
	FromGroup      string  `json:"from_group"`
	FromSize       int     `json:"from_size"`
	FromProjectID  string  `json:"from_project_id"`
	ToID           string  `json:"to_id"`
	ToTitle        string  `json:"to_title"`
	ToType         string  `json:"to_type"`
	ToGroup        string  `json:"to_group"`
	ToSize         int     `json:"to_size"`
	ToProjectID    string  `json:"to_project_id"`
	EdgeType       string  `json:"edge_type"`
	Confidence     float64 `json:"confidence"`
	ConfidenceTier string  `json:"confidence_tier,omitempty"`
}

// ScoredNode is a Node with a retrieval score attached.
type ScoredNode struct {
	Node
	Score float64
}

// Chunk represents a chunk of content from a parent node.
type Chunk struct {
	ID            string
	ParentNodeID  string
	ChunkIndex    int
	Type          string
	HeadingPath   []string
	Content       string
	Language      string
	SourceAdapter string // populated from parent node during chunking
	PrevChunkID   string // empty for first chunk
	NextChunkID   string // empty for last chunk
	Embedding     []float32
	ModelID       string `json:"-"` // set during embedding for composite vec0 PK
}

// ScoredChunk is a Chunk with a retrieval score attached.
type ScoredChunk struct {
	Chunk
	Score float64
}

// AccessPair represents two nodes accessed together in the same session.
type AccessPair struct {
	NodeA string
	NodeB string
	Count int
}

// Store is the full persistence surface tastastas depends on.
type Store interface {
	UpsertNode(ctx context.Context, n Node) error
	UpsertEdge(ctx context.Context, e Edge) error
	UpsertEdges(ctx context.Context, edges []Edge) error
	DeleteNode(ctx context.Context, id string) error
	GetNode(ctx context.Context, id string) (Node, error)

	// SearchLexical runs an FTS5 query scoped to a project.
	// Returns scored nodes where Score is the FTS5 BM25 rank (lower = better match).
	SearchLexical(ctx context.Context, projectID, query string, limit int) ([]ScoredNode, error)

	// SearchVector runs a nearest-neighbor query scoped to a project and model.
	SearchVector(
		ctx context.Context,
		projectID string,
		embedding []float32,
		limit int,
		modelID string,
	) ([]ScoredNode, error)

	// Chunk operations for unified embeddings
	UpsertChunks(ctx context.Context, chunks []Chunk) error
	DeleteChunksByParent(ctx context.Context, parentNodeID string, modelID string) error
	SearchChunks(
		ctx context.Context,
		projectID string,
		embedding []float32,
		limit int,
		modelID string,
	) ([]ScoredChunk, error)

	// GetChunksByParent returns chunks for a parent node, ordered by chunk_index.
	GetChunksByParent(ctx context.Context, parentNodeID string, limit, offset int) ([]Chunk, error)

	// CountChunksByParent returns total chunk count for a parent node.
	CountChunksByParent(ctx context.Context, parentNodeID string) (int, error)

	// Neighbors walks typed edges out to depth hops from id.
	Neighbors(ctx context.Context, id string, edgeTypes []string, depth int) ([]Node, []Edge, error)

	// NeighborsWithConfidence is like Neighbors but also returns, per node
	// ID, the confidence of the edge that first discovered it during the
	// walk — for callers that need to know how strongly a neighbor is
	// actually connected (e.g. retrieval's edge-confidence boost), rather
	// than guessing via unrelated index alignment between two slices.
	NeighborsWithConfidence(
		ctx context.Context,
		id string,
		edgeTypes []string,
		depth int,
	) ([]Node, map[string]float64, error)

	// MarkStaleDownstream flags nodes reachable from changedID (via impact
	// edge types) as status=stale, up to maxDepth hops, and returns them.
	MarkStaleDownstream(ctx context.Context, changedID string, maxDepth int) ([]Node, error)

	// Stats returns aggregate counts for a project.
	Stats(ctx context.Context, projectID string, modelID string) (StoreStats, error)

	// EdgeTypeCounts returns edge count per edge type for a project.
	EdgeTypeCounts(ctx context.Context, projectID string) (map[string]int, error)

	// ListNodesByType returns nodes matching any of the given types, ordered by created_at desc.
	ListNodesByType(ctx context.Context, projectID string, types []string, limit, offset int) ([]Node, error)

	// ListEdgesByType returns edges matching the given edge type, scoped to a project.
	ListEdgesByType(ctx context.Context, projectID string, edgeType string, limit, offset int) ([]Edge, error)

	// LogAccess records that a node was accessed in a session.
	LogAccess(ctx context.Context, projectID, nodeID, sessionID string) error

	// RecentAccesses returns node IDs accessed within the past N seconds, ordered by most recent.
	RecentAccesses(ctx context.Context, projectID string, withinSec int) ([]string, error)

	// AccessHistory returns access log entries for a node, most recent first.
	AccessHistory(ctx context.Context, nodeID string, limit int) ([]Node, error)

	// ListNodesByUpdatedAfter returns nodes updated after a given timestamp.
	ListNodesByUpdatedAfter(ctx context.Context, projectID, after string, limit int) ([]Node, error)

	// GetAccessStats returns access_count and last_accessed_at for a node.
	GetAccessStats(ctx context.Context, nodeID string) (count int, lastAccessedAt string, err error)

	// GetSessionPairs returns node pairs accessed together in the same session,
	// counted per-session, for sessions within the past withinSec seconds.
	// Only returns pairs seen in >= minSessions distinct sessions.
	GetSessionPairs(ctx context.Context, withinSec int, minSessions int) ([]AccessPair, error)

	// ListEdgesByProject returns all edges for a project with resolved node
	// metadata (title, type, group) for both endpoints. Paginated.
	// edgeTypes filter: nil/empty = all types. Returns (edges, totalCount).
	ListEdgesByProject(
		ctx context.Context,
		projectID string,
		edgeTypes []string,
		limit, offset int,
	) ([]EdgeResult, int, error)

	// GetEdgesFrom returns outgoing edges from nodeID, optionally filtered by edgeTypes.
	GetEdgesFrom(ctx context.Context, nodeID string, edgeTypes []string) ([]Edge, error)

	// GetEdgesTo returns incoming edges to nodeID, optionally filtered by edgeTypes.
	GetEdgesTo(ctx context.Context, nodeID string, edgeTypes []string) ([]Edge, error)

	// DeleteEdge removes a single edge by its composite key.
	DeleteEdge(ctx context.Context, fromID, toID, edgeType string) error

	// ResolveUnresolved checks if a newly-upserted node's title matches any
	// pending unresolved_references mentions for the same project. For each
	// match, creates the stored relation edge and deletes the reference.
	// Returns number of references resolved.
	ResolveUnresolved(ctx context.Context, projectID, nodeID, nodeTitle string) (int, error)

	// SetJobMarker writes a marker indicating an async job is in progress.
	// Used for interrupted-run detection on next startup.
	SetJobMarker(ctx context.Context) error

	// ClearJobMarker removes the in-progress marker.
	ClearJobMarker(ctx context.Context) error

	// HasJobMarker returns true if a previous run was interrupted.
	HasJobMarker(ctx context.Context) (bool, error)

	// GetEmbedModelID returns the stored model identity for a project.
	// Returns empty string if no record exists.
	GetEmbedModelID(ctx context.Context, projectID string) (string, error)

	// GetEmbedModelStatus returns the dirty/clean status for a project+model.
	// Returns empty string if no record exists.
	GetEmbedModelStatus(ctx context.Context, projectID, modelID string) (string, error)

	// InitEmbedConfig creates the config row as 'clean' if not exists.
	// Used by remember/extract_and_remember — single-input tools.
	InitEmbedConfig(ctx context.Context, projectID, modelID string) error

	// SetEmbedModelDirty creates/updates the config row as 'dirty'.
	// Used by onboard/ingest at goroutine start (guardIngest).
	SetEmbedModelDirty(ctx context.Context, projectID, modelID string) error

	// SetEmbedModelClean updates the config row status to 'clean' for a model.
	SetEmbedModelClean(ctx context.Context, projectID, modelID string) error

	// ListEmbedModels returns all models with their status for a project.
	ClearProject(ctx context.Context, projectID, modelID string) (ClearProjectResult, error)
	ListProjects(ctx context.Context) ([]ProjectInfo, error)

	// UpsertProject records the human-readable name and remote URL for a base
	// (unstaged) project ID. Idempotent.
	UpsertProject(ctx context.Context, projectID, projectName, repositoryURL string) error

	ListEmbedModels(ctx context.Context, projectID string) ([]ModelInfo, error)

	// ListProjectIDs returns all known project IDs.
	ListProjectIDs(ctx context.Context) ([]string, error)

	// GetTopNodesByImportance returns the top-N nodes for a project ordered by importance DESC.
	GetTopNodesByImportance(ctx context.Context, projectID string, limit int) ([]Node, error)

	// GetNodesByCrossProjectEdges returns nodes of projectID that are endpoints
	// of edges touching otherProject, ordered by importance DESC.
	GetNodesByCrossProjectEdges(ctx context.Context, projectID, otherProject string, limit int) ([]Node, error)

	// GetNodeEmbeddings returns embeddings for the given node IDs.
	GetNodeEmbeddings(ctx context.Context, nodeIDs []string) (map[string][]float32, error)

	Close() error
}

// EdgeProposal is a Tier-3 candidate link awaiting disambiguation.
type EdgeProposal struct {
	ID         string
	FromID     string
	ToID       string
	EdgeType   string
	Confidence float64
	Reason     string
	Status     string // pending | accepted | rejected
}

// ModelInfo holds per-model status for a project.
type ModelInfo struct {
	ModelID string `json:"model_id"`
	Status  string `json:"status"` // "dirty" | "clean"
}

// StoreStats holds aggregate project-level counts for inspection tools.
type StoreStats struct {
	NodeCount     int         `json:"node_count"`
	EdgeCount     int         `json:"edge_count"`
	ChunkCount    int         `json:"chunk_count"`
	VecCount      int         `json:"vec_count"`
	StaleCount    int         `json:"stale_count"`
	ConventionCnt int         `json:"convention_count"`
	EmbedModelID  string      `json:"embed_model_id,omitempty"`
	Models        []ModelInfo `json:"models,omitempty"`
}

type ClearProjectResult struct {
	Nodes   int
	Edges   int
	Chunks  int
	Vectors int
}

type ProjectInfo struct {
	ProjectID          string `json:"project_id"`
	ProjectName        string `json:"project_name,omitempty"`
	RepositoryURL      string `json:"repository_url,omitempty"`
	Stage              string `json:"stage,omitempty"`
	EffectiveProjectID string `json:"effective_project_id,omitempty"`
	NodeCount          int    `json:"node_count"`
	EdgeCount          int    `json:"edge_count"`
}
