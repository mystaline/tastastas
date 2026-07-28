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
	"sync"

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
	batchSize int,
) error {
	jobs := newJobStore(db)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{
			Name:    "tastastas",
			Version: Version,
		}, nil)
		registerTools(srv, db, embedder, batchSize)
		return srv
	}, nil)

	mux := http.NewServeMux()

	// MCP endpoint — all MCP tools available via Streamable HTTP
	mux.Handle("/mcp", mcpHandler)

	// Graph visualization — GET /graph/{project}
	mux.HandleFunc("GET /graph/{project}", HandleGraphView(db))

	// REST ingest — POST /ingest auto-detects adapters, same pipeline as MCP ingest tool.
	mux.HandleFunc("POST /ingest", handleRESTIngest(db, embedder, jobs, batchSize))
	mux.HandleFunc("GET /ingest/jobs/{id}", handleIngestJobStatus(jobs))

	// Health check — exempt from auth
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, Version)
	})

	log.Printf("tastastas HTTP server listening on %s", addr)
	log.Printf("  MCP endpoint: %s/mcp", addr)
	log.Printf("  Graph:        %s/graph/{project}", addr)
	log.Printf("  REST ingest:  %s/ingest", addr)

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

func bearerMatches(r *http.Request, token string) bool {
	got := r.Header.Get("Authorization")
	if !strings.HasPrefix(got, "Bearer ") {
		return false
	}
	got = strings.TrimPrefix(got, "Bearer ")
	if len(got) != len(token) {
		return false
	}
	ok := 0
	for i := range got {
		if got[i] == token[i] {
			ok++
		}
	}
	return ok == len(token) && len(token) > 0
}

func HandleGraphView(db store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("project")
		if projectID == "" {
			projectID = "default"
		}

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
		maxEdges := 500
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

		if q := r.URL.Query().Get("q"); q != "" {
			q = strings.ToLower(q)
			var filtered []graphNode
			for _, n := range nodes {
				if strings.Contains(strings.ToLower(n.Title), q) || strings.Contains(strings.ToLower(n.ID), q) {
					filtered = append(filtered, n)
				}
			}
			// Keep only edges that connect filtered nodes
			keep := map[string]bool{}
			for _, n := range filtered {
				keep[n.ID] = true
			}
			var fe []graphEdge
			for _, e := range edges {
				if keep[e.Source] && keep[e.Target] {
					fe = append(fe, e)
				}
			}
			nodes = filtered
			edges = fe
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

		page := strings.Replace(graphPageSource, "__DATA__", string(jsonBytes), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}
}

// handleRESTIngest handles POST /ingest — auto-detect adapters, same pipeline
// as the MCP ingest tool. Async: returns { job_id, status } immediately.
func handleRESTIngest(db store.Store, embedder embed.EmbedderBackend, jobs *jobStore, batchSize int) http.HandlerFunc {
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
		// same pipeline as MCP ingest tool (server.go:466-520)
		safeGo(func() {
			ctx := context.Background()
			nodes, edges, _, filesWalked, filesSkipped, err := onboard.AutoDetectAdapters(ctx, db, root, projectID)
			if err != nil {
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("detect adapters: %w", err))
				return
			}
			jobs.updateCounts(job.ID, filesWalked, filesSkipped)
			for _, n := range nodes {
				if err := db.UpsertNode(ctx, n); err != nil {
					jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert node: %w", err))
					return
				}
			}
			if err := db.UpsertEdges(ctx, edges); err != nil {
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert edges: %w", err))
				return
			}
			jobs.updatePhase(job.ID, "waiting")
			ingestMu.Lock()
			jobs.updatePhase(job.ID, "chunking")

			embedOnce := sync.OnceFunc(func() { jobs.updatePhase(job.ID, "embedding") })
			chunkCount, err := chunkAndEmbedNodes(
				ctx, db, embedder, nodes, batchSize,
				func(embedded, total int) {
					embedOnce()
					jobs.updateChunksEmbedded(job.ID, embedded, total)
				},
				func() { jobs.updatePhase(job.ID, "persisting") },
			)
			if err != nil {
				ingestMu.Unlock()
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("chunk/embed: %w", err))
				return
			}
			if embedder != nil {
				_ = onboard.EmbedNodes(ctx, db, nodes, embedder, batchSize)
			}
			ingestMu.Unlock()

			jobs.updatePhase(job.ID, "linking")

			convNodes := onboard.InferConventions(ctx, db, projectID, nodes)
			for _, cn := range convNodes {
				_ = db.UpsertNode(ctx, cn)
			}
			nodes = append(nodes, convNodes...)
			hierNodes, hierEdges := onboard.BuildHierarchy(projectID, nodes)
			for _, n := range hierNodes {
				if err := db.UpsertNode(ctx, n); err != nil {
					jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert hierarchy node: %w", err))
					return
				}
			}
			if err := db.UpsertEdges(ctx, hierEdges); err != nil {
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert hierarchy edges: %w", err))
				return
			}
			nodes = append(nodes, hierNodes...)
			auto, proposals := onboard.Tier2ScoreAndLink(ctx, db, projectID, nodes)
			jobs.finish(job.ID, len(nodes), len(edges), chunkCount, nil, len(convNodes), auto, proposals)
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"job_id":%q,"status":"running"}`, job.ID)
	}
}

// handleIngestJobStatus polls an async ingest job by ID.
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
