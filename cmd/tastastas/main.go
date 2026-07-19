// Package main is the tastastas entry point.
// Default mode: MCP server over stdio (embedded/CLI use).
// --serve flag: HTTP server mode (team-shared instance + webhooks).
package main

import (
	"context"
	"flag"
	"log"
	"os"

	mcpserver "github.com/mystaline-dev/tastastas/internal/mcp"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	serve := flag.Bool("serve", false, "run as HTTP server instead of stdio MCP")
	dbPath := flag.String("db", "memory.db", "path to SQLite database file")
	embedDim := flag.Int("embed-dim", 384, "embedding vector dimension (must match your embedder)")
	flag.Parse()

	db, err := sqlitestore.Open(context.Background(), *dbPath, *embedDim)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	if *serve {
		// Phase 6: HTTP server mode — stub for now.
		log.Fatal("--serve: HTTP mode not yet implemented (coming in Phase 6)")
	}

	// Stdio MCP server mode (default)
	srv := mcpserver.NewServer(db)
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("server exited: %v", err)
		os.Exit(1)
	}
}
