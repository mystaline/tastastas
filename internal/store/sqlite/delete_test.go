package sqlite

import (
	"context"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline/tastastas/internal/store"
)

func TestDeleteNode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	n := store.Node{ID: "to-delete", NodeType: "fact", Title: "delete me", Content: "ephemeral"}
	if err := s.UpsertNode(ctx, n); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	// confirm it exists
	if _, err := s.GetNode(ctx, "to-delete"); err != nil {
		t.Fatalf("GetNode before delete: %v", err)
	}
	// delete
	if err := s.DeleteNode(ctx, "to-delete"); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	// confirm gone
	_, err := s.GetNode(ctx, "to-delete")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteNodeNotFound(t *testing.T) {
	s := openTest(t)
	err := s.DeleteNode(context.Background(), "does/not/exist")
	if err == nil {
		t.Fatal("expected error for missing node, got nil")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
