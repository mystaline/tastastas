package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// ServeHTTP starts the HTTP server with MCP-over-HTTP + REST ingestion endpoints.
// addr is the listen address (e.g. ":8080").
func ServeHTTP(ctx context.Context, db store.Store, addr string) error {
	// MCP-over-HTTP via Streamable HTTP handler
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{
			Name:    "tastastas",
			Version: "0.1.0",
		}, nil)
		registerTools(srv, db)
		return srv
	}, nil)

	mux := http.NewServeMux()

	// MCP endpoint — all MCP protocol traffic
	mux.Handle("/mcp", mcpHandler)

	// REST ingestion endpoints — POST /ingest/{adapter} dispatches to
	// docwalk, gitrepo, or obsidian.
	mux.HandleFunc("POST /ingest/{adapter}", handleIngest(db))
	mux.HandleFunc("POST /ingest/webhook", handleIngestWebhook(db))

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"0.1.0"}`)
	})

	log.Printf("tastastas HTTP server listening on %s", addr)
	log.Printf("  MCP endpoint:   %s/mcp", addr)
	log.Printf("  Ingest:         %s/ingest/{docwalk,gitrepo,obsidian}", addr)
	log.Printf("  Webhook:        %s/ingest/webhook", addr)

	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		server.Close()
	}()

	return server.ListenAndServe()
}

// handleIngest handles POST /ingest/{adapter} (docwalk, gitrepo, obsidian)
// with JSON body: { "root": "/path", "config_path": "...", "project_id": "..." }
// config_path only applies to the docwalk adapter.
func handleIngest(db store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adapter := r.PathValue("adapter")
		var req struct {
			Root       string `json:"root"`
			ConfigPath string `json:"config_path"`
			ProjectID  string `json:"project_id"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}

		nodes, edges, err := runIngestAdapter(adapter, req.Root, req.ConfigPath, req.ProjectID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"ingest: %s"}`, err), http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		for i := range nodes {
			if err := db.UpsertNode(ctx, nodes[i]); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"upsert node: %s"}`, err), http.StatusInternalServerError)
				return
			}
		}
		for i := range edges {
			if err := db.UpsertEdge(ctx, edges[i]); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"upsert edge: %s"}`, err), http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"nodes_ingested":%d,"edges_created":%d}`, len(nodes), len(edges))
	}
}

// handleIngestWebhook handles POST /ingest/webhook — generic doc-change push endpoint.
// Accepts JSON array of { "path": "...", "content": "...", "project_id": "..." }
// or single object. Creates/updates generic-doc nodes.
func handleIngestWebhook(db store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var items []struct {
			Path       string `json:"path"`
			Content    string `json:"content"`
			ProjectID  string `json:"project_id"`
			NodeType   string `json:"node_type"`
			Title      string `json:"title"`
		}

		body, _ := io.ReadAll(r.Body)
		// Try array first, then single object
		if err := json.Unmarshal(body, &items); err != nil {
			var single struct {
				Path      string `json:"path"`
				Content   string `json:"content"`
				ProjectID string `json:"project_id"`
				NodeType  string `json:"node_type"`
				Title     string `json:"title"`
			}
			if err2 := json.Unmarshal(body, &single); err2 != nil {
				http.Error(w, `{"error":"invalid JSON (expected object or array)"}`, http.StatusBadRequest)
				return
			}
			items = append(items, single)
		}

		ctx := r.Context()
		ingested := 0
		for _, item := range items {
			projectID := item.ProjectID
			if projectID == "" {
				projectID = "default"
			}
			nodeType := item.NodeType
			if nodeType == "" {
				nodeType = "generic-doc"
			}
			title := item.Title
			if title == "" {
				parts := strings.Split(item.Path, "/")
				title = parts[len(parts)-1]
			}

			n := store.Node{
				ID:            fmt.Sprintf("%s/webhook/%s", projectID, item.Path),
				ProjectID:     projectID,
				NodeType:      nodeType,
				Title:         title,
				Content:       item.Content,
				Status:        "current",
				SourceAdapter: "webhook",
				SourcePath:    item.Path,
				Importance:    0.5,
			}
			if err := db.UpsertNode(ctx, n); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"upsert: %s"}`, err), http.StatusInternalServerError)
				return
			}
			ingested++
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ingested":%d}`, ingested)
	}
}
