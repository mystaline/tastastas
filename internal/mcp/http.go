package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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
	modelID string,
	spaDir string,
) error {
	jobs := newJobStore(db)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{
			Name:    "tastastas",
			Version: Version,
		}, nil)
		registerTools(srv, db, embedder, batchSize, modelID)
		return srv
	}, nil)

	mux := http.NewServeMux()

	// MCP endpoint — all MCP tools available via Streamable HTTP
	mux.Handle("/mcp", mcpHandler)

	// Graph data — both /graph and /api/graph serve same JSON.
	// SPA fetches from /api/graph/{project}, no reverse proxy in front to strip the prefix.
	mux.HandleFunc("GET /graph/{project}", HandleGraphData(db))
	mux.HandleFunc("GET /api/graph/{project}", HandleGraphData(db))
	mux.HandleFunc("GET /graph/{project}/", HandleGraphSPA(spaDir))

	// REST ingest — POST /ingest auto-detects adapters, same pipeline as MCP ingest tool.
	mux.HandleFunc("POST /ingest", handleRESTIngest(db, embedder, jobs, batchSize, modelID))
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

func HandleGraphData(db store.Store) http.HandlerFunc {
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
			"proposed",
			"references",
			"contains",
		}
		maxEdges := 2000
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
			weight, size            int
		}{}
		addNode := func(id, title, ntype, group string, size int) {
			if _, ok := nodeMap[id]; !ok {
				nodeMap[id] = &struct {
					id, title, ntype, group string
					weight, size            int
				}{id: id, title: title, ntype: ntype, group: group, size: size}
			}
			nodeMap[id].weight++
		}
		type graphNode struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Type      string `json:"type"`
			Group     string `json:"group"`
			Size      int    `json:"size"`
			Weight    int    `json:"weight"`
			ProjectID string `json:"project_id"`
		}
		type graphEdge struct {
			Source     string  `json:"source"`
			Target     string  `json:"target"`
			EdgeType   string  `json:"edge_type"`
			Confidence float64 `json:"confidence"`
		}

		nodes := []graphNode{}
		structuralEdges := []graphEdge{}
		proposedEdges := []graphEdge{}
		for _, r := range results {
			addNode(r.FromID, r.FromTitle, r.FromType, r.FromGroup, r.FromSize)
			addNode(r.ToID, r.ToTitle, r.ToType, r.ToGroup, r.ToSize)
			edge := graphEdge{
				Source: r.FromID, Target: r.ToID,
				EdgeType: r.EdgeType, Confidence: r.Confidence,
			}
			if r.EdgeType == "proposed" {
				proposedEdges = append(proposedEdges, edge)
			} else {
				structuralEdges = append(structuralEdges, edge)
			}
		}
		for _, n := range nodeMap {
			nodes = append(nodes, graphNode{
				ID: n.id, Title: n.title, Type: n.ntype, Group: n.group,
				Size: n.size, Weight: n.weight,
				ProjectID: projectID,
			})
		}

		data := struct {
			ProjectID       string      `json:"project_id"`
			TotalEdges      int         `json:"total_edges"`
			Returned        int         `json:"returned"`
			Nodes           []graphNode `json:"nodes"`
			StructuralEdges []graphEdge `json:"structural_edges"`
			ProposedEdges   []graphEdge `json:"proposed_edges"`
		}{
			ProjectID:       projectID,
			TotalEdges:      total,
			Returned:        len(results),
			Nodes:           nodes,
			StructuralEdges: structuralEdges,
			ProposedEdges:   proposedEdges,
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

		if r.URL.Query().Get("v") == "legacy" {
			page := strings.Replace(graphPageSource, "__DATA__", string(jsonBytes), 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(page))
			return
		}

		// Redirect to SPA entry point
		redirectURL := "/graph/" + projectID + "/"
		if r.URL.RawQuery != "" {
			redirectURL += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, redirectURL, http.StatusFound)
	}
}

func HandleGraphSPA(spaDir string) http.HandlerFunc {
	readFile := func(path string) ([]byte, error) {
		if spaDir != "" {
			return os.ReadFile(filepath.Join(spaDir, path))
		}
		return frontendDist.ReadFile("frontenddist/" + path)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("project")
		if projectID == "" {
			projectID = "default"
		}
		prefix := "/graph/" + projectID + "/"
		subpath := strings.TrimPrefix(r.URL.Path, prefix)

		// SPA built with vite base: /graph/ — assets at /graph/assets/...
		if projectID == "assets" {
			if strings.Contains(subpath, "..") {
				http.NotFound(w, r)
				return
			}
			data, err := readFile("assets/" + subpath)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeContent(w, r, subpath, time.Time{}, bytes.NewReader(data))
			return
		}

		if subpath == "" || subpath == "/" {
			w.Header().Set("Cache-Control", "no-cache")
			data, err := readFile("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
			return
		}

		if strings.HasPrefix(subpath, "assets/") && !strings.Contains(subpath, "..") {
			data, err := readFile(subpath)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeContent(w, r, subpath, time.Time{}, bytes.NewReader(data))
			return
		}

		// SPA fallback — serve index.html for client-side routing
		w.Header().Set("Cache-Control", "no-cache")
		data, err := readFile("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
	}
}

// handleRESTIngest handles POST /ingest — auto-detect adapters, same pipeline
// as the MCP ingest tool. Async: returns { job_id, status } immediately.
func handleRESTIngest(db store.Store, embedder embed.EmbedderBackend, jobs *jobStore, batchSize int, modelID string) http.HandlerFunc {
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
				ctx, db, embedder, nodes, batchSize, "",
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
				_ = onboard.EmbedNodes(ctx, db, nodes, embedder, batchSize, "")
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
