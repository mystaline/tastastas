// Package store defines the storage interface tastastas is built against.
// The default implementation is SQLite (internal/store/sqlite); this
// interface exists so a Postgres/pgvector or Kuzu backend can be swapped in
// later without touching ingestion, extraction, dedupe, or retrieval logic.
package store

import (
	"context"
	"errors"
)

// ErrNotFound is returned by GetNode when the id doesn't exist.
var ErrNotFound = errors.New("node not found")

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
	CreatedAt     string
	UpdatedAt     string
}

// Edge is a typed, directed relation between two nodes.
type Edge struct {
	FromID     string
	ToID       string
	EdgeType   string
	Confidence float64
}

// ScoredNode is a Node with a retrieval score attached.
type ScoredNode struct {
	Node
	Score float64
}

// Store is the full persistence surface tastastas depends on.
type Store interface {
	UpsertNode(ctx context.Context, n Node) error
	UpsertEdge(ctx context.Context, e Edge) error
	DeleteNode(ctx context.Context, id string) error
	GetNode(ctx context.Context, id string) (Node, error)

	// SearchLexical runs an FTS5 (or equivalent) query scoped to a project.
	SearchLexical(ctx context.Context, projectID, query string, limit int) ([]Node, error)

	// SearchVector runs a nearest-neighbor query scoped to a project.
	SearchVector(ctx context.Context, projectID string, embedding []float32, limit int) ([]ScoredNode, error)

	// Neighbors walks typed edges out to depth hops from id.
	Neighbors(ctx context.Context, id string, edgeTypes []string, depth int) ([]Node, []Edge, error)

	// MarkStaleDownstream flags nodes reachable from changedID (via impact
	// edge types) as status=stale, up to maxDepth hops, and returns them.
	MarkStaleDownstream(ctx context.Context, changedID string, maxDepth int) ([]Node, error)

	Close() error
}
