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

	mcpserver "github.com/mystaline-dev/tastastas/internal/mcp"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	serve := flag.String("serve", "", "run as HTTP server on given address (e.g. :8080)")
	dbPath := flag.String("db", "memory.db", "path to SQLite database file")
	embedDim := flag.Int("embed-dim", 384, "embedding vector dimension (must match your embedder)")
	flag.Parse()

	db, err := sqlitestore.Open(context.Background(), *dbPath, *embedDim)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	if *serve != "" {
		// HTTP server mode
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := mcpserver.ServeHTTP(ctx, db, *serve)
		cancel()
		db.Close() // close before exit: log.Fatalf below skips defers
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("HTTP server: %v", err)
		}
		return
	}

	// Stdio MCP server mode (default)
	srv := mcpserver.NewServer(db)
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		db.Close() // close before os.Exit
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
	db.Close() // clean shutdown — close before main returns
}
