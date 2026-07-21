package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/ingest/docwalk"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
)

// TestDocwalkChunksAndEmbeds proves docwalk-ingested nodes get chunked and
// embedded via chunkAndEmbedNodes — the same helper both the MCP "ingest"
// tool and the HTTP POST /ingest/{adapter} handler call. Uses the real
// ONNX sidecar so this is a genuine embedding pipeline test, not a mock.
// Skips gracefully if no baked sidecar binary exists for this platform.
func TestDocwalkChunksAndEmbeds(t *testing.T) {
	sc, err := embed.NewSidecar()
	if err != nil {
		t.Skipf("sidecar unavailable on this platform: %v", err)
	}
	defer sc.Close()

	ctx := context.Background()
	db, err := sqlitestore.Open(ctx, ":memory:", 384)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte(
		"# Auth Notes\n\nJWT validation happens in the middleware layer.\n\n"+
			"## Token Parsing\n\nBearer tokens are parsed from the Authorization header.\n",
	), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	nodes, _, _, _, err := docwalk.Ingest(root, docwalk.Config{ProjectID: "docwalk-chunk-test"})
	if err != nil {
		t.Fatalf("docwalk.Ingest: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected >=1 node from docwalk.Ingest, got 0")
	}
	for i := range nodes {
		if err := db.UpsertNode(ctx, nodes[i]); err != nil {
			t.Fatalf("UpsertNode: %v", err)
		}
	}

	chunkCount, err := chunkAndEmbedNodes(ctx, db, sc, nodes, nil)
	if err != nil {
		t.Fatalf("chunkAndEmbedNodes: %v", err)
	}
	if chunkCount == 0 {
		t.Fatal("expected chunkAndEmbedNodes to produce >=1 chunk, got 0")
	}

	// Confirm the chunks are actually retrievable via vector search — not
	// just counted, but real rows with real embeddings in chunk_vectors.
	queryVec, err := sc.Embed(ctx, "how are bearer tokens extracted from headers")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	hits, err := db.SearchChunks(ctx, "docwalk-chunk-test", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected >=1 chunk hit via vector search, got 0 — chunks were counted but not actually embedded/stored correctly")
	}
}
