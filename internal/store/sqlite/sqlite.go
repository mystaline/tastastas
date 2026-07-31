// Package sqlite is the default tastastas storage backend: a single SQLite
// file combining typed nodes/edges, FTS5 lexical search, and sqlite-vec
// nearest-neighbor search. No cgo (modernc.org/sqlite), no external services.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/mystaline/mig/pkg/database"
	"github.com/mystaline/mig/pkg/migrator"
	"github.com/mystaline/tastastas/internal/store"
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
			s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE name = ? AND type = 'table'", tbl).
				Scan(&sqlStr)
			wantDim := fmt.Sprintf("float[%d]", s.dim)
			if !strings.Contains(sqlStr, wantDim) {
				if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
					return fmt.Errorf("sqlite: drop stale %s: %w", tbl, err)
				}
			}
		}
		colName := "node_id"
		if tbl == "chunk_vectors" {
			colName = "chunk_id"
		}
		stmt := fmt.Sprintf(
			`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(%s TEXT PRIMARY KEY, embedding float[%d])`,
			tbl, colName, s.dim,
		)
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite: create %s: %w", tbl, err)
		}
	}

	// 4. Backfill legacy vec0 rows to composite PK format.
	// Must run after vec0 tables are confirmed to exist and match dimension.
	if err := s.backfillVec0ModelIDs(ctx); err != nil {
		return fmt.Errorf("sqlite: backfill vec0 model ids: %w", err)
	}
	return nil
}

// backfillVec0ModelIDs migrates legacy vec0 rows (pre-V2) to composite PK
// format. Legacy node_vectors had PK = node_id; V2 uses PK = "model_id:node_id".
// Runs in a transaction: on failure, old tables are untouched and retry on
// next startup.
func (s *Store) backfillVec0ModelIDs(ctx context.Context) error {
	var needBackfill int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_vectors WHERE node_id NOT LIKE '%:%'`).Scan(&needBackfill)
	if err != nil {
		// Table may not exist yet on fresh DB; skip silently.
		return nil
	}
	if needBackfill == 0 {
		return nil
	}

	type vecRow struct {
		id  string
		emb []float32
	}
	readVecs := func(table, idCol string) ([]vecRow, error) {
		q, err := s.db.QueryContext(ctx,
			fmt.Sprintf(`SELECT %s, embedding FROM %s`, idCol, table))
		if err != nil {
			return nil, err
		}
		defer q.Close()

		var rows []vecRow
		for q.Next() {
			var r vecRow
			var blob []byte
			if err := q.Scan(&r.id, &blob); err != nil {
				return nil, err
			}
			r.emb = blobToFloat32Slice(blob, s.dim)
			rows = append(rows, r)
		}
		return rows, q.Err()
	}

	nodeVecs, err := readVecs("node_vectors", "node_id")
	if err != nil {
		return fmt.Errorf("backfill: read node_vectors: %w", err)
	}
	chunkVecs, err := readVecs("chunk_vectors", "chunk_id")
	if err != nil {
		return fmt.Errorf("backfill: read chunk_vectors: %w", err)
	}

	// Resolve model_id for each node from config, fallback to "default".
	modelForNode := map[string]string{}
	cfgRows, err := s.db.QueryContext(ctx,
		`SELECT model_id FROM project_embed_config ORDER BY created_at DESC LIMIT 1`)
	if err == nil {
		defer cfgRows.Close()
		if cfgRows.Next() {
			var mid string
			cfgRows.Scan(&mid)
			if mid != "" {
				for _, r := range nodeVecs {
					modelForNode[r.id] = mid
				}
			}
		}
	}
	defaultModel := "default"
	for _, r := range nodeVecs {
		if modelForNode[r.id] == "" {
			modelForNode[r.id] = defaultModel
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("backfill: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, tbl := range []string{"node_vectors", "chunk_vectors"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
			return fmt.Errorf("backfill: drop %s: %w", tbl, err)
		}
		colName := "node_id"
		if tbl == "chunk_vectors" {
			colName = "chunk_id"
		}
		stmt := fmt.Sprintf(
			`CREATE VIRTUAL TABLE %s USING vec0(%s TEXT PRIMARY KEY, embedding float[%d])`,
			tbl, colName, s.dim,
		)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("backfill: create %s: %w", tbl, err)
		}
	}

	for _, r := range nodeVecs {
		if len(r.emb) == 0 || store.HasNonFiniteFloat32(r.emb) {
			log.Printf("sqlite: backfill: skipping node %s, invalid legacy embedding (empty or NaN/Inf)", r.id)
			continue
		}
		modelID := modelForNode[r.id]
		newID := modelID + ":" + r.id
		vecJSON, jerr := json.Marshal(r.emb)
		if jerr != nil {
			log.Printf("sqlite: backfill: skipping node %s, marshal error: %v", r.id, jerr)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_vectors (node_id, embedding) VALUES (?, vec_f32(?))`,
			newID, string(vecJSON)); err != nil {
			return fmt.Errorf("backfill: insert node vector %s: %w", r.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO node_vector_model (id, node_id, model_id) VALUES (?, ?, ?)`,
			newID, r.id, modelID); err != nil {
			return fmt.Errorf("backfill: insert node mapping %s: %w", r.id, err)
		}
	}
	for _, r := range chunkVecs {
		if len(r.emb) == 0 || store.HasNonFiniteFloat32(r.emb) {
			log.Printf("sqlite: backfill: skipping chunk %s, invalid legacy embedding (empty or NaN/Inf)", r.id)
			continue
		}
		modelID := defaultModel
		newID := modelID + ":" + r.id
		vecJSON, jerr := json.Marshal(r.emb)
		if jerr != nil {
			log.Printf("sqlite: backfill: skipping chunk %s, marshal error: %v", r.id, jerr)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunk_vectors (chunk_id, embedding) VALUES (?, vec_f32(?))`,
			newID, string(vecJSON)); err != nil {
			return fmt.Errorf("backfill: insert chunk vector %s: %w", r.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO chunk_vector_model (id, chunk_id, model_id) VALUES (?, ?, ?)`,
			newID, r.id, modelID); err != nil {
			return fmt.Errorf("backfill: insert chunk mapping %s: %w", r.id, err)
		}
	}

	return tx.Commit()
}

// blobToFloat32Slice converts a raw vec0 blob (little-endian float32 bytes) into
// a []float32 slice. vec0's C extension stores float32 in platform-native byte
// order; on amd64 this is little-endian.
func blobToFloat32Slice(blob []byte, dim int) []float32 {
	if len(blob) == 0 {
		return nil
	}
	out := make([]float32, dim)
	for i := 0; i < dim && i*4+4 <= len(blob); i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}

func (s *Store) GetNodeEmbeddings(ctx context.Context, nodeIDs []string) (map[string][]float32, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(nodeIDs))
	args := make([]any, len(nodeIDs))
	for i, id := range nodeIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT nvm.node_id, v.embedding FROM node_vectors v JOIN node_vector_model nvm ON nvm.id = v.node_id WHERE nvm.node_id IN (" + strings.Join(
		placeholders,
		",",
	) + ")"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get node embeddings: %w", err)
	}
	defer rows.Close()
	result := make(map[string][]float32)
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		if len(blob) != s.dim*4 {
			log.Printf(
				"sqlite: WARN skipping read of %s: blob len %d bytes, expected %d for dim %d (truncated/corrupt row)",
				id,
				len(blob),
				s.dim*4,
				s.dim,
			)
			continue
		}
		emb := blobToFloat32Slice(blob, s.dim)
		if store.HasNonFiniteFloat32(emb) {
			log.Printf("sqlite: WARN skipping read of %s: stored embedding contains NaN/Inf (corrupt row)", id)
			continue
		}
		result[id] = emb
	}
	return result, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SetJobMarker(ctx context.Context) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO job_marker (id) VALUES (1) ON CONFLICT(id) DO UPDATE SET started_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
	)
	return err
}

func (s *Store) ClearJobMarker(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM job_marker WHERE id = 1`)
	return err
}

func (s *Store) HasJobMarker(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_marker WHERE id = 1`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

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

	var vectorSkipped error
	switch {
	case len(n.Embedding) > 0 && store.HasNonFiniteFloat32(n.Embedding):
		log.Printf(
			"sqlite: WARN skipping vector write for node %s: embedding contains NaN/Inf (metadata still upserted)",
			n.ID,
		)
		vectorSkipped = fmt.Errorf("node %s: %w: contains NaN/Inf", n.ID, store.ErrVectorSkipped)
	case len(n.Embedding) > 0 && len(n.Embedding) != s.dim:
		log.Printf(
			"sqlite: WARN skipping vector write for node %s: embedding dim %d != store dim %d (metadata still upserted)",
			n.ID,
			len(n.Embedding),
			s.dim,
		)
		vectorSkipped = fmt.Errorf(
			"node %s: %w: dim %d != store dim %d",
			n.ID,
			store.ErrVectorSkipped,
			len(n.Embedding),
			s.dim,
		)
	case len(n.Embedding) > 0:
		vecJSON, jerr := json.Marshal(n.Embedding)
		if jerr != nil {
			log.Printf("sqlite: WARN skipping vector write for node %s: marshal error: %v", n.ID, jerr)
			vectorSkipped = fmt.Errorf("node %s: %w: marshal error: %v", n.ID, store.ErrVectorSkipped, jerr)
			break
		}
		nodeVecID := n.ID
		if n.ModelID != "" {
			nodeVecID = n.ModelID + ":" + n.ID
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id = ?`, nodeVecID)
		if err != nil {
			return fmt.Errorf("sqlite: delete stale vector for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(
			ctx,
			`INSERT INTO node_vectors (node_id, embedding) VALUES (?, vec_f32(?))`,
			nodeVecID,
			string(vecJSON),
		)
		if err != nil {
			return fmt.Errorf("sqlite: insert vector for %s: %w", n.ID, err)
		}
		if n.ModelID != "" {
			_, err = s.db.ExecContext(ctx,
				`INSERT OR REPLACE INTO node_vector_model (id, node_id, model_id) VALUES (?, ?, ?)`,
				nodeVecID, n.ID, n.ModelID)
			if err != nil {
				return fmt.Errorf("sqlite: insert node vector model mapping for %s: %w", n.ID, err)
			}
		}
	}

	if n.Title != "" {
		_, _ = s.ResolveUnresolved(ctx, n.ProjectID, n.ID, n.Title)
	}
	if vectorSkipped != nil {
		return vectorSkipped
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
	if e.ConfidenceTier == "" {
		e.ConfidenceTier = "INFERRED"
	}
	bidir := 0
	if e.Bidirectional {
		bidir = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO edges (from_id, to_id, edge_type, confidence, confidence_tier, bidirectional)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_id, to_id, edge_type) DO UPDATE SET
			confidence = MAX(edges.confidence, excluded.confidence),
			confidence_tier = CASE
				WHEN edges.confidence_tier = 'EXTRACTED' OR excluded.confidence_tier = 'EXTRACTED' THEN 'EXTRACTED'
				WHEN edges.confidence_tier = 'PROPOSED' AND excluded.confidence_tier = 'PROPOSED' THEN 'PROPOSED'
				ELSE 'INFERRED'
			END,
			bidirectional = MAX(edges.bidirectional, excluded.bidirectional)
	`, e.FromID, e.ToID, e.EdgeType, e.Confidence, e.ConfidenceTier, bidir)
	if err != nil {
		return fmt.Errorf("sqlite: upsert edge %s->%s(%s): %w", e.FromID, e.ToID, e.EdgeType, err)
	}
	return nil
}

func (s *Store) UpsertEdges(ctx context.Context, edges []store.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin edge upsert tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for i := 0; i < len(edges); i += upsertEdgeBatch {
		end := i + upsertEdgeBatch
		if end > len(edges) {
			end = len(edges)
		}
		batch := edges[i:end]

		var valStrings []string
		var args []any
		for _, e := range batch {
			if e.FromID == "" || e.ToID == "" || e.EdgeType == "" {
				return errors.New("sqlite: edge requires from_id, to_id, edge_type")
			}
			if e.Confidence == 0 {
				e.Confidence = 1.0
			}
			tier := e.ConfidenceTier
			if tier == "" {
				tier = "INFERRED"
			}
			bidir := 0
			if e.Bidirectional {
				bidir = 1
			}
			valStrings = append(valStrings, "(?,?,?,?,?,?)")
			args = append(args, e.FromID, e.ToID, e.EdgeType, e.Confidence, tier, bidir)
		}
		_, err = tx.ExecContext(ctx,
			fmt.Sprintf(`
				INSERT INTO edges (from_id, to_id, edge_type, confidence, confidence_tier, bidirectional)
				VALUES %s
				ON CONFLICT(from_id, to_id, edge_type) DO UPDATE SET
					confidence = MAX(edges.confidence, excluded.confidence),
					confidence_tier = CASE
						WHEN edges.confidence_tier = 'EXTRACTED' OR excluded.confidence_tier = 'EXTRACTED' THEN 'EXTRACTED'
						WHEN edges.confidence_tier = 'PROPOSED' AND excluded.confidence_tier = 'PROPOSED' THEN 'PROPOSED'
						ELSE 'INFERRED'
					END,
					bidirectional = MAX(edges.bidirectional, excluded.bidirectional)
			`, strings.Join(valStrings, ",")),
			args...,
		)
		if err != nil {
			return fmt.Errorf("sqlite: upsert edge batch %d-%d: %w", i, end, err)
		}
	}

	return tx.Commit()
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id LIKE ? || ':%'`, id)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id = ?`, id)
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
	modelID string,
) ([]store.ScoredNode, error) {
	if len(embedding) != s.dim {
		return nil, fmt.Errorf("sqlite: query embedding has dim %d, store configured for dim %d", len(embedding), s.dim)
	}
	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil, fmt.Errorf("sqlite: marshal query embedding: %w", err)
	}

	var rows *sql.Rows
	if modelID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT `+nodeCols+`, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
			FROM node_vectors v
			JOIN node_vector_model nvm ON nvm.id = v.node_id
			JOIN nodes n ON n.id = nvm.node_id
			WHERE nvm.model_id = ? AND n.project_id = ?
			ORDER BY distance
			LIMIT ?
		`, string(vecJSON), modelID, projectID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT `+nodeCols+`, vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
			FROM node_vectors v
			JOIN nodes n ON n.id = v.node_id
			WHERE n.project_id = ?
			ORDER BY distance
			LIMIT ?
		`, string(vecJSON), projectID, limit)
	}
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
const upsertEdgeBatch = 100

// isValidChunkEmbedding reports whether c's embedding is safe to persist:
// non-empty, correct dimension, and free of NaN/Inf. Pure check — no
// logging here, callers log once at their own call site.
func isValidChunkEmbedding(c store.Chunk, dim int) bool {
	if len(c.Embedding) == 0 {
		return false
	}
	if len(c.Embedding) != dim {
		return false
	}
	return !store.HasNonFiniteFloat32(c.Embedding)
}

func (s *Store) UpsertChunks(ctx context.Context, chunks []store.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	var skippedIDs []string
	for _, c := range chunks {
		if len(c.Embedding) > 0 && !isValidChunkEmbedding(c, s.dim) {
			reason := "unknown"
			switch {
			case store.HasNonFiniteFloat32(c.Embedding):
				reason = "NaN/Inf"
			case len(c.Embedding) != s.dim:
				reason = fmt.Sprintf("dim %d != store dim %d", len(c.Embedding), s.dim)
			}
			log.Printf("sqlite: WARN skipping vector write for chunk %s: %s", c.ID, reason)
			skippedIDs = append(skippedIDs, c.ID)
		}
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

		_, err = tx.ExecContext(
			ctx,
			fmt.Sprintf(
				`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id IN (%s))`,
				inClause,
			),
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
			chunkArgs = append(
				chunkArgs,
				c.ID,
				c.ParentNodeID,
				c.ChunkIndex,
				c.Type,
				string(headingJSON),
				c.Content,
				c.Language,
				c.SourceAdapter,
				c.PrevChunkID,
				c.NextChunkID,
			)
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

		// Insert chunk_vectors for entries whose embedding validated above.
		for _, c := range batch {
			if !isValidChunkEmbedding(c, s.dim) {
				continue
			}
			vecJSON, jerr := json.Marshal(c.Embedding)
			if jerr != nil {
				log.Printf("sqlite: WARN skipping vector write for chunk %s: marshal error: %v", c.ID, jerr)
				skippedIDs = append(skippedIDs, c.ID)
				continue
			}
			chunkVecID := c.ID
			if c.ModelID != "" {
				chunkVecID = c.ModelID + ":" + c.ID
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM chunk_vectors WHERE chunk_id = ?`, chunkVecID)
			if err != nil {
				return fmt.Errorf("sqlite: delete chunk vector %s: %w", c.ID, err)
			}
			_, err = tx.ExecContext(
				ctx,
				`INSERT INTO chunk_vectors (chunk_id, embedding) VALUES (?, vec_f32(?))`,
				chunkVecID,
				string(vecJSON),
			)
			if err != nil {
				return fmt.Errorf("sqlite: insert chunk vector %s: %w", c.ID, err)
			}
			if c.ModelID != "" {
				_, err = tx.ExecContext(ctx,
					`INSERT OR REPLACE INTO chunk_vector_model (id, chunk_id, model_id) VALUES (?, ?, ?)`,
					chunkVecID, c.ID, c.ModelID)
				if err != nil {
					return fmt.Errorf("sqlite: insert chunk vector model mapping for %s: %w", c.ID, err)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(skippedIDs) > 0 {
		return fmt.Errorf(
			"%d chunk(s) skipped [%s]: %w",
			len(skippedIDs),
			strings.Join(skippedIDs, ", "),
			store.ErrVectorSkipped,
		)
	}
	return nil
}

func (s *Store) DeleteChunksByParent(ctx context.Context, parentNodeID string, modelID string) error {
	var err error
	if modelID != "" {
		_, err = s.db.ExecContext(
			ctx,
			`DELETE FROM chunk_vectors WHERE chunk_id IN (
				SELECT cvm.id FROM chunk_vector_model cvm
				JOIN chunks c ON c.id = cvm.chunk_id
				WHERE c.parent_node_id = ? AND cvm.model_id = ?
			)`,
			parentNodeID, modelID,
		)
	} else {
		_, err = s.db.ExecContext(
			ctx,
			`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id = ?)`,
			parentNodeID,
		)
	}
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
	modelID string,
) ([]store.ScoredChunk, error) {
	if len(embedding) != s.dim {
		return nil, fmt.Errorf("sqlite: query embedding has dim %d, store configured for dim %d", len(embedding), s.dim)
	}
	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return nil, fmt.Errorf("sqlite: marshal query embedding: %w", err)
	}

	var rows *sql.Rows
	if modelID != "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT c.id, c.parent_node_id, c.chunk_index, c.chunk_type, c.heading_path, c.content, c.language, c.source_adapter, c.prev_chunk_id, c.next_chunk_id,
			       vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
			FROM chunk_vectors v
			JOIN chunk_vector_model cvm ON cvm.id = v.chunk_id
			JOIN chunks c ON c.id = cvm.chunk_id
			JOIN nodes n ON n.id = c.parent_node_id
			WHERE cvm.model_id = ? AND n.project_id = ?
			ORDER BY distance
			LIMIT ?
		`, string(vecJSON), modelID, projectID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT c.id, c.parent_node_id, c.chunk_index, c.chunk_type, c.heading_path, c.content, c.language, c.source_adapter, c.prev_chunk_id, c.next_chunk_id,
			       vec_distance_cosine(v.embedding, vec_f32(?)) AS distance
			FROM chunk_vectors v
			JOIN chunks c ON c.id = v.chunk_id
			JOIN nodes n ON n.id = c.parent_node_id
			WHERE n.project_id = ?
			ORDER BY distance
			LIMIT ?
		`, string(vecJSON), projectID, limit)
	}
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
			edgeCols := "from_id, to_id, edge_type, confidence, confidence_tier, bidirectional"
			q := `SELECT ` + edgeCols + ` FROM edges WHERE from_id = ?` + typeFilter + `
UNION ALL
SELECT to_id, from_id, edge_type, confidence, confidence_tier, bidirectional FROM edges WHERE to_id = ? AND bidirectional = 1` + typeFilter
			queryArgs := make([]any, 0, 2+2*len(edgeTypes))
			queryArgs = append(queryArgs, fromID)
			queryArgs = append(queryArgs, args...)
			queryArgs = append(queryArgs, fromID)
			queryArgs = append(queryArgs, args...)
			rows, err := s.db.QueryContext(ctx, q, queryArgs...)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("sqlite: neighbors query: %w", err)
			}
			for rows.Next() {
				var e store.Edge
				if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence, &e.ConfidenceTier, &e.Bidirectional); err != nil {
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

func (s *Store) Stats(ctx context.Context, projectID, modelID string) (store.StoreStats, error) {
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

	// VecCount = node_vectors + chunk_vectors scoped to project and optionally model.
	if modelID != "" {
		row = s.db.QueryRowContext(
			ctx,
			`SELECT (
				SELECT COUNT(*) FROM node_vectors v JOIN node_vector_model nvm ON nvm.id = v.node_id JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ? AND nvm.model_id = ?
			) + (
				SELECT COUNT(*) FROM chunk_vectors v JOIN chunk_vector_model cvm ON cvm.id = v.chunk_id JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ? AND cvm.model_id = ?
			)`,
			projectID, modelID, projectID, modelID,
		)
	} else {
		row = s.db.QueryRowContext(
			ctx,
			`SELECT (
				SELECT COUNT(*) FROM node_vectors WHERE node_id IN (
					SELECT nvm.id FROM node_vector_model nvm JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ?
				)
			) + (
				SELECT COUNT(*) FROM chunk_vectors WHERE chunk_id IN (
					SELECT cvm.id FROM chunk_vector_model cvm JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?
				)
			) + (
				SELECT COUNT(*) FROM node_vectors WHERE node_id IN (SELECT id FROM nodes WHERE project_id = ?) AND node_id NOT IN (SELECT nvm.id FROM node_vector_model nvm JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ?)
			) + (
				SELECT COUNT(*) FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id IN (SELECT id FROM nodes WHERE project_id = ?)) AND chunk_id NOT IN (SELECT cvm.id FROM chunk_vector_model cvm JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)
			)`,
			projectID, projectID, projectID, projectID, projectID, projectID,
		)
	}
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

	st.EmbedModelID, _ = s.GetEmbedModelID(ctx, projectID) // best-effort
	st.Models, _ = s.ListEmbedModels(ctx, projectID)       // best-effort

	return st, nil
}

func (s *Store) GetEmbedModelID(ctx context.Context, projectID string) (string, error) {
	var modelID string
	err := s.db.QueryRowContext(ctx,
		`SELECT model_id FROM project_embed_config WHERE project_id = ?`, projectID).Scan(&modelID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return modelID, err
}

func (s *Store) GetEmbedModelStatus(ctx context.Context, projectID, modelID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT status FROM project_embed_config WHERE project_id = ? AND model_id = ?`,
		projectID,
		modelID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

func (s *Store) InitEmbedConfig(ctx context.Context, projectID, modelID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO project_embed_config (project_id, model_id, status, created_at)
		 VALUES (?, ?, 'clean', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		projectID, modelID)
	return err
}

func (s *Store) SetEmbedModelDirty(ctx context.Context, projectID, modelID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO project_embed_config (project_id, model_id, status, created_at)
		 VALUES (?, ?, 'dirty', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		projectID, modelID)
	return err
}

func (s *Store) SetEmbedModelClean(ctx context.Context, projectID, modelID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE project_embed_config SET status = 'clean' WHERE project_id = ? AND model_id = ?`, projectID, modelID)
	return err
}

func (s *Store) ListEmbedModels(ctx context.Context, projectID string) ([]store.ModelInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model_id, status FROM project_embed_config WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.ModelInfo
	for rows.Next() {
		var mi store.ModelInfo
		if err := rows.Scan(&mi.ModelID, &mi.Status); err != nil {
			return nil, err
		}
		out = append(out, mi)
	}
	return out, rows.Err()
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
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT e.from_id, e.to_id, e.edge_type, e.confidence, e.confidence_tier, e.bidirectional
		FROM edges e JOIN nodes n ON n.id = e.from_id
		WHERE n.project_id = ? AND e.edge_type = ? ORDER BY e.created_at DESC LIMIT ? OFFSET ?`,
		projectID,
		edgeType,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var out []store.Edge
	for rows.Next() {
		var e store.Edge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence, &e.ConfidenceTier, &e.Bidirectional); err != nil {
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
	q := `SELECT e.from_id, fn.title, fn.node_type, LENGTH(fn.content), e.to_id, tn.title, tn.node_type, LENGTH(tn.content), e.edge_type, e.confidence, e.confidence_tier,
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
		if err := rows.Scan(&er.FromID, &fromTitle, &fromType, &er.FromSize, &er.ToID, &toTitle, &toType, &er.ToSize, &er.EdgeType, &er.Confidence, &er.ConfidenceTier, &totalRow); err != nil {
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
	edgeCols := "from_id, to_id, edge_type, confidence, confidence_tier, bidirectional"
	q := `SELECT ` + edgeCols + ` FROM edges WHERE from_id = ?` + typeFilter + `
UNION
SELECT to_id, from_id, edge_type, confidence, confidence_tier, bidirectional FROM edges WHERE to_id = ? AND bidirectional = 1` + typeFilter + `
ORDER BY confidence DESC`
	queryArgs := make([]any, 0, 2+2*len(edgeTypes))
	queryArgs = append(queryArgs, nodeID)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, nodeID)
	queryArgs = append(queryArgs, args...)
	rows, err := s.db.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get edges from: %w", err)
	}
	defer rows.Close()

	var out []store.Edge
	for rows.Next() {
		var e store.Edge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence, &e.ConfidenceTier, &e.Bidirectional); err != nil {
			return nil, fmt.Errorf("sqlite: scan edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetEdgesTo(ctx context.Context, nodeID string, edgeTypes []string) ([]store.Edge, error) {
	typeFilter, args := buildEdgeTypeFilter(edgeTypes)
	edgeCols := "from_id, to_id, edge_type, confidence, confidence_tier, bidirectional"
	q := `SELECT ` + edgeCols + ` FROM edges WHERE to_id = ?` + typeFilter + `
UNION
SELECT to_id, from_id, edge_type, confidence, confidence_tier, bidirectional FROM edges WHERE from_id = ? AND bidirectional = 1` + typeFilter + `
ORDER BY confidence DESC`
	queryArgs := make([]any, 0, 2+2*len(edgeTypes))
	queryArgs = append(queryArgs, nodeID)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, nodeID)
	queryArgs = append(queryArgs, args...)
	rows, err := s.db.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get edges to: %w", err)
	}
	defer rows.Close()

	var out []store.Edge
	for rows.Next() {
		var e store.Edge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.EdgeType, &e.Confidence, &e.ConfidenceTier, &e.Bidirectional); err != nil {
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

func (s *Store) ClearProject(ctx context.Context, projectID, modelID string) (store.ClearProjectResult, error) {
	if projectID == "" {
		return store.ClearProjectResult{}, errors.New("sqlite: project_id must not be empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.ClearProjectResult{}, fmt.Errorf("sqlite: begin clear project tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if modelID != "" {
		var nodeVecs, chunkVecs int
		_ = tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM node_vector_model WHERE model_id = ? AND node_id IN (SELECT id FROM nodes WHERE project_id = ?)`,
			modelID,
			projectID,
		).Scan(&nodeVecs)
		_ = tx.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM chunk_vector_model WHERE model_id = ? AND chunk_id IN (SELECT c.id FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
			modelID,
			projectID,
		).Scan(&chunkVecs)

		_, _ = tx.ExecContext(
			ctx,
			`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunk_vector_model WHERE model_id = ? AND chunk_id IN (SELECT c.id FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?))`,
			modelID,
			projectID,
		)
		_, _ = tx.ExecContext(
			ctx,
			`DELETE FROM node_vectors WHERE node_id IN (SELECT id FROM node_vector_model WHERE model_id = ? AND node_id IN (SELECT id FROM nodes WHERE project_id = ?))`,
			modelID,
			projectID,
		)
		_, _ = tx.ExecContext(
			ctx,
			`DELETE FROM node_vector_model WHERE model_id = ? AND node_id IN (SELECT id FROM nodes WHERE project_id = ?)`,
			modelID,
			projectID,
		)
		_, _ = tx.ExecContext(
			ctx,
			`DELETE FROM chunk_vector_model WHERE model_id = ? AND chunk_id IN (SELECT c.id FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
			modelID,
			projectID,
		)
		_, _ = tx.ExecContext(
			ctx,
			`DELETE FROM project_embed_config WHERE project_id = ? AND model_id = ?`,
			projectID,
			modelID,
		)

		if err := tx.Commit(); err != nil {
			return store.ClearProjectResult{}, fmt.Errorf("sqlite: commit model clear: %w", err)
		}
		return store.ClearProjectResult{Vectors: nodeVecs + chunkVecs}, nil
	}

	// Full clear.
	var nodes, edges, chunks, nodeVecs, chunkVecs int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE project_id = ?`, projectID).Scan(&nodes)
	_ = tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM edges WHERE from_id IN (SELECT id FROM nodes WHERE project_id = ?) OR to_id IN (SELECT id FROM nodes WHERE project_id = ?)`,
		projectID,
		projectID,
	).Scan(&edges)
	_ = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?`,
		projectID).Scan(&chunks)
	// Count model-prefixed vectors (stored with an explicit modelID).
	_ = tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM node_vectors WHERE node_id IN (SELECT nvm.id FROM node_vector_model nvm JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ?)`,
		projectID,
	).Scan(&nodeVecs)
	_ = tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM chunk_vectors WHERE chunk_id IN (SELECT cvm.id FROM chunk_vector_model cvm JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
		projectID,
	).Scan(&chunkVecs)
	// Count non-prefixed vectors (stored without modelID — old sidecar/REST
	// path before model-aware storage). These have no node_vector_model /
	// chunk_vector_model row, so they must be counted by direct ID match.
	var bareNodeVecs, bareChunkVecs int
	_ = tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM node_vectors WHERE node_id IN (SELECT id FROM nodes WHERE project_id = ?) AND node_id NOT IN (SELECT nvm.id FROM node_vector_model nvm JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ?)`,
		projectID, projectID,
	).Scan(&bareNodeVecs)
	_ = tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id IN (SELECT id FROM nodes WHERE project_id = ?)) AND chunk_id NOT IN (SELECT cvm.id FROM chunk_vector_model cvm JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
		projectID, projectID,
	).Scan(&bareChunkVecs)

	// Delete model-prefixed vectors.
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT cvm.id FROM chunk_vector_model cvm JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
		projectID,
	)
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM node_vectors WHERE node_id IN (SELECT nvm.id FROM node_vector_model nvm JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ?)`,
		projectID,
	)
	// Delete non-prefixed vectors — direct ID match bypasses the model
	// mapping table since these were stored without modelID.
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM node_vectors WHERE node_id IN (SELECT id FROM nodes WHERE project_id = ?) AND node_id NOT IN (SELECT nvm.id FROM node_vector_model nvm JOIN nodes n ON n.id = nvm.node_id WHERE n.project_id = ?)`,
		projectID, projectID,
	)
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM chunk_vectors WHERE chunk_id IN (SELECT id FROM chunks WHERE parent_node_id IN (SELECT id FROM nodes WHERE project_id = ?)) AND chunk_id NOT IN (SELECT cvm.id FROM chunk_vector_model cvm JOIN chunks c ON c.id = cvm.chunk_id JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
		projectID, projectID,
	)
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM node_vector_model WHERE node_id IN (SELECT id FROM nodes WHERE project_id = ?)`,
		projectID,
	)
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM chunk_vector_model WHERE chunk_id IN (SELECT c.id FROM chunks c JOIN nodes n ON n.id = c.parent_node_id WHERE n.project_id = ?)`,
		projectID,
	)
	_, _ = tx.ExecContext(ctx, `DELETE FROM project_embed_config WHERE project_id = ?`, projectID)
	_, _ = tx.ExecContext(
		ctx,
		`DELETE FROM edge_proposals WHERE from_id IN (SELECT id FROM nodes WHERE project_id = ?) OR to_id IN (SELECT id FROM nodes WHERE project_id = ?)`,
		projectID,
		projectID,
	)
	_, _ = tx.ExecContext(ctx, `DELETE FROM unresolved_references WHERE project_id = ?`, projectID)
	_, _ = tx.ExecContext(ctx, `DELETE FROM nodes WHERE project_id = ?`, projectID)

	if err := tx.Commit(); err != nil {
		return store.ClearProjectResult{}, fmt.Errorf("sqlite: commit clear project: %w", err)
	}

	return store.ClearProjectResult{
		Nodes:   nodes,
		Edges:   edges,
		Chunks:  chunks,
		Vectors: nodeVecs + chunkVecs + bareNodeVecs + bareChunkVecs,
	}, nil
}

func (s *Store) LogAccess(ctx context.Context, projectID, nodeID, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO access_log (project_id, node_id, session_id) VALUES (?, ?, ?)`,
		projectID, nodeID, sessionID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE nodes SET access_count = access_count + 1, last_accessed_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`,
		nodeID,
	)
	return err
}

func (s *Store) RecentAccesses(ctx context.Context, projectID string, withinSec int) ([]string, error) {
	query := `SELECT DISTINCT node_id FROM access_log WHERE`
	var queryArgs []any
	if projectID == "" {
		query += ` 1=1`
	} else {
		query += ` project_id = ?`
		queryArgs = append(queryArgs, projectID)
	}
	query += ` AND accessed_at >= datetime('now', ? || ' seconds') ORDER BY accessed_at DESC`
	queryArgs = append(queryArgs, fmt.Sprintf("-%d", withinSec))
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: recent accesses: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetSessionPairs(ctx context.Context, withinSec int, minSessions int) ([]store.AccessPair, error) {
	query := `
		SELECT a.node_id, b.node_id, COUNT(DISTINCT a.session_id) as cnt
		FROM access_log a
		JOIN access_log b ON a.session_id = b.session_id AND a.session_id != '' AND a.node_id < b.node_id
		WHERE a.accessed_at >= datetime('now', ? || ' seconds')
		GROUP BY a.node_id, b.node_id
		HAVING cnt >= ?`
	rows, err := s.db.QueryContext(ctx, query, fmt.Sprintf("-%d", withinSec), minSessions)
	if err != nil {
		return nil, fmt.Errorf("sqlite: session pairs: %w", err)
	}
	defer rows.Close()
	var out []store.AccessPair
	for rows.Next() {
		var p store.AccessPair
		if err := rows.Scan(&p.NodeA, &p.NodeB, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) AccessHistory(ctx context.Context, nodeID string, limit int) ([]store.Node, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT n.id, n.project_id, n.node_type, n.title, n.content, n.content_hash, n.status, n.source_adapter, n.source_path, n.importance, n.language, n.created_at, n.updated_at
		FROM access_log a JOIN nodes n ON n.id = a.node_id
		WHERE a.node_id = ?
		ORDER BY a.accessed_at DESC LIMIT ?`,
		nodeID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: access history: %w", err)
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

func (s *Store) GetAccessStats(ctx context.Context, nodeID string) (int, string, error) {
	var count int
	var lastAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT access_count, last_accessed_at FROM nodes WHERE id = ?`, nodeID).Scan(&count, &lastAt)
	if err != nil {
		return 0, "", err
	}
	return count, lastAt, nil
}

func (s *Store) ListNodesByUpdatedAfter(ctx context.Context, projectID, after string, limit int) ([]store.Node, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, created_at, updated_at
		FROM nodes WHERE project_id = ? AND updated_at > ? ORDER BY updated_at DESC LIMIT ?`,
		projectID,
		after,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list nodes by updated: %w", err)
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

func (s *Store) ListProjectIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project_id FROM nodes ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list project ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite: scan project id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetTopNodesByImportance(ctx context.Context, projectID string, limit int) ([]store.Node, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, language, created_at, updated_at
		FROM nodes WHERE project_id = ? AND node_type IN ('code:function','code:type','code:method')
		ORDER BY importance DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get top nodes: %w", err)
	}
	defer rows.Close()
	var out []store.Node
	for rows.Next() {
		var n store.Node
		if err := rows.Scan(&n.ID, &n.ProjectID, &n.NodeType, &n.Title, &n.Content, &n.ContentHash,
			&n.Status, &n.SourceAdapter, &n.SourcePath, &n.Importance, &n.Language, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) ListProjects(ctx context.Context) ([]store.ProjectInfo, error) {
	nodeRows, err := s.db.QueryContext(ctx, `SELECT project_id, COUNT(*) FROM nodes GROUP BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list projects node count: %w", err)
	}
	defer nodeRows.Close()

	nodeMap := map[string]int{}
	for nodeRows.Next() {
		var pid string
		var count int
		if err := nodeRows.Scan(&pid, &count); err != nil {
			return nil, fmt.Errorf("sqlite: scan project node count: %w", err)
		}
		nodeMap[pid] = count
	}
	if err := nodeRows.Err(); err != nil {
		return nil, err
	}

	edgeRows, err := s.db.QueryContext(ctx,
		`SELECT n.project_id, COUNT(*) FROM edges e JOIN nodes n ON n.id = e.from_id GROUP BY n.project_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list projects edge count: %w", err)
	}
	defer edgeRows.Close()

	edgeMap := map[string]int{}
	for edgeRows.Next() {
		var pid string
		var count int
		if err := edgeRows.Scan(&pid, &count); err != nil {
			return nil, fmt.Errorf("sqlite: scan project edge count: %w", err)
		}
		edgeMap[pid] = count
	}
	if err := edgeRows.Err(); err != nil {
		return nil, err
	}

	out := make([]store.ProjectInfo, 0, len(nodeMap))
	for pid, nc := range nodeMap {
		out = append(out, store.ProjectInfo{
			ProjectID: pid,
			NodeCount: nc,
			EdgeCount: edgeMap[pid],
		})
	}
	for pid := range edgeMap {
		if _, ok := nodeMap[pid]; !ok {
			log.Printf("sqlite: orphan edges found for project %q (nodes missing)", pid)
		}
	}
	return out, nil
}
