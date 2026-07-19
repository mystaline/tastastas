package sqlite

import (
	"context"
	"fmt"

	"github.com/mystaline-dev/tastastas/internal/store"
)

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete node %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sqlite: delete node %s: %w", id, store.ErrNotFound)
	}
	// clean up edges referencing this node
	_, _ = s.db.ExecContext(ctx, `DELETE FROM edges WHERE from_id = ? OR to_id = ?`, id, id)
	// clean up vector embeddings
	_, _ = s.db.ExecContext(ctx, `DELETE FROM node_vectors WHERE node_id = ?`, id)
	// FTS cleanup handled automatically by the nodes_ad DELETE trigger
	return nil
}
