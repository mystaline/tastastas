// Package main is the tastastas entry point.
// Default mode: MCP server over stdio (embedded/CLI use).
// --serve flag: HTTP server mode (team-shared instance + webhooks).
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/mystaline-dev/tastastas/internal/embed"
	mcpserver "github.com/mystaline-dev/tastastas/internal/mcp"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newEmbedder picks an embedding backend based on --embed-backend:
//   - "sidecar": baked ONNX binary, zero external deps, 384-dim fixed.
//     Falls back to nil (lexical-only) if this platform has no baked binary.
//   - "ollama" (default): HTTP call to a local Ollama instance.
//   - "none": explicit lexical-only mode, no embedder at all.
func newEmbedder(backend, ollamaURL, ollamaModel string, sidecarWorkers int) embed.EmbedderBackend {
	switch backend {
	case "none":
		return nil
	case "sidecar":
		if sidecarWorkers != 1 {
			// Use pool for 0 (NumCPU) or >1 workers
			p, err := embed.NewSidecarPool(sidecarWorkers)
			if err != nil {
				log.Printf("embed: sidecar pool unavailable (%v), falling back to lexical-only", err)
				return nil
			}
			return p
		}
		// Single worker (original behavior)
		sc, err := embed.NewSidecar()
		if err != nil {
			log.Printf("embed: sidecar unavailable (%v), falling back to lexical-only", err)
			return nil
		}
		return sc
	default:
		return embed.New(embed.Config{OllamaURL: ollamaURL, Model: ollamaModel})
	}
}

func main() {
	serve := flag.String("serve", "", "run as HTTP server on given address (e.g. :8080)")
	dbPath := flag.String("db", "memory.db", "path to SQLite database file")
	embedDim := flag.Int("embed-dim", 384, "embedding vector dimension (must match your embedder)")
	embedBackend := flag.String("embed-backend", "ollama", "embedder backend: ollama, sidecar, or none")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama base URL (used when --embed-backend=ollama)")
	ollamaModel := flag.String("ollama-model", "nomic-embed-text", "Ollama embedding model (used when --embed-backend=ollama)")
	sidecarWorkers := flag.Int("sidecar-workers", 0, "number of sidecar workers (0 = NumCPU, only for --embed-backend=sidecar)")
	flag.Parse()

	db, err := sqlitestore.Open(context.Background(), *dbPath, *embedDim)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	embedder := newEmbedder(*embedBackend, *ollamaURL, *ollamaModel, *sidecarWorkers)
	closeEmbedder := func() {
		if closer, ok := embedder.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}

	if *serve != "" {
		// HTTP server mode
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := mcpserver.ServeHTTP(ctx, db, embedder, *serve)
		cancel()
		db.Close() // close before exit: log.Fatalf below skips defers
		closeEmbedder()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("HTTP server: %v", err)
		}
		return
	}

	// Stdio MCP server mode (default)
	srv := mcpserver.NewServer(db, embedder)
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		db.Close() // close before os.Exit
		closeEmbedder()
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
	db.Close() // clean shutdown — close before main returns
	closeEmbedder()
}
