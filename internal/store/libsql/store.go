// Package libsql implements the tastastas Store interface using libSQL (Turso).
// Supports both local embedded (tursogo) and remote (libsql-client-go) modes.
//
// DSN formats:
//   - Local:  "file:memory.db" (uses tursogo driver)
//   - Remote: "libsql://db.turso.io?authToken=..." (uses libsql-client-go driver)
//
// The Store interface is defined in internal/store/store.go
package libsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/mystaline-dev/tastastas/internal/store"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "turso.tech/database/tursogo"
)

type Store struct {
	db  *sql.DB
	dim int
}

func Open(ctx context.Context, dsn string, dim int) (*Store, error) {
	var driver string
	if strings.HasPrefix(dsn, "libsql://") || strings.HasPrefix(dsn, "http://") || strings.HasPrefix(dsn, "https://") {
		driver = "libsql"
	} else {
		driver = "turso"
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("libsql: open %s: %w", dsn, err)
	}

	s := &Store{db: db, dim: dim}
	if err := s.initSchema(ctx); err != nil {
		return nil, fmt.Errorf("libsql: init schema: %w", err)
	}
	return s, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	// Nodes table
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS nodes (
			id            TEXT PRIMARY KEY,
			project_id    TEXT NOT NULL DEFAULT 'default',
			node_type     TEXT NOT NULL,
			title         TEXT NOT NULL DEFAULT '',
			content       TEXT NOT NULL DEFAULT '',
			content_hash  TEXT NOT NULL DEFAULT '',
			status        TEXT NOT NULL DEFAULT 'current',
			source_adapter TEXT NOT NULL DEFAULT '',
			source_path   TEXT NOT NULL DEFAULT '',
			importance    REAL NOT NULL DEFAULT 0.5,
			language      TEXT NOT NULL DEFAULT '',
			created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)
	`)
	if err != nil {
		return err
	}

	// Edges table
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS edges (
			from_id    TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			to_id      TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			edge_type  TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 1.0,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			PRIMARY KEY (from_id, to_id, edge_type)
		)
	`)
	if err != nil {
		return err
	}

	// Indexes
	_, err = s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_nodes_project_type ON nodes(project_id, node_type);
		CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
		CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
		CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);
	`)
	if err != nil {
		return err
	}

	// vec0 virtual tables for vector search (dimension-specific)
	// These may fail if extensions aren't available (e.g., tursogo local mode)
	nodeVecStmt := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS node_vectors USING vec0(node_id TEXT PRIMARY KEY, embedding float[%d])`,
		s.dim,
	)
	_, err = s.db.ExecContext(ctx, nodeVecStmt)
	if err != nil {
		log.Printf("libsql: vec0 node_vectors not available (extension missing): %v", err)
	}

	chunkVecStmt := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunk_vectors USING vec0(chunk_id TEXT PRIMARY KEY, embedding float[%d])`,
		s.dim,
	)
	_, err = s.db.ExecContext(ctx, chunkVecStmt)
	if err != nil {
		log.Printf("libsql: vec0 chunk_vectors not available (extension missing): %v", err)
	}

	// Chunks table
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS chunks (
			id            TEXT PRIMARY KEY,
			parent_node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
			chunk_index   INTEGER NOT NULL,
			chunk_type    TEXT NOT NULL,
			heading_path  TEXT NOT NULL DEFAULT '[]',
			content       TEXT NOT NULL,
			language      TEXT NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return err
	}

	// Unresolved references table (Tier 1 linking)
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS unresolved_references (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id      TEXT NOT NULL,
			mention         TEXT NOT NULL,
			source_node_id  TEXT NOT NULL,
			relation_type   TEXT NOT NULL,
			created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)
	`)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(
		ctx,
		`CREATE INDEX IF NOT EXISTS idx_unresolved_mention ON unresolved_references(project_id, mention)`,
	)
	if err != nil {
		return err
	}

	// Edge proposals table (Tier 3)
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS edge_proposals (
			id          TEXT PRIMARY KEY,
			from_id     TEXT NOT NULL,
			to_id       TEXT NOT NULL,
			edge_type   TEXT NOT NULL,
			confidence  REAL NOT NULL,
			reason      TEXT,
			status      TEXT NOT NULL DEFAULT 'pending',
			created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		)
	`)
	if err != nil {
		return err
	}

	// Ingest jobs table
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS ingest_jobs (
			id            TEXT PRIMARY KEY,
			adapter       TEXT NOT NULL,
			root          TEXT NOT NULL,
			project_id    TEXT NOT NULL,
			status        TEXT NOT NULL,
			nodes_ingested INTEGER DEFAULT 0,
			edges_created  INTEGER DEFAULT 0,
			chunks_created INTEGER DEFAULT 0,
			started_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			finished_at   TEXT,
			error         TEXT
		)
	`)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

var _ store.Store = (*Store)(nil)

// UpsertNode inserts or updates a node.
func (s *Store) UpsertNode(ctx context.Context, n store.Node) error {
	if n.ID == "" {
		return errors.New("libsql: node id must not be empty")
	}
	if n.ProjectID == "" {
		n.ProjectID = "default"
	}
	if n.Status == "" {
		n.Status = "current"
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes (id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(id) DO UPDATE SET
			project_id     = excluded.project_id,
			node_type      = excluded.node_type,
			title          = excluded.title,
			content        = excluded.content,
			content_hash   = excluded.content_hash,
			status         = excluded.status,
			source_adapter = excluded.source_adapter,
			source_path    = excluded.source_path,
			importance     = excluded.importance,
			language       = excluded.language,
			updated_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, n.ID, n.ProjectID, n.NodeType, n.Title, n.Content, n.ContentHash, n.Status, n.SourceAdapter, n.SourcePath, n.Importance, n.Language)
	if err != nil {
		return fmt.Errorf("libsql: upsert node %s: %w", n.ID, err)
	}

	if len(n.Embedding) > 0 {
		if len(n.Embedding) != s.dim {
			return fmt.Errorf(
				"libsql: node %s embedding has dim %d, store configured for dim %d",
				n.ID,
				len(n.Embedding),
				s.dim,
			)
		}
		vecJSON, err := json.Marshal(n.Embedding)
		if err != nil {
			return fmt.Errorf("libsql: marshal embedding for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id = ?`, n.ID)
		if err != nil {
			return fmt.Errorf("libsql: delete stale vector for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(
			ctx,
			`INSERT INTO node_vectors (node_id, embedding) VALUES (?, vec_f32(?))`,
			n.ID,
			string(vecJSON),
		)
		if err != nil {
			return fmt.Errorf("libsql: insert vector for %s: %w", n.ID, err)
		}
	}

	if n.Title != "" {
		_, _ = s.ResolveUnresolved(ctx, n.ProjectID, n.ID, n.Title)
	}
	return nil
}

// UpsertEdge inserts or updates an edge.
func (s *Store) UpsertEdge(ctx context.Context, e store.Edge) error {
	if e.FromID == "" || e.ToID == "" || e.EdgeType == "" {
		return errors.New("libsql: edge requires from_id, to_id, edge_type")
	}
	if e.Confidence == 0 {
		e.Confidence = 1.0
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO edges (from_id, to_id, edge_type, confidence)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(from_id, to_id, edge_type) DO UPDATE SET confidence = excluded.confidence
	`, e.FromID, e.ToID, e.EdgeType, e.Confidence)
	if err != nil {
		return fmt.Errorf("libsql: upsert edge %s->%s(%s): %w", e.FromID, e.ToID, e.EdgeType, err)
	}
	return nil
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("libsql: delete node %s: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("libsql: delete node %s rows affected: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("libsql: node %s: %w", id, store.ErrNotFound)
	}
	return nil
}

func (s *Store) GetNode(ctx context.Context, id string) (store.Node, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, created_at, updated_at,
		       EXISTS(SELECT 1 FROM chunks WHERE parent_node_id = nodes.id)
		FROM nodes WHERE id = ?
	`, id)
	var n store.Node
	err := row.Scan(
		&n.ID, &n.ProjectID, &n.NodeType, &n.Title, &n.Content, &n.ContentHash, &n.Status,
		&n.SourceAdapter, &n.SourcePath, &n.Importance, &n.Language, &n.CreatedAt, &n.UpdatedAt,
		&n.HasChunks,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Node{}, fmt.Errorf("libsql: node %s: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("libsql: get node %s: %w", id, err)
	}
	return n, nil
}

const nodeCols = "n.id, n.project_id, n.node_type, n.title, n.content, n.content_hash, n.status, n.source_adapter, n.source_path, n.importance, n.language, n.created_at, n.updated_at"

func (s *Store) SearchLexical(ctx context.Context, projectID, query string, limit int) ([]store.ScoredNode, error) {
	// Check if FTS5 is available first
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM pragma_table_list WHERE name = 'nodes_fts' AND type = 'table'`).
		Scan(&exists)
	if err != nil || exists == 0 {
		return nil, fmt.Errorf("libsql: FTS5 not available (nodes_fts table missing)")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeCols+`
		FROM nodes_fts f
		JOIN nodes n ON n.id = f.id
		WHERE nodes_fts MATCH ? AND n.project_id = ?
		LIMIT ?
	`, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("libsql: search lexical: %w", err)
	}

	defer rows.Close()

	var out []store.ScoredNode
	for rows.Next() {
		var sn store.ScoredNode
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &sn.NodeType, &sn.Title, &sn.Content, &sn.ContentHash, &sn.Status, &sn.SourceAdapter, &sn.SourcePath, &sn.Importance, &sn.Language, &sn.CreatedAt, &sn.UpdatedAt, &sn.Score); err != nil {
			return nil, fmt.Errorf("libsql: scan scored node: %w", err)
		}
		out = append(out, sn)
	}

	return out, rows.Err()
}

func (s *Store) SearchVector(
	ctx context.Context,
	projectID string,
	embedding []float32,
	limit int,
) ([]store.ScoredNode, error) {
	if len(embedding) != s.dim {
		return nil, fmt.Errorf("libsql: query embedding has dim %d, store configured for dim %d", len(embedding), s.dim)
	}
	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil, fmt.Errorf("libsql: marshal query embedding: %w", err)
	}
	// Check if vec0 is available
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM pragma_table_list WHERE name = 'node_vectors' AND type = 'table'`).
		Scan(&exists)
	if err != nil || exists == 0 {
		return nil, fmt.Errorf("libsql: vec0 not available (node_vectors table missing)")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeCols+`, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
		FROM node_vectors v
		JOIN nodes n ON n.id = v.node_id
		WHERE n.project_id = ?
		ORDER BY distance
		LIMIT ?
	`, string(vecJSON), projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("libsql: search vector: %w", err)
	}

	defer rows.Close()

	var out []store.ScoredNode
	for rows.Next() {
		var sn store.ScoredNode
		var distance float64
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &sn.NodeType, &sn.Title, &sn.Content, &sn.ContentHash, &sn.Status, &sn.SourceAdapter, &sn.SourcePath, &sn.Importance, &sn.Language, &sn.CreatedAt, &sn.UpdatedAt, &distance); err != nil {
			return nil, fmt.Errorf("libsql: scan scored node: %w", err)
		}
		sn.Score = 1 - distance
		out = append(out, sn)
	}

	return out, rows.Err()
}

// UpsertChunks deletes existing chunks for parents and inserts new ones.
func (s *Store) UpsertChunks(ctx context.Context, chunks []store.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("libsql: begin chunk upsert tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	parentIDs := make(map[string]bool)
	for _, c := range chunks {
		parentIDs[c.ParentNodeID] = true
	}
	if len(parentIDs) > 0 {
		placeholders := make([]string, 0, len(parentIDs))
		args := make([]any, 0, len(parentIDs))
		for pid := range parentIDs {
			placeholders = append(placeholders, "?")
			args = append(args, pid)
		}
		inClause := strings.Join(placeholders, ",")

		// Delete vectors first (FK to chunks.id)
		_, err = tx.ExecContext(
			ctx,
			fmt.Sprintf(
				`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id IN (%s))`,
				inClause,
			),
			args...,
		)
		if err != nil {
			return fmt.Errorf("libsql: delete old chunk vectors: %w", err)
		}

		// Delete chunks
		_, err = tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM chunks WHERE parent_node_id IN (%s)`, inClause),
			args...,
		)
		if err != nil {
			return fmt.Errorf("libsql: delete old chunks: %w", err)
		}
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO chunks (id, parent_node_id, chunk_index, chunk_type, heading_path, content, language)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("libsql: prepare chunk insert: %w", err)
	}

	defer stmt.Close()

	vecStmt, err := tx.PrepareContext(ctx, `INSERT INTO chunk_vectors (chunk_id, embedding) VALUES (?, vec_f32(?))`)
	if err != nil {
		return fmt.Errorf("libsql: prepare chunk vector insert: %w", err)
	}

	defer vecStmt.Close()

	for _, c := range chunks {
		headingJSON, _ := json.Marshal(c.HeadingPath)
		_, err := stmt.ExecContext(
			ctx,
			c.ID,
			c.ParentNodeID,
			c.ChunkIndex,
			c.Type,
			string(headingJSON),
			c.Content,
			c.Language,
		)
		if err != nil {
			return fmt.Errorf("libsql: insert chunk %s: %w", c.ID, err)
		}

		if len(c.Embedding) > 0 {
			if len(c.Embedding) != s.dim {
				return fmt.Errorf(
					"libsql: chunk %s embedding has dim %d, store configured for dim %d",
					c.ID,
					len(c.Embedding),
					s.dim,
				)
			}

			vecJSON, _ := json.Marshal(c.Embedding)
			_, err := vecStmt.ExecContext(ctx, c.ID, string(vecJSON))
			if err != nil {
				return fmt.Errorf("libsql: insert chunk vector %s: %w", c.ID, err)
			}
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteChunksByParent(ctx context.Context, parentNodeID string) error {
	_, err := s.db.ExecContext(
		ctx,
		`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id = ?)`,
		parentNodeID,
	)

	if err != nil {
		return fmt.Errorf("libsql: delete chunk vectors for %s: %w", parentNodeID, err)
	}

	_, err = s.db.ExecContext(ctx, `DELETE FROM chunks WHERE parent_node_id = ?`, parentNodeID)
	if err != nil {
		return fmt.Errorf("libsql: delete chunks for %s: %w", parentNodeID, err)
	}

	return nil
}

func (s *Store) SearchChunks(
	ctx context.Context,
	projectID string,
	embedding []float32,
	limit int,
) ([]store.ScoredChunk, error) {
	if len(embedding) != s.dim {
		return nil, fmt.Errorf("libsql: query embedding has dim %d, store configured for dim %d", len(embedding), s.dim)
	}

	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil, fmt.Errorf("libsql: marshal query embedding: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.parent_node_id, c.chunk_index, c.chunk_type, c.heading_path, c.content, c.language,
		       vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
		FROM chunk_vectors v
		JOIN chunks c ON c.id = v.chunk_id
		JOIN nodes n ON n.id = c.parent_node_id
		WHERE n.project_id = ?
		ORDER BY distance
		LIMIT ?
	`, string(vecJSON), projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("libsql: search chunks: %w", err)
	}

	defer rows.Close()

	var out []store.ScoredChunk
	for rows.Next() {
		var sc store.ScoredChunk
		var headingPath string
		var distance float64
		if err := rows.Scan(&sc.ID, &sc.ParentNodeID, &sc.ChunkIndex, &sc.Type, &headingPath, &sc.Content, &sc.Language, &distance); err != nil {
			return nil, fmt.Errorf("libsql: scan scored chunk: %w", err)
		}
		_ = json.Unmarshal([]byte(headingPath), &sc.HeadingPath)
		sc.Score = 1 - distance
		out = append(out, sc)
	}

	return out, rows.Err()
}

func (s *Store) Neighbors(
	ctx context.Context,
	id string,
	edgeTypes []string,
	depth int,
) ([]store.Node, []store.Edge, error) {
	nodes, edges, _, err := s.neighborsBFS(ctx, id, edgeTypes, depth)
	return nodes, edges, err
}

func (s *Store) NeighborsWithConfidence(
	ctx context.Context,
	id string,
	edgeTypes []string,
	depth int,
) ([]store.Node, map[string]float64, error) {
	nodes, _, confidence, err := s.neighborsBFS(ctx, id, edgeTypes, depth)
	return nodes, confidence, err
}

func (s *Store) neighborsBFS(
	ctx context.Context,
	id string,
	edgeTypes []string,
	depth int,
) ([]store.Node, []store.Edge, map[string]float64, error) {
	if depth < 1 {
		depth = 1
	}
	visited := map[string]bool{id: true}
	reachedBy := map[string]float64{}
	frontier := []string{id}
	var allEdges []store.Edge

	typeFilter, args := buildEdgeTypeFilter(edgeTypes)

	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, fromID := range frontier {
			q := `SELECT from_id, to_id, edge_type, confidence FROM edges WHERE from_id = ?` + typeFilter
			queryArgs := append([]any{fromID}, args...)
			rows, err := s.db.QueryContext(ctx, q, queryArgs...)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("libsql: neighbors query: %w", err)
			}
			for rows.Next() {
				var e store.Edge
				if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence); err != nil {
					rows.Close()
					return nil, nil, nil, fmt.Errorf("libsql: scan edge: %w", err)
				}
				allEdges = append(allEdges, e)
				if !visited[e.ToID] {
					visited[e.ToID] = true
					reachedBy[e.ToID] = e.Confidence
					next = append(next, e.ToID)
				}
			}
			rows.Close()
		}
		frontier = next
	}

	delete(visited, id)
	var nodes []store.Node
	for nid := range visited {
		n, err := s.GetNode(ctx, nid)
		if err != nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, allEdges, reachedBy, nil
}

func (s *Store) MarkStaleDownstream(ctx context.Context, changedID string, maxDepth int) ([]store.Node, error) {
	impactTypes := []string{"implements", "tests", "specifies", "depends-on"}

	nodes, _, err := s.NeighborsWithConfidence(ctx, changedID, impactTypes, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("libsql: mark stale downstream: %w", err)
	}

	for i := range nodes {
		_, err := s.db.ExecContext(
			ctx,
			`UPDATE nodes SET status = 'stale', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
			nodes[i].ID,
		)
		if err != nil {
			return nil, fmt.Errorf("libsql: mark stale %s: %w", nodes[i].ID, err)
		}
		nodes[i].Status = "stale"
	}

	return nodes, nil
}

func (s *Store) Stats(ctx context.Context, projectID string) (store.StoreStats, error) {
	var st store.StoreStats
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = ?`, projectID)
	if err := row.Scan(&st.NodeCount); err != nil {
		return st, fmt.Errorf("libsql: stats nodes: %w", err)
	}

	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM edges e JOIN nodes n ON n.id = e.from_id WHERE n.project_id = ?`,
		projectID,
	)
	if err := row.Scan(&st.EdgeCount); err != nil {
		return st, fmt.Errorf("libsql: stats edges: %w", err)
	}
	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?`,
		projectID,
	)
	if err := row.Scan(&st.ChunkCount); err != nil {
		return st, fmt.Errorf("libsql: stats chunks: %w", err)
	}

	row = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = ? AND status = 'stale'`, projectID)
	if err := row.Scan(&st.StaleCount); err != nil {
		return st, fmt.Errorf("libsql: stats stale: %w", err)
	}

	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM nodes WHERE project_id = ? AND node_type = 'code:convention'`,
		projectID,
	)
	if err := row.Scan(&st.ConventionCnt); err != nil {
		return st, fmt.Errorf("libsql: stats conventions: %w", err)
	}

	return st, nil
}

func buildEdgeTypeFilter(edgeTypes []string) (string, []any) {
	if len(edgeTypes) == 0 {
		return "", nil
	}
	clause := " AND edge_type IN ("
	args := make([]any, len(edgeTypes))
	for i, t := range edgeTypes {
		if i > 0 {
			clause += ","
		}
		clause += "?"
		args[i] = t
	}
	clause += ")"
	return clause, args
}

func (s *Store) ListNodesByType(
	ctx context.Context,
	projectID string,
	types []string,
	limit, offset int,
) ([]store.Node, error) {
	if len(types) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(types))
	args := make([]any, 0, len(types)+2)
	args = append(args, projectID)

	for i, t := range types {
		placeholders[i] = "?"
		args = append(args, t)
	}

	args = append(args, limit, offset)
	q := fmt.Sprintf(
		`SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, created_at, updated_at
		FROM nodes WHERE project_id = ? AND node_type IN (%s) ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var out []store.Node
	for rows.Next() {
		var n store.Node
		if err := rows.Scan(&n.ID, &n.ProjectID, &n.NodeType, &n.Title, &n.Content, &n.ContentHash,
			&n.Status, &n.SourceAdapter, &n.SourcePath, &n.Importance, &n.Language, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}

	return out, rows.Err()
}

func (s *Store) ListEdgesByType(
	ctx context.Context,
	projectID string,
	edgeType string,
	limit, offset int,
) ([]store.Edge, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.from_id, e.to_id, e.edge_type, e.confidence
		FROM edges e JOIN nodes n ON n.id = e.from_id
		WHERE n.project_id = ? AND e.edge_type = ? ORDER BY e.created_at DESC LIMIT ? OFFSET ?`,
		projectID, edgeType, limit, offset)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var out []store.Edge
	for rows.Next() {
		var e store.Edge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence); err != nil {
			return nil, err
		}
		out = append(out, e)
	}

	return out, rows.Err()
}

func (s *Store) DeleteEdge(ctx context.Context, fromID, toID, edgeType string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM edges WHERE from_id = ? AND to_id = ? AND edge_type = ?`,
		fromID, toID, edgeType)
	return err
}

func (s *Store) ResolveUnresolved(ctx context.Context, projectID, nodeID, nodeTitle string) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, source_node_id, relation_type FROM unresolved_references WHERE project_id = ? AND mention = ?`,
		projectID, nodeTitle)
	if err != nil {
		return 0, err
	}

	defer rows.Close()

	type ref struct {
		id               int64
		source, relation string
	}

	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.source, &r.relation); err != nil {
			return 0, err
		}
		refs = append(refs, r)
	}

	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, r := range refs {
		_ = s.UpsertEdge(ctx, store.Edge{
			FromID:     r.source,
			ToID:       nodeID,
			EdgeType:   r.relation,
			Confidence: 0.85,
		})
		_, _ = s.db.ExecContext(ctx, `DELETE FROM unresolved_references WHERE id = ?`, r.id)
	}

	return len(refs), nil
}
