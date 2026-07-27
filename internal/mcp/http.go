package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/onboard"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// mutating endpoints require it via the Authorization: Bearer {token} header.
// Empty token = no auth (open access — use behind VPN/SSH tunnel).
func ServeHTTP(
	ctx context.Context,
	db store.Store,
	embedder embed.EmbedderBackend,
	addr, authToken string,
) error {
	jobs := newJobStore(db)

	// MCP-over-HTTP via Streamable HTTP handler
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{
			Name:    "tastastas",
			Version: "0.1.0",
		}, nil)
		registerTools(srv, db, embedder)
		return srv
	}, nil)

	mux := http.NewServeMux()

	// MCP endpoint — all MCP protocol traffic
	mux.Handle("/mcp", mcpHandler)

	// Graph visualization — GET /graph/{project}
	mux.HandleFunc("GET /graph/{project}", HandleGraphView(db))

	// REST ingestion — POST /ingest auto-detects adapters.
	mux.HandleFunc("POST /ingest", handleIngest(db, embedder, jobs))
	mux.HandleFunc("POST /ingest/{adapter}", handleIngest(db, embedder, jobs))
	mux.HandleFunc("GET /ingest/jobs/{id}", handleIngestJobStatus(jobs))
	mux.HandleFunc("POST /ingest/webhook", handleIngestWebhook(db))

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"0.1.0"}`)
	})

	log.Printf("tastastas HTTP server listening on %s", addr)
	log.Printf("  MCP endpoint:   %s/mcp", addr)
	log.Printf("  Ingest:         %s/ingest", addr)
	log.Printf("  Webhook:        %s/ingest/webhook", addr)

	var handler http.Handler = mux
	if authToken != "" {
		handler = withBearerAuth(authToken)(mux)
	}

	server := &http.Server{Addr: addr, Handler: handler}
	go func() {
		<-ctx.Done()
		server.Close()
	}()

	return server.ListenAndServe()
}

// withBearerAuth returns middleware that checks Authorization: Bearer {token}.
// GET /health is exempt (readiness probe).
func withBearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
			if !bearerMatches(r, token) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":"unauthorized"}`)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerMatches checks the Authorization header against token in constant time.
func bearerMatches(r *http.Request, token string) bool {
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, "Bearer ") {
		return false
	}
	got = strings.TrimPrefix(got, "Bearer ")
	if len(got) != len(token) {
		return false
	}
	// ponytail: ct_eq constant-time, fine for bearer token.
	// Use subtle.ConstantTimeCompare if token contains secrets.
	ok := 0
	for i := range got {
		if got[i] == token[i] {
			ok++
		}
	}
	// Only return true if ALL bytes matched (no early exit = timing-safe)
	return ok == len(token) && len(token) > 0
}

// handleIngest handles POST /ingest/{adapter} (docwalk, gitrepo, obsidian)
// with JSON body: { "root": "/path", "config_path": "...", "project_id": "..." }
// config_path only applies to the docwalk adapter.
//
// Runs asynchronously: returns {"job_id": "..."} immediately (HTTP 202) and
// does the walk + chunk + embed in a background goroutine. Large directories
// (whole workspaces, dozens of repos) can take minutes to embed — holding
// the request open that long guarantees client/proxy timeouts. Poll
// GET /ingest/jobs/{job_id} for completion.
func handleIngest(db store.Store, embedder embed.EmbedderBackend, jobs *jobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Root      string `json:"root"`
			ProjectID string `json:"project_id"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		projectID := req.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		root := req.Root
		if root == "" {
			http.Error(w, `{"error":"root is required"}`, http.StatusBadRequest)
			return
		}

		job := jobs.create()
		jobs.runAsync(job, func(ctx context.Context) (int, int, int, error) {
			nodes, edges, _, filesWalked, filesSkipped, err := onboard.AutoDetectAdapters(ctx, db, root, projectID)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("detect adapters: %w", err)
			}
			jobs.updateCounts(job.ID, filesWalked, filesSkipped)
			for i := range nodes {
				if err := db.UpsertNode(ctx, nodes[i]); err != nil {
					return 0, 0, 0, fmt.Errorf("upsert node: %w", err)
				}
			}
			for i := range edges {
				if err := db.UpsertEdge(ctx, edges[i]); err != nil {
					return 0, 0, 0, fmt.Errorf("upsert edge: %w", err)
				}
			}
			chunkCount, err := chunkAndEmbedNodes(
				ctx, db, embedder, nodes,
				func(embedded, total int) { jobs.updateChunksEmbedded(job.ID, embedded, total) },
				func() { jobs.updatePhase(job.ID, "persisting") },
			)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("chunk/embed: %w", err)
			}
			convNodes := onboard.InferConventions(ctx, db, projectID, nodes)
			for _, cn := range convNodes {
				_ = db.UpsertNode(ctx, cn)
			}
			nodes = append(nodes, convNodes...)
			auto, proposals := onboard.Tier2ScoreAndLink(ctx, db, projectID, nodes)
			_ = auto
			_ = proposals
			return len(nodes), len(edges), chunkCount, nil
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"job_id":%q,"status":"running"}`, job.ID)
	}
}

// handleIngestJobStatus handles GET /ingest/jobs/{id} — poll an async
// ingest job's status. Returns 404 if the job ID is unknown (server
// restarted, or typo).
func handleIngestJobStatus(jobs *jobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		job, ok := jobs.get(id)

		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"job not found"}`)
			return
		}

		_ = json.NewEncoder(w).Encode(job)
	}
}

// handleIngestWebhook handles POST /ingest/webhook — generic doc-change push endpoint.
// Accepts JSON array of { "path": "...", "content": "...", "project_id": "..." }
// or single object. Creates/updates generic-doc nodes.
func handleIngestWebhook(db store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var items []struct {
			Path      string `json:"path"`
			Content   string `json:"content"`
			ProjectID string `json:"project_id"`
			NodeType  string `json:"node_type"`
			Title     string `json:"title"`
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

// handleGraphView serves the interactive graph visualization page.
func HandleGraphView(db store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("project")
		if projectID == "" {
			projectID = "default"
		}

		// Collect edges: structural + auto-linked.
		edgeTypes := []string{
			"specifies",
			"implements",
			"tests",
			"calls",
			"defines",
			"imports",
			"convention-member",
			"auto-linked",
			"references",
			"contains",
		}
		maxEdges := 500 // default: 500 edges is enough for a useful graph; full project may have 50k+
		if m := r.URL.Query().Get("max_edges"); m != "" {
			if v, err := strconv.Atoi(m); err == nil && v > 0 {
				maxEdges = v
			}
		}
		results, total, err := db.ListEdgesByProject(r.Context(), projectID, edgeTypes, maxEdges, 0)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
			return
		}

		// Build deduplicated node list with degree weight.
		nodeMap := map[string]*struct {
			id, title, ntype, group string
			weight                  int
		}{}
		addNode := func(id, title, ntype, group string) {
			if _, ok := nodeMap[id]; !ok {
				nodeMap[id] = &struct {
					id, title, ntype, group string
					weight                  int
				}{id: id, title: title, ntype: ntype, group: group}
			}
			nodeMap[id].weight++
		}
		type graphNode struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Type   string `json:"type"`
			Group  string `json:"group"`
			Weight int    `json:"weight"`
		}
		type graphEdge struct {
			Source     string  `json:"source"`
			Target     string  `json:"target"`
			EdgeType   string  `json:"edge_type"`
			Confidence float64 `json:"confidence"`
		}
		nodes := []graphNode{}
		edges := []graphEdge{}
		for _, r := range results {
			addNode(r.FromID, r.FromTitle, r.FromType, r.FromGroup)
			addNode(r.ToID, r.ToTitle, r.ToType, r.ToGroup)
			edges = append(edges, graphEdge{
				Source: r.FromID, Target: r.ToID,
				EdgeType: r.EdgeType, Confidence: r.Confidence,
			})
		}
		for _, n := range nodeMap {
			nodes = append(nodes, graphNode{
				ID: n.id, Title: n.title, Type: n.ntype, Group: n.group, Weight: n.weight,
			})
		}

		data := struct {
			ProjectID  string      `json:"project_id"`
			TotalEdges int         `json:"total_edges"`
			Returned   int         `json:"returned"`
			Nodes      []graphNode `json:"nodes"`
			Edges      []graphEdge `json:"edges"`
		}{
			ProjectID:  projectID,
			TotalEdges: total,
			Returned:   len(results),
			Nodes:      nodes,
			Edges:      edges,
		}

		jsonBytes, err := json.Marshal(data)
		if err != nil {
			http.Error(w, `{"error":"marshal"}`, http.StatusInternalServerError)
			return
		}

		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "application/json") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(jsonBytes)
			return
		}

		// Serve HTML
		page := strings.Replace(graphPageSource, "__DATA__", string(jsonBytes), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}
}
