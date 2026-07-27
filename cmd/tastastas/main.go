// Package main is the tastastas entry point.
// Default mode: MCP server over stdio (embedded/CLI use).
// --serve flag: HTTP server mode (team-shared instance + webhooks).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
	_ "turso.tech/database/tursogo"

	"github.com/mystaline-dev/tastastas/internal/embed"
	_ "github.com/mystaline-dev/tastastas/internal/onboard"
	mcpserver "github.com/mystaline-dev/tastastas/internal/mcp"
	libsqlstore "github.com/mystaline-dev/tastastas/internal/store/libsql"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
	"github.com/mystaline-dev/tastastas/internal/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

func defaultDBPath() string {
	// Honor $TASTASTAS_DB first, then $XDG_DATA_HOME/tastastas/memory.db,
	// then ~/.local/share/tastastas/memory.db. Always absolute so the
	// memory graph is cwd-independent (same DB no matter which project
	// spawned the binary).
	if v := os.Getenv("TASTASTAS_DB"); v != "" {
		return v
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "memory.db" // last-ditch fallback (preserves old relative behavior)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "tastastas", "memory.db")
}

func ensureDBDir(path string) {
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
}

func isRemoteDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "libsql://") || strings.HasPrefix(dsn, "http://") || strings.HasPrefix(dsn, "https://")
}

func main() {
	// Version probe — before any flag parsing
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version") {
		fmt.Println(mcpserver.Version)
		return
	}

	// Subcommand: update — run pending migrations then exit.
	if len(os.Args) > 1 && os.Args[1] == "update" {
		updateCmd := flag.NewFlagSet("update", flag.ExitOnError)
		dbPath := updateCmd.String("db", defaultDBPath(), "path to SQLite database file")
		embedDim := updateCmd.Int("embed-dim", 0, "embedding vector dimension")
		embedBackend := updateCmd.String("embed-backend", "sidecar", "embedder backend")
		updateCmd.Parse(os.Args[2:])

		if *embedDim <= 0 {
			switch *embedBackend {
			case "ollama":
				*embedDim = 768
			default:
				*embedDim = 384
			}
		}
		if !isRemoteDSN(*dbPath) {
			ensureDBDir(*dbPath)
		}
		if isRemoteDSN(*dbPath) {
			_, err := libsqlstore.Open(context.Background(), *dbPath, *embedDim)
			if err != nil {
				log.Fatalf("update: open store: %v", err)
			}
			return
		}
		_, err := sqlitestore.Open(context.Background(), *dbPath, *embedDim)
		if err != nil {
			log.Fatalf("update: open store: %v", err)
		}
		log.Println("update complete")
		return
	}

	serve := flag.String("serve", "", "run as HTTP server on given address (e.g. :8080)")
	graphAddr := flag.String("graph-addr", "", "serve graph visualization page on this address (e.g. :9292) — works in both stdio and HTTP mode")
	dbPath := flag.String("db", defaultDBPath(), "path to SQLite database file (default: $XDG_DATA_HOME/tastastas/memory.db — cwd-independent so all projects share one source of truth)")
	embedDim := flag.Int("embed-dim", 0, "embedding vector dimension (0 = auto-detect: 384 for sidecar, 768 for ollama)")
	embedBackend := flag.String("embed-backend", "sidecar", "embedder backend: sidecar (baked ONNX, zero deps, 384-dim), ollama (HTTP, 768-dim default with nomic-embed-text), or none (lexical only)")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama base URL (used when --embed-backend=ollama)")
	ollamaModel := flag.String("ollama-model", "nomic-embed-text", "Ollama embedding model (used when --embed-backend=ollama)")
	sidecarWorkers := flag.Int("sidecar-workers", 0, "number of sidecar workers (0 = 4, only for --embed-backend=sidecar)")
	authToken := flag.String("auth-token", "", "bearer token for HTTP server mode (empty = no auth)")
	flag.Parse()

	// Ensure DB directory exists (for local SQLite, not remote DSN).
	if !isRemoteDSN(*dbPath) {
		ensureDBDir(*dbPath)
	}

	// Auto-detect embed dim when not explicitly set (0 = default).
	if *embedDim <= 0 {
		switch *embedBackend {
		case "ollama":
			*embedDim = 768
		default:
			*embedDim = 384
		}
	}

	var db store.Store
	var err error
	if isRemoteDSN(*dbPath) {
		db, err = libsqlstore.Open(context.Background(), *dbPath, *embedDim)
	} else {
		db, err = sqlitestore.Open(context.Background(), *dbPath, *embedDim)
	}
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// Check for interrupted-run marker from previous run.
	if hasMarker, mErr := db.HasJobMarker(context.Background()); mErr == nil && hasMarker {
		log.Println("WARNING: previous ingest/onboard was interrupted. Re-run to complete. Ingest is idempotent (upsert + content-hash skip), so re-running is safe.")
	}

	embedder := newEmbedder(*embedBackend, *ollamaURL, *ollamaModel, *sidecarWorkers)
	closeEmbedder := func() {
		if closer, ok := embedder.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	// Start graph HTTP server if requested (works alongside stdio MCP or HTTP mode).
	if *graphAddr != "" {
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /graph/{project}", mcpserver.HandleGraphView(db))
			log.Printf("graph server listening on %s", *graphAddr)
			if err := http.ListenAndServe(*graphAddr, mux); err != nil {
				log.Printf("graph server: %v", err)
			}
		}()
	}

	if *serve != "" {
		// HTTP server mode
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := mcpserver.ServeHTTP(ctx, db, embedder, *serve, *authToken)
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
	if err := srv.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		db.Close() // close before os.Exit
		closeEmbedder()
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
	db.Close() // clean shutdown — close before main returns
	closeEmbedder()
}
