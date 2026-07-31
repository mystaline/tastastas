package mcp

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/onboard"
	"github.com/mystaline-dev/tastastas/internal/store/sqlite"
	"github.com/mystaline-dev/tastastas/internal/store"
)

func TestIntegrationBench(t *testing.T) {
	dbPath := "/tmp/tastastas-bench.db"
	os.Remove(dbPath)

	ctx := context.Background()
	db, err := sqlite.Open(ctx, dbPath, 384)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	embedder, err := embed.NewSidecar()
	if err != nil {
		t.Skipf("sidecar unavailable: %v", err)
	}
	// embedder = nil // uncomment for lexical-only

	var total time.Duration

	// ── 1. Onboard tastastas ──
	t.Run("onboard-tastastas", func(t *testing.T) {
		start := time.Now()
		result, err := onboard.Run(ctx, onboard.Config{
			CWD:       "/home/user/projects/tastastas",
			ProjectID: "tastastas",
			Scope:     "subtree",
			Embedder:  embedder,
			Store:     db,
		})
		elapsed := time.Since(start)
		total += elapsed
		if err != nil {
			t.Fatalf("onboard: %v", err)
		}
		fmt.Printf("  [tastastas] %v: %d symbols, %d calls, %d imports, %d docs, %d conv, %d auto, %d prop, %d files\n",
			elapsed, result.CodeSymbols, result.CallGraphEdges, result.ImportEdges,
			result.GenericDocs, result.ConventionsInferred, result.AutoLinked,
			result.ProposalsQueued, result.FilesWalked)
	})

	// ── 2. Onboard project-alpha-gofiber ──
	t.Run("onboard-project-alpha", func(t *testing.T) {
		start := time.Now()
		result, err := onboard.Run(ctx, onboard.Config{
			CWD:       "/home/user/projects/project-alpha-gofiber",
			ProjectID: "project-alpha",
			Scope:     "subtree",
			Embedder:  embedder,
			Store:     db,
		})
		elapsed := time.Since(start)
		total += elapsed
		if err != nil {
			t.Fatalf("onboard: %v", err)
		}
		fmt.Printf("  [project-alpha] %v: %d symbols, %d calls, %d imports, %d docs, %d conv, %d files\n",
			elapsed, result.CodeSymbols, result.CallGraphEdges, result.ImportEdges,
			result.GenericDocs, result.ConventionsInferred, result.FilesWalked)
	})

	// ── 3. Onboard project-beta-react ──
	t.Run("onboard-project-beta", func(t *testing.T) {
		start := time.Now()
		result, err := onboard.Run(ctx, onboard.Config{
			CWD:       "/home/user/projects/project-beta-react",
			ProjectID: "project-beta",
			Scope:     "subtree",
			Embedder:  embedder,
			Store:     db,
		})
		elapsed := time.Since(start)
		total += elapsed
		if err != nil {
			t.Fatalf("onboard: %v", err)
		}
		fmt.Printf("  [project-beta] %v: %d symbols, %d calls, %d imports, %d docs, %d conv, %d files\n",
			elapsed, result.CodeSymbols, result.CallGraphEdges, result.ImportEdges,
			result.GenericDocs, result.ConventionsInferred, result.FilesWalked)
	})

	// ── 4. Recall across projects ──
	t.Run("recall", func(t *testing.T) {
		for _, q := range []string{"main", "handler", "server", "config", "database"} {
			results, err := db.SearchLexical(ctx, "tastastas", q, 3)
			if err != nil {
				t.Logf("search '%s': %v", q, err)
				continue
			}
			fmt.Printf("  [lexical] '%s': %d hits\n", q, len(results))
			for i, r := range results {
				if i >= 3 {
					break
				}
				fmt.Printf("    %s (%.3f) %s\n", shortID(r.ID), r.Score, r.Title)
			}
		}
	})

	// ── 5. Remember ──
	t.Run("remember", func(t *testing.T) {
		err := db.UpsertNode(ctx, store.Node{
			ID:            "tastastas/fact/bench-test",
			ProjectID:     "tastastas",
			NodeType:      "generic-doc",
			Title:         "Benchmark test fact",
			Content:       "This is a benchmark verification fact stored via UpsertNode.",
			SourceAdapter: "manual",
			Importance:    0.6,
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		fmt.Println("  stored OK")
	})

	// ── 6. Stats per project ──
	t.Run("stats", func(t *testing.T) {
		for _, pid := range []string{"tastastas", "project-alpha", "project-beta"} {
			stats, err := db.Stats(ctx, pid, "")
			if err != nil {
				t.Logf("stats %s: %v", pid, err)
				continue
			}
			fmt.Printf("  %s: %d nodes, %d edges, %d chunks, %d vecs, %d conv, %d stale\n",
				pid, stats.NodeCount, stats.EdgeCount, stats.ChunkCount,
				stats.VecCount, stats.ConventionCnt, stats.StaleCount)
		}
	})

	// ── 7. Conventions ──
	t.Run("conventions", func(t *testing.T) {
		results, _ := db.SearchLexical(ctx, "tastastas", "convention", 10)
		fmt.Printf("  conventions found: %d\n", len(results))
		for _, r := range results {
			fmt.Printf("    %s (%s)\n", r.Title, r.ID)
		}
	})

	fmt.Printf("\n  Total: %v\n", total)
}

func shortID(id string) string {
	if len(id) > 60 {
		return id[:57] + "..."
	}
	return id
}
