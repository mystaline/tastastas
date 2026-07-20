// Package sqlite is the default tastastas storage backend: a single SQLite
// file combining typed nodes/edges, FTS5 lexical search, and sqlite-vec
// nearest-neighbor search. No cgo (modernc.org/sqlite), no external services.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mystaline-dev/tastastas/internal/store"
)

type Store struct {
	db  *sql.DB
	dim int
}

func Open(ctx context.Context, path string, dim int) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	// Enable WAL mode for better concurrency
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL;`); err != nil {
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	s := &Store{db: db, dim: dim}
	if err := s.initSchema(ctx); err != nil {
		return nil, fmt.Errorf("sqlite: init schema: %w", err)
	}
	return s, nil
}

//go:embed all:migrations
var migrationsFS embed.FS

func (s *Store) initSchema(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("sqlite: read embedded migrations: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("sqlite: read migration %s: %w", e.Name(), err)
		}
		if _, err := s.db.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("sqlite: apply migration %s: %w", e.Name(), err)
		}
	}
	// vec0 tables are dimension-specific, created here rather than in the
	// static migration files.
	nodeVecStmt := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS node_vectors USING vec0(node_id TEXT PRIMARY KEY, embedding float[%d])`,
		s.dim,
	)
	if _, err := s.db.ExecContext(ctx, nodeVecStmt); err != nil {
		return fmt.Errorf("sqlite: create node_vectors: %w", err)
	}
	chunkVecStmt := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunk_vectors USING vec0(chunk_id TEXT PRIMARY KEY, embedding float[%d])`,
		s.dim,
	)
	if _, err := s.db.ExecContext(ctx, chunkVecStmt); err != nil {
		return fmt.Errorf("sqlite: create chunk_vectors: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

var _ store.Store = (*Store)(nil)

func (s *Store) UpsertNode(ctx context.Context, n store.Node) error {
	if n.ID == "" {
		return errors.New("sqlite: node id must not be empty")
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
		return fmt.Errorf("sqlite: upsert node %s: %w", n.ID, err)
	}

	if len(n.Embedding) > 0 {
		if len(n.Embedding) != s.dim {
			return fmt.Errorf(
				"sqlite: node %s embedding has dim %d, store configured for dim %d",
				n.ID,
				len(n.Embedding),
				s.dim,
			)
		}
		vecJSON, err := json.Marshal(n.Embedding)
		if err != nil {
			return fmt.Errorf("sqlite: marshal embedding for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id = ?`, n.ID)
		if err != nil {
			return fmt.Errorf("sqlite: delete stale vector for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(
			ctx,
			`INSERT INTO node_vectors (node_id, embedding) VALUES (?, vec_f32(?))`,
			n.ID,
			string(vecJSON),
		)
		if err != nil {
			return fmt.Errorf("sqlite: insert vector for %s: %w", n.ID, err)
		}
	}
	return nil
}

func (s *Store) UpsertEdge(ctx context.Context, e store.Edge) error {
	if e.FromID == "" || e.ToID == "" || e.EdgeType == "" {
		return errors.New("sqlite: edge requires from_id, to_id, edge_type")
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
		return fmt.Errorf("sqlite: upsert edge %s->%s(%s): %w", e.FromID, e.ToID, e.EdgeType, err)
	}
	return nil
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete node %s: %w", id, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: delete node %s rows affected: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("sqlite: node %s: %w", id, store.ErrNotFound)
	}
	return nil
}

func (s *Store) GetNode(ctx context.Context, id string) (store.Node, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, created_at, updated_at
		FROM nodes WHERE id = ?
	`, id)
	var n store.Node
	err := row.Scan(
		&n.ID,
		&n.ProjectID,
		&n.NodeType,
		&n.Title,
		&n.Content,
		&n.ContentHash,
		&n.Status,
		&n.SourceAdapter,
		&n.SourcePath,
		&n.Importance,
		&n.Language,
		&n.CreatedAt,
		&n.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Node{}, fmt.Errorf("sqlite: node %s: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("sqlite: get node %s: %w", id, err)
	}
	return n, nil
}

const nodeCols = "n.id, n.project_id, n.node_type, n.title, n.content, n.content_hash, n.status, n.source_adapter, n.source_path, n.importance, n.language, n.created_at, n.updated_at"

func (s *Store) SearchLexical(ctx context.Context, projectID, query string, limit int) ([]store.ScoredNode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeCols+`, rank
		FROM nodes_fts f
		JOIN nodes n ON n.id = f.id
		WHERE nodes_fts MATCH ? AND n.project_id = ?
		ORDER BY rank
		LIMIT ?
	`, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search lexical: %w", err)
	}
	defer rows.Close()
	var out []store.ScoredNode
	for rows.Next() {
		var sn store.ScoredNode
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &sn.NodeType, &sn.Title, &sn.Content, &sn.ContentHash, &sn.Status, &sn.SourceAdapter, &sn.SourcePath, &sn.Importance, &sn.Language, &sn.CreatedAt, &sn.UpdatedAt, &sn.Score); err != nil {
			return nil, fmt.Errorf("sqlite: scan scored node: %w", err)
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
		return nil, fmt.Errorf("sqlite: query embedding has dim %d, store configured for dim %d", len(embedding), s.dim)
	}
	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil, fmt.Errorf("sqlite: marshal query embedding: %w", err)
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
		return nil, fmt.Errorf("sqlite: search vector: %w", err)
	}
	defer rows.Close()
	var out []store.ScoredNode
	for rows.Next() {
		var sn store.ScoredNode
		var distance float64
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &sn.NodeType, &sn.Title, &sn.Content, &sn.ContentHash, &sn.Status, &sn.SourceAdapter, &sn.SourcePath, &sn.Importance, &sn.Language, &sn.CreatedAt, &sn.UpdatedAt, &distance); err != nil {
			return nil, fmt.Errorf("sqlite: scan scored node: %w", err)
		}
		sn.Score = 1 - distance
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *Store) UpsertChunks(ctx context.Context, chunks []store.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin chunk upsert tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Delete existing chunks for these parents
	parentIDs := make(map[string]bool)
	for _, c := range chunks {
		parentIDs[c.ParentNodeID] = true
	}
	for pid := range parentIDs {
		_, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE parent_node_id = ?`, pid)
		if err != nil {
			return fmt.Errorf("sqlite: delete old chunks for %s: %w", pid, err)
		}
		_, err = tx.ExecContext(
			ctx,
			`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT chunk_id FROM chunks WHERE parent_node_id = ?)`,
			pid,
		)
		if err != nil {
			return fmt.Errorf("sqlite: delete old chunk vectors for %s: %w", pid, err)
		}
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO chunks (chunk_id, parent_node_id, chunk_index, chunk_type, heading_path, content, language)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare chunk insert: %w", err)
	}
	defer stmt.Close()

	vecStmt, err := tx.PrepareContext(ctx, `INSERT INTO chunk_vectors (chunk_id, embedding) VALUES (?, vec_f32(?))`)
	if err != nil {
		return fmt.Errorf("sqlite: prepare chunk vector insert: %w", err)
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
			return fmt.Errorf("sqlite: insert chunk %s: %w", c.ID, err)
		}
		if len(c.Embedding) > 0 {
			if len(c.Embedding) != s.dim {
				return fmt.Errorf(
					"sqlite: chunk %s embedding has dim %d, store configured for dim %d",
					c.ID,
					len(c.Embedding),
					s.dim,
				)
			}
			vecJSON, _ := json.Marshal(c.Embedding)
			_, err := vecStmt.ExecContext(ctx, c.ID, string(vecJSON))
			if err != nil {
				return fmt.Errorf("sqlite: insert chunk vector %s: %w", c.ID, err)
			}
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteChunksByParent(ctx context.Context, parentNodeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks WHERE parent_node_id = ?`, parentNodeID)
	if err != nil {
		return fmt.Errorf("sqlite: delete chunks for %s: %w", parentNodeID, err)
	}
	_, err = s.db.ExecContext(
		ctx,
		`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT chunk_id FROM chunks WHERE parent_node_id = ?)`,
		parentNodeID,
	)
	if err != nil {
		return fmt.Errorf("sqlite: delete chunk vectors for %s: %w", parentNodeID, err)
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
		return nil, fmt.Errorf("sqlite: query embedding has dim %d, store configured for dim %d", len(embedding), s.dim)
	}
	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil, fmt.Errorf("sqlite: marshal query embedding: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.chunk_id, c.parent_node_id, c.chunk_index, c.chunk_type, c.heading_path, c.content, c.language,
		       vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
		FROM chunk_vectors v
		JOIN chunks c ON c.chunk_id = v.chunk_id
		JOIN nodes n ON n.id = c.parent_node_id
		WHERE n.project_id = ?
		ORDER BY distance
		LIMIT ?
	`, string(vecJSON), projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search chunks: %w", err)
	}
	defer rows.Close()
	var out []store.ScoredChunk
	for rows.Next() {
		var sc store.ScoredChunk
		var headingPath string
		var distance float64
		if err := rows.Scan(&sc.ID, &sc.ParentNodeID, &sc.ChunkIndex, &sc.Type, &headingPath, &sc.Content, &sc.Language, &distance); err != nil {
			return nil, fmt.Errorf("sqlite: scan scored chunk: %w", err)
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
				return nil, nil, nil, fmt.Errorf("sqlite: neighbors query: %w", err)
			}
			for rows.Next() {
				var e store.Edge
				if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence); err != nil {
					rows.Close()
					return nil, nil, nil, fmt.Errorf("sqlite: scan edge: %w", err)
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
		return nil, fmt.Errorf("sqlite: mark stale downstream: %w", err)
	}
	for i := range nodes {
		if _, err := s.db.ExecContext(ctx, `UPDATE nodes SET status = 'stale', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, nodes[i].ID); err != nil {
			return nil, fmt.Errorf("sqlite: mark stale %s: %w", nodes[i].ID, err)
		}
		nodes[i].Status = "stale"
	}
	return nodes, nil
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
