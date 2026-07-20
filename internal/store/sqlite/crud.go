package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mystaline-dev/tastastas/internal/store"
)

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
		INSERT INTO nodes (id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
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
			updated_at     = strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, n.ID, n.ProjectID, n.NodeType, n.Title, n.Content, n.ContentHash, n.Status, n.SourceAdapter, n.SourcePath, n.Importance)
	if err != nil {
		return fmt.Errorf("sqlite: upsert node %s: %w", n.ID, err)
	}

	if len(n.Embedding) > 0 {
		if len(n.Embedding) != s.dim {
			return fmt.Errorf("sqlite: node %s embedding has dim %d, store configured for dim %d", n.ID, len(n.Embedding), s.dim)
		}
		vecJSON, err := json.Marshal(n.Embedding)
		if err != nil {
			return fmt.Errorf("sqlite: marshal embedding for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id = ?`, n.ID)
		if err != nil {
			return fmt.Errorf("sqlite: delete stale vector for %s: %w", n.ID, err)
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO node_vectors (node_id, embedding) VALUES (?, vec_f32(?))`, n.ID, string(vecJSON))
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

func (s *Store) GetNode(ctx context.Context, id string) (store.Node, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, node_type, title, content, content_hash, status, source_adapter, source_path, importance, created_at, updated_at
		FROM nodes WHERE id = ?
	`, id)
	var n store.Node
	err := row.Scan(&n.ID, &n.ProjectID, &n.NodeType, &n.Title, &n.Content, &n.ContentHash, &n.Status, &n.SourceAdapter, &n.SourcePath, &n.Importance, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Node{}, fmt.Errorf("sqlite: node %s: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return store.Node{}, fmt.Errorf("sqlite: get node %s: %w", id, err)
	}
	return n, nil
}

func scanNodes(rows *sql.Rows) ([]store.Node, error) {
	defer rows.Close()
	var out []store.Node
	for rows.Next() {
		var n store.Node
		if err := rows.Scan(&n.ID, &n.ProjectID, &n.NodeType, &n.Title, &n.Content, &n.ContentHash, &n.Status, &n.SourceAdapter, &n.SourcePath, &n.Importance, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

const nodeCols = "n.id, n.project_id, n.node_type, n.title, n.content, n.content_hash, n.status, n.source_adapter, n.source_path, n.importance, n.created_at, n.updated_at"

func (s *Store) SearchLexical(ctx context.Context, projectID, query string, limit int) ([]store.Node, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+nodeCols+`
		FROM nodes_fts f
		JOIN nodes n ON n.id = f.id
		WHERE nodes_fts MATCH ? AND n.project_id = ?
		ORDER BY rank
		LIMIT ?
	`, query, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: search lexical: %w", err)
	}
	return scanNodes(rows)
}

func (s *Store) SearchVector(ctx context.Context, projectID string, embedding []float32, limit int) ([]store.ScoredNode, error) {
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
		if err := rows.Scan(&sn.ID, &sn.ProjectID, &sn.NodeType, &sn.Title, &sn.Content, &sn.ContentHash, &sn.Status, &sn.SourceAdapter, &sn.SourcePath, &sn.Importance, &sn.CreatedAt, &sn.UpdatedAt, &distance); err != nil {
			return nil, fmt.Errorf("sqlite: scan scored node: %w", err)
		}
		sn.Score = 1 - distance // cosine distance -> similarity-ish score, refined in internal/retrieve
		out = append(out, sn)
	}
	return out, rows.Err()
}

// Neighbors does a breadth-first walk over `edges` up to depth hops,
// optionally restricted to edgeTypes (nil/empty = all types). The returned
// edges slice is NOT positionally correlated with the returned nodes slice
// (BFS visits nodes in map-iteration order, discovers them via arbitrary
// edges) — callers needing "what edge reached this node" should use
// NeighborsWithConfidence instead.
func (s *Store) Neighbors(ctx context.Context, id string, edgeTypes []string, depth int) ([]store.Node, []store.Edge, error) {
	nodes, edges, _, err := s.neighborsBFS(ctx, id, edgeTypes, depth)
	return nodes, edges, err
}

// NeighborsWithConfidence is like Neighbors but also returns, per node ID,
// the confidence of the edge that first discovered it during the BFS walk
// (the highest-confidence edge if a node is reachable via multiple paths
// at the same depth is not guaranteed — first-discovery wins, matching BFS
// semantics). Used by internal/retrieve to boost pulled-in neighbors by the
// edge that actually connects them, instead of guessing via index alignment.
func (s *Store) NeighborsWithConfidence(ctx context.Context, id string, edgeTypes []string, depth int) ([]store.Node, map[string]float64, error) {
	nodes, _, confidence, err := s.neighborsBFS(ctx, id, edgeTypes, depth)
	return nodes, confidence, err
}

func (s *Store) neighborsBFS(ctx context.Context, id string, edgeTypes []string, depth int) ([]store.Node, []store.Edge, map[string]float64, error) {
	if depth < 1 {
		depth = 1
	}
	visited := map[string]bool{id: true}
	reachedBy := map[string]float64{} // nodeID -> confidence of the edge that first discovered it
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
			continue // node referenced by an edge but missing; skip rather than fail the whole walk
		}
		nodes = append(nodes, n)
	}
	return nodes, allEdges, reachedBy, nil
}

// MarkStaleDownstream flags nodes reachable from changedID via impact-bearing
// edge types (implements, tests, specifies, depends-on) as status=stale.
func (s *Store) MarkStaleDownstream(ctx context.Context, changedID string, maxDepth int) ([]store.Node, error) {
	impactTypes := []string{"implements", "tests", "specifies", "depends-on"}
	nodes, _, err := s.Neighbors(ctx, changedID, impactTypes, maxDepth)
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
