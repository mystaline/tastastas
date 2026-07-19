// Package sqlite is the default tastastas storage backend: a single SQLite
// file combining typed nodes/edges, FTS5 lexical search, and sqlite-vec
// nearest-neighbor search. No cgo (modernc.org/sqlite), no external services.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline-dev/tastastas/internal/store"
)

//go:embed all:migrations
var migrationsFS embed.FS

// Store is the SQLite-backed implementation of store.Store.
type Store struct {
	db  *sql.DB
	dim int // embedding dimension, fixes vec0 table width
}

// Open creates/opens a SQLite database at path (":memory:" for tests),
// applies the base schema, and creates the vec0 table for the given
// embedding dimension. dim must be > 0.
func Open(ctx context.Context, path string, dim int) (*Store, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("sqlite: embedding dimension must be > 0, got %d", dim)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite: single-writer, avoid SQLITE_BUSY

	s := &Store{db: db, dim: dim}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
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
	// vec0 table width is dimension-specific, created here rather than in the
	// static migration file.
	stmt := fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS node_vectors USING vec0(node_id TEXT PRIMARY KEY, embedding float[%d])`,
		s.dim,
	)
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("sqlite: create node_vectors: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

var _ store.Store = (*Store)(nil)
