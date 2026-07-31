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
	"time"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
	_ "turso.tech/database/tursogo"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mystaline/tastastas/internal/consolidate"
	"github.com/mystaline/tastastas/internal/embed"
	mcpserver "github.com/mystaline/tastastas/internal/mcp"
	_ "github.com/mystaline/tastastas/internal/onboard"
	"github.com/mystaline/tastastas/internal/store"
	libsqlstore "github.com/mystaline/tastastas/internal/store/libsql"
	sqlitestore "github.com/mystaline/tastastas/internal/store/sqlite"
)

// newEmbedder picks an embedding backend based on --embed-backend:
//   - "sidecar": baked ONNX binary, zero external deps, 384-dim fixed.
//     Falls back to nil (lexical-only) if this platform has no baked binary.
//   - "openai": HTTP call to OpenAI embeddings API (cloud, 1536-dim).
//     Requires --openai-api-key or $TASTASTAS_OPENAI_KEY.
//   - "ollama" (default): HTTP call to a local Ollama instance.
//   - "none": explicit lexical-only mode, no embedder at all.
func newEmbedder(backend string, sidecarWorkers, sidecarIntraThreads, batchSize int,
	ollamaURL, ollamaModel string,
	openaiKey, openaiModel, openaiBaseURL string,
	embedDim int, maxContentBytes int) embed.EmbedderBackend {
	sc := embed.SidecarConfig{
		IntraThreads: sidecarIntraThreads,
		MaxBatchSize: batchSize,
	}
	switch backend {
	case "none":
		return nil
	case "sidecar":
		if sidecarWorkers != 1 {
			p, err := embed.NewSidecarPoolWithConfig(sidecarWorkers, sc)
			if err != nil {
				log.Printf("embed: sidecar pool unavailable (%v), falling back to lexical-only", err)
				return nil
			}
			return p
		}
		se, err := embed.NewSidecarWithConfig(sc)
		if err != nil {
			log.Printf("embed: sidecar unavailable (%v), falling back to lexical-only", err)
			return nil
		}
		return se
	case "openai":
		if openaiKey == "" {
			log.Fatal("embed: --openai-api-key or $TASTASTAS_OPENAI_KEY required for --embed-backend=openai")
		}
		return embed.NewOpenAI(openaiKey, openaiModel, openaiBaseURL, embedDim, batchSize, maxContentBytes)
	default:
		return embed.New(embed.Config{
			OllamaURL:       ollamaURL,
			Model:           ollamaModel,
			MaxBatchSize:    batchSize,
			MaxContentBytes: maxContentBytes,
		})
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
	return strings.HasPrefix(dsn, "libsql://") || strings.HasPrefix(dsn, "http://") ||
		strings.HasPrefix(dsn, "https://")
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

		if strings.HasPrefix(*dbPath, "~/") {
			home, err := os.UserHomeDir()
			if err == nil {
				*dbPath = filepath.Join(home, (*dbPath)[2:])
			}
		}

		if *embedDim <= 0 {
			switch *embedBackend {
			case "openai":
				*embedDim = 1536
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
	graphAddr := flag.String(
		"graph-addr",
		"",
		"serve graph visualization page on this address (e.g. :9292) — works in both stdio and HTTP mode",
	)
	dbPath := flag.String(
		"db",
		defaultDBPath(),
		"path to SQLite database file (default: $XDG_DATA_HOME/tastastas/memory.db — cwd-independent so all projects share one source of truth)",
	)
	embedDim := flag.Int(
		"embed-dim",
		0,
		"embedding vector dimension (0 = auto-detect: 384 for sidecar, 768 for ollama, 1536 for openai)",
	)
	embedBackend := flag.String(
		"embed-backend",
		"sidecar",
		"embedder backend: sidecar (baked ONNX, zero deps, 384-dim), ollama (HTTP, 768-dim), openai (cloud API, 1536-dim), or none (lexical only)",
	)
	ollamaURL := flag.String(
		"ollama-url",
		"http://localhost:11434",
		"Ollama base URL (used when --embed-backend=ollama)",
	)
	ollamaModel := flag.String(
		"ollama-model",
		"nomic-embed-text",
		"Ollama embedding model (used when --embed-backend=ollama)",
	)
	sidecarWorkers := flag.Int(
		"sidecar-workers",
		0,
		"number of sidecar workers (0 = 4, only for --embed-backend=sidecar)",
	)
	openaiKey := flag.String("openai-api-key", "", "OpenAI API key (prefer $TASTASTAS_OPENAI_KEY env var)")
	openaiModel := flag.String(
		"openai-model",
		"text-embedding-3-small",
		"OpenAI model ID (used when --embed-backend=openai)",
	)
	openaiBaseURL := flag.String(
		"openai-base-url",
		"https://api.openai.com/v1",
		"OpenAI API base URL (used when --embed-backend=openai)",
	)
	sidecarIntraThreads := flag.Int(
		"sidecar-intra-threads",
		0,
		"sidecar ONNX intra-op thread count (0 = ONNX default = all cores, 2 = ~200% CPU on 4-vCPU, only for --embed-backend=sidecar)",
	)
	batchSize := flag.Int("batch-size", 32, "max texts per embed batch (16 saves ~50% peak RAM, 32=throughput)")
	authToken := flag.String("auth-token", "", "bearer token for HTTP server mode (empty = no auth)")
	spaDir := flag.String(
		"spa-dir",
		"",
		"path to built React SPA directory (empty = use embedded frontend) — also read from $TASTASTAS_SPA_DIR",
	)
	consolidateInterval := flag.String(
		"consolidate-interval",
		"",
		"consolidation cron interval (e.g. '1h', '30m'). Empty = disabled",
	)
	embedMaxContent := flag.Int(
		"embed-max-content",
		0,
		"max bytes per input text for the embedding model (0 = auto-detect: 8192 for unknown models, larger for known models like Nemotron)",
	)
	flag.Parse()

	if *spaDir == "" {
		*spaDir = os.Getenv("TASTASTAS_SPA_DIR")
	}
	if *spaDir != "" {
		if info, err := os.Stat(*spaDir); err != nil || !info.IsDir() {
			log.Printf(
				"warning: --spa-dir %s not found — SPA graph page unavailable (legacy ?v=legacy still works)",
				*spaDir,
			)
		}
	}

	if *openaiKey == "" {
		*openaiKey = os.Getenv("TASTASTAS_OPENAI_KEY")
	}

	// Expand ~/ in dbPath — MCP clients spawn without shell, so tilde
	// isn't expanded by the OS.
	if strings.HasPrefix(*dbPath, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			*dbPath = filepath.Join(home, (*dbPath)[2:])
		}
	}

	// Ensure DB directory exists (for local SQLite, not remote DSN).
	if !isRemoteDSN(*dbPath) {
		ensureDBDir(*dbPath)
	}

	// Auto-detect embed dim when not explicitly set (0 = default).
	if *embedDim <= 0 {
		switch *embedBackend {
		case "openai":
			if *openaiKey == "" {
				log.Fatal("embed: --openai-api-key or $TASTASTAS_OPENAI_KEY required for --embed-backend=openai")
			}
			probed, err := embed.ProbeOpenAIDim(*openaiKey, *openaiModel, *openaiBaseURL)
			if err != nil {
				log.Fatalf("embed: probe openai dim: %v", err)
			}
			*embedDim = probed
		case "ollama":
			probed, err := embed.ProbeOllamaDim(*ollamaURL, *ollamaModel)
			if err != nil {
				log.Fatalf("embed: probe ollama dim: %v", err)
			}
			*embedDim = probed
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

	// Root context — SIGINT/SIGTERM cancels in both modes, draining servers,
	// stopping the consolidator, and canceling in-flight ingest jobs.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	mcpserver.SetJobContext(ctx)

	if *consolidateInterval != "" {
		dur, err := time.ParseDuration(*consolidateInterval)
		if err != nil {
			log.Fatalf("consolidate: invalid interval %q: %v", *consolidateInterval, err)
		}
		go consolidate.RunPeriodic(ctx, db, dur)
		log.Printf("consolidate: cron started at %v interval", dur)
	}

	// Check for interrupted-run marker from previous run.
	if hasMarker, mErr := db.HasJobMarker(context.Background()); mErr == nil && hasMarker {
		log.Println(
			"WARNING: previous ingest/onboard was interrupted. Re-run to complete. Ingest is idempotent (upsert + content-hash skip), so re-running is safe.",
		)
	}

	embedder := newEmbedder(*embedBackend, *sidecarWorkers, *sidecarIntraThreads, *batchSize,
		*ollamaURL, *ollamaModel,
		*openaiKey, *openaiModel, *openaiBaseURL, *embedDim, *embedMaxContent)

	modelID := ""
	switch *embedBackend {
	case "sidecar":
		modelID = "sidecar:bge-small-en-v1.5:384"
	case "openai":
		modelID = fmt.Sprintf("openai:%s:%d", *openaiModel, *embedDim)
	case "ollama":
		modelID = fmt.Sprintf("ollama:%s", *ollamaModel)
	}

	closeEmbedder := func() {
		if closer, ok := embedder.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	// Start graph HTTP server if requested (works alongside stdio MCP or HTTP mode).
	if *graphAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /graph/{project}", mcpserver.HandleGraphData(db))
		mux.HandleFunc("GET /api/graph/{project}", mcpserver.HandleGraphData(db))
		mux.HandleFunc("GET /graph/{project}/", mcpserver.HandleGraphSPA(*spaDir))
		graphServer := &http.Server{Addr: *graphAddr, Handler: mux}
		go func() {
			log.Printf("graph server listening on %s", *graphAddr)
			if err := graphServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("graph server: %v", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			graphServer.Shutdown(shutdownCtx)
		}()
	}

	if *serve != "" {
		// HTTP server mode
		err := mcpserver.ServeHTTP(ctx, db, embedder, *serve, *authToken, *batchSize, modelID, *spaDir)
		cancel()
		db.Close() // close before log.Fatalf below skips defers
		closeEmbedder()
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server: %v", err)
		}
		return
	}

	// Stdio MCP server mode (default)
	srv := mcpserver.NewServer(db, embedder, *batchSize, modelID)
	if err := srv.Run(ctx, &mcpsdk.StdioTransport{}); err != nil {
		// stdio loop ended (client disconnect or signal) — stop jobs, wait
		// for drain, then close the DB before exit.
		mcpserver.CancelJobs()
		mcpserver.WaitForJobs(30 * time.Second)
		db.Close() // close before os.Exit
		closeEmbedder()
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
	mcpserver.CancelJobs()
	mcpserver.WaitForJobs(30 * time.Second)
	db.Close() // clean shutdown — close before main returns
	closeEmbedder()
}
