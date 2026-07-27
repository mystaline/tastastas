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
	"github.com/mystaline/mig/pkg/database"
	"github.com/mystaline/mig/pkg/migrator"
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
	// Enable WAL mode + busy timeout for better concurrency
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode = WAL;`); err != nil {
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 30000;`); err != nil {
		return nil, fmt.Errorf("sqlite: set busy_timeout: %w", err)
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
	// 1. Run versioned migrations via mig (reads from embedded FS).
	m := migrator.NewMigratorFromFS(migrationsFS, "migrations")
	m.SetDB(database.NewSQLiteDBFromDB(s.db))
	m.Quiet = true
	if err := m.Init(ctx); err != nil {
		return fmt.Errorf("sqlite: init mig: %w", err)
	}
	if err := m.RunUp(ctx, 0); err != nil {
		return fmt.Errorf("sqlite: run migrations: %w", err)
	}

	// 2. [ONE-TIME] Existing DBs missing source_adapter column.
	// Chunks CREATE TABLE in 0002 includes it; old DBs created before this
	// migration system don't have it. SQLite's CREATE TABLE IF NOT EXISTS
	// won't add missing columns, so we check and fix here.
	var hasCol int
	_ = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info WHERE name = 'chunks' AND name = 'source_adapter'`).Scan(&hasCol)
	if hasCol == 0 {
		_, _ = s.db.ExecContext(ctx,
			`ALTER TABLE chunks ADD COLUMN source_adapter TEXT NOT NULL DEFAULT ''`)
	}

	// 3. vec0 tables are dimension-specific. If an existing vec0 table has a
	// different dimension (e.g. user switched embedder from ollama 768-dim to
	// sidecar 384-dim), drop + re-create automatically. vec0 doesn't support
	// ALTER TABLE to change float[N].
	for _, tbl := range []string{"node_vectors", "chunk_vectors"} {
		exists := 0
		s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_list WHERE name = ?", tbl).Scan(&exists)
		if exists != 0 {
			// Check current dimension
			var sqlStr string
			s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = ? AND type = 'table'", tbl).Scan(&sqlStr)
			wantDim := fmt.Sprintf("float[%d]", s.dim)
			if !strings.Contains(sqlStr, wantDim) {
				if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
					return fmt.Errorf("sqlite: drop stale %s: %w", tbl, err)
				}
			}
		}
		stmt := fmt.Sprintf(
			`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(%s TEXT PRIMARY KEY, embedding float[%d])`,
			tbl,
			map[string]string{
				"node_vectors":  "node_id",
				"chunk_vectors": "chunk_id",
			}[tbl],
			s.dim,
		)
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite: create %s: %w", tbl, err)
		}
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

	if n.Title != "" {
		_, _ = s.ResolveUnresolved(ctx, n.ProjectID, n.ID, n.Title)
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
		SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, created_at, updated_at,
		       EXISTS(SELECT 1 FROM chunks WHERE parent_node_id = nodes.id)
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
		&n.HasChunks,
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
		SELECT `+nodeCols+`
		FROM nodes_fts f
		JOIN nodes n ON n.id = f.id
		WHERE nodes_fts MATCH ? AND n.project_id = ?
		LIMIT ?
	`, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search lexical: %w", err)
	}

	defer rows.Close()

	var out []store.ScoredNode
	for rows.Next() {
		var sn store.ScoredNode
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &sn.NodeType, &sn.Title, &sn.Content, &sn.ContentHash, &sn.Status, &sn.SourceAdapter, &sn.SourcePath, &sn.Importance, &sn.Language, &sn.CreatedAt, &sn.UpdatedAt); err != nil {
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

// UpsertChunks deletes any existing chunks for the same parent nodes,
// then inserts the new chunks. This makes re-ingestion idempotent.
// upsertChunkBatch inserts up to batchSize chunks + their vectors in a
// single multi-row INSERT each. Using prepared statements here is slower
// with modernc.org/sqlite — each ExecContext has overhead that dominates
// at N > 100, even within a transaction.
const upsertChunkBatch = 100

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

	// Delete existing chunks for these parents — single DELETE per table.
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

		_, err = tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id IN (%s))`, inClause),
			args...,
		)
		if err != nil {
			return fmt.Errorf("sqlite: delete old chunk vectors: %w", err)
		}
		_, err = tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM chunks WHERE parent_node_id IN (%s)`, inClause),
			args...,
		)
		if err != nil {
			return fmt.Errorf("sqlite: delete old chunks: %w", err)
		}
	}

	chunkCols := "(id, parent_node_id, chunk_index, chunk_type, heading_path, content, language, source_adapter, prev_chunk_id, next_chunk_id)"

	for i := 0; i < len(chunks); i += upsertChunkBatch {
		end := i + upsertChunkBatch
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		// Multi-row INSERT for chunks with ON CONFLICT to handle re-ingestion idempotency.
		// The UNIQUE constraint is on (parent_node_id, chunk_index) — use DO UPDATE to
		// replace content/heading_path/language when the same chunk slot is reused.
		var chunkValStrings []string
		var chunkArgs []any
		for _, c := range batch {
			headingJSON, _ := json.Marshal(c.HeadingPath)
			chunkValStrings = append(chunkValStrings, "(?,?,?,?,?,?,?,?,?,?)")
			chunkArgs = append(chunkArgs, c.ID, c.ParentNodeID, c.ChunkIndex, c.Type, string(headingJSON), c.Content, c.Language, c.SourceAdapter, c.PrevChunkID, c.NextChunkID)
		}
		_, err = tx.ExecContext(ctx,
			fmt.Sprintf(`
				INSERT INTO chunks %s VALUES %s
				ON CONFLICT(parent_node_id, chunk_index) DO UPDATE SET
					id             = excluded.id,
					chunk_type     = excluded.chunk_type,
					heading_path   = excluded.heading_path,
					content        = excluded.content,
					language       = excluded.language,
					source_adapter = excluded.source_adapter,
					created_at     = strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now')
			`, chunkCols, strings.Join(chunkValStrings, ",")),
			chunkArgs...,
		)
		if err != nil {
			return fmt.Errorf("sqlite: insert chunk batch %d-%d: %w", i, end, err)
		}

		// Multi-row INSERT for chunk_vectors with ON CONFLICT (chunk_id is PRIMARY KEY)
		var vecValStrings []string
		var vecArgs []any
		for _, c := range batch {
			if len(c.Embedding) == 0 {
				continue
			}
			if len(c.Embedding) != s.dim {
				return fmt.Errorf("sqlite: chunk %s embedding has dim %d, store configured for dim %d", c.ID, len(c.Embedding), s.dim)
			}
			vecJSON, _ := json.Marshal(c.Embedding)
			vecValStrings = append(vecValStrings, "(?, vec_f32(?))")
			vecArgs = append(vecArgs, c.ID, string(vecJSON))
		}
		if len(vecValStrings) > 0 {
			for _, c := range batch {
				if len(c.Embedding) == 0 {
					continue
				}
				vecJSON, _ := json.Marshal(c.Embedding)
				_, err = tx.ExecContext(ctx, `DELETE FROM chunk_vectors WHERE chunk_id = ?`, c.ID)
				if err != nil {
					return fmt.Errorf("sqlite: delete chunk vector %s: %w", c.ID, err)
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO chunk_vectors (chunk_id, embedding) VALUES (?, vec_f32(?))`, c.ID, string(vecJSON))
				if err != nil {
					return fmt.Errorf("sqlite: insert chunk vector %s: %w", c.ID, err)
				}
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
		return fmt.Errorf("sqlite: delete chunk vectors for %s: %w", parentNodeID, err)
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM chunks WHERE parent_node_id = ?`, parentNodeID)
	if err != nil {
		return fmt.Errorf("sqlite: delete chunks for %s: %w", parentNodeID, err)
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
		SELECT c.id, c.parent_node_id, c.chunk_index, c.chunk_type, c.heading_path, c.content, c.language, c.source_adapter, c.prev_chunk_id, c.next_chunk_id,
		       vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
		FROM chunk_vectors v
		JOIN chunks c ON c.id = v.chunk_id
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
		if err := rows.Scan(&sc.ID, &sc.ParentNodeID, &sc.ChunkIndex, &sc.Type, &headingPath, &sc.Content, &sc.Language, &sc.SourceAdapter, &sc.PrevChunkID, &sc.NextChunkID, &distance); err != nil {
			return nil, fmt.Errorf("sqlite: scan scored chunk: %w", err)
		}
		_ = json.Unmarshal([]byte(headingPath), &sc.HeadingPath)
		sc.Score = 1 - distance
		out = append(out, sc)
	}

	return out, rows.Err()
}

func (s *Store) GetChunksByParent(ctx context.Context, parentNodeID string, limit, offset int) ([]store.Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, parent_node_id, chunk_index, chunk_type, heading_path, content, language, source_adapter, prev_chunk_id, next_chunk_id
		FROM chunks
		WHERE parent_node_id = ?
		ORDER BY chunk_index
		LIMIT ? OFFSET ?
	`, parentNodeID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get chunks by parent: %w", err)
	}
	defer rows.Close()

	var out []store.Chunk
	for rows.Next() {
		var c store.Chunk
		var headingPath string
		if err := rows.Scan(&c.ID, &c.ParentNodeID, &c.ChunkIndex, &c.Type, &headingPath, &c.Content, &c.Language, &c.SourceAdapter, &c.PrevChunkID, &c.NextChunkID); err != nil {
			return nil, fmt.Errorf("sqlite: scan chunk: %w", err)
		}
		_ = json.Unmarshal([]byte(headingPath), &c.HeadingPath)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CountChunksByParent(ctx context.Context, parentNodeID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE parent_node_id = ?`, parentNodeID).Scan(&count)
	return count, err
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

func (s *Store) Stats(ctx context.Context, projectID string) (store.StoreStats, error) {
	var st store.StoreStats
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = ?`, projectID)
	if err := row.Scan(&st.NodeCount); err != nil {
		return st, fmt.Errorf("sqlite: stats nodes: %w", err)
	}

	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM edges e JOIN nodes n ON n.id = e.from_id WHERE n.project_id = ?`,
		projectID,
	)
	if err := row.Scan(&st.EdgeCount); err != nil {
		return st, fmt.Errorf("sqlite: stats edges: %w", err)
	}

	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?`,
		projectID,
	)
	if err := row.Scan(&st.ChunkCount); err != nil {
		return st, fmt.Errorf("sqlite: stats chunks: %w", err)
	}

	row = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = ? AND status = 'stale'`, projectID)
	if err := row.Scan(&st.StaleCount); err != nil {
		return st, fmt.Errorf("sqlite: stats stale: %w", err)
	}

	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM node_vectors v JOIN nodes n ON n.id = v.node_id WHERE n.project_id = ?`,
		projectID,
	)
	if err := row.Scan(&st.VecCount); err != nil {
		return st, fmt.Errorf("sqlite: stats vectors: %w", err)
	}

	row = s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM nodes WHERE project_id = ? AND node_type = 'code:convention'`,
		projectID,
	)
	if err := row.Scan(&st.ConventionCnt); err != nil {
		return st, fmt.Errorf("sqlite: stats conventions: %w", err)
	}

	return st, nil
}

func (s *Store) EdgeTypeCounts(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.edge_type, COUNT(*) FROM edges e
		JOIN nodes n ON n.id = e.from_id
		WHERE n.project_id = ?
		GROUP BY e.edge_type`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: edge type counts: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err != nil {
			return nil, fmt.Errorf("sqlite: scan edge type count: %w", err)
		}
		out[t] = c
	}
	return out, rows.Err()
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

func (s *Store) ListEdgesByProject(
	ctx context.Context,
	projectID string,
	edgeTypes []string,
	limit, offset int,
) ([]store.EdgeResult, int, error) {
	// Use COUNT(*) OVER() windowed to get total count in same query.
	q := `SELECT e.from_id, fn.title, fn.node_type, e.to_id, tn.title, tn.node_type, e.edge_type, e.confidence,
	       COUNT(*) OVER() AS total
		FROM edges e
		JOIN nodes fn ON fn.id = e.from_id
		JOIN nodes tn ON tn.id = e.to_id
		WHERE fn.project_id = ?`
	args := []any{projectID}

	if len(edgeTypes) > 0 {
		placeholders := make([]string, len(edgeTypes))
		for i, t := range edgeTypes {
			placeholders[i] = "?"
			args = append(args, t)
		}
		q += ` AND e.edge_type IN (` + strings.Join(placeholders, ",") + `)`
	}
	q += ` ORDER BY e.confidence DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlite: list edges by project: %w", err)
	}
	defer rows.Close()

	var out []store.EdgeResult
	total := 0
	for rows.Next() {
		var er store.EdgeResult
		var fromTitle, fromType, toTitle, toType string
		var totalRow int
		if err := rows.Scan(&er.FromID, &fromTitle, &fromType, &er.ToID, &toTitle, &toType, &er.EdgeType, &er.Confidence, &totalRow); err != nil {
			return nil, 0, fmt.Errorf("sqlite: scan edge result: %w", err)
		}
		total = totalRow
		er.FromTitle = fromTitle
		er.FromType = fromType
		er.ToTitle = toTitle
		er.ToType = toType
		er.FromGroup = extractGroup(er.FromID, projectID)
		er.ToGroup = extractGroup(er.ToID, projectID)
		out = append(out, er)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func extractGroup(id, projectID string) string {
	trimmed := strings.TrimPrefix(id, projectID+"/")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		return trimmed[:i]
	}
	return ""
}

func (s *Store) GetEdgesFrom(ctx context.Context, nodeID string, edgeTypes []string) ([]store.Edge, error) {
	typeFilter, args := buildEdgeTypeFilter(edgeTypes)
	q := `SELECT from_id, to_id, edge_type, confidence FROM edges WHERE from_id = ?` + typeFilter
	queryArgs := append([]any{nodeID}, args...)
	rows, err := s.db.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get edges from: %w", err)
	}
	defer rows.Close()

	var out []store.Edge
	for rows.Next() {
		var e store.Edge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence); err != nil {
			return nil, fmt.Errorf("sqlite: scan edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetEdgesTo(ctx context.Context, nodeID string, edgeTypes []string) ([]store.Edge, error) {
	typeFilter, args := buildEdgeTypeFilter(edgeTypes)
	q := `SELECT from_id, to_id, edge_type, confidence FROM edges WHERE to_id = ?` + typeFilter
	queryArgs := append([]any{nodeID}, args...)
	rows, err := s.db.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get edges to: %w", err)
	}
	defer rows.Close()

	var out []store.Edge
	for rows.Next() {
		var e store.Edge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence); err != nil {
			return nil, fmt.Errorf("sqlite: scan edge: %w", err)
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
