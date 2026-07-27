// Package mcp wires tastastas store and ingest packages to MCP tool
// definitions exposed over stdio (embedded mode) or HTTP (server mode).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	ts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mystaline-dev/tastastas/internal/chunker"
	"github.com/mystaline-dev/tastastas/internal/dedupe"
	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/extract"
	"github.com/mystaline-dev/tastastas/internal/ingest/docwalk"
	"github.com/mystaline-dev/tastastas/internal/ingest/gitrepo"
	"github.com/mystaline-dev/tastastas/internal/ingest/obsidian"
	"github.com/mystaline-dev/tastastas/internal/llm"
	"github.com/mystaline-dev/tastastas/internal/onboard"
	"github.com/mystaline-dev/tastastas/internal/retrieve"
	"github.com/mystaline-dev/tastastas/internal/store"
)

var goLanguage = sync.OnceValue(func() *sitter.Language {
	return sitter.NewLanguage(golang.Language())
})
var tsLanguage = sync.OnceValue(func() *sitter.Language {
	return sitter.NewLanguage(ts.LanguageTypescript())
})

func chunkAndEmbedNodes(
	ctx context.Context,
	db store.Store,
	embedder embed.EmbedderBackend,
	nodes []store.Node,
	progress func(int),
) (int, error) {
	if embedder == nil {
		return 0, nil
	}
	var allChunks []store.Chunk
	goLang := goLanguage()
	tsLang := tsLanguage()
	for _, n := range nodes {
		allChunks = append(allChunks, chunkForNode(n, chunker.DefaultConfig(), goLang, tsLang)...)
	}
	if len(allChunks) == 0 {
		return 0, nil
	}
	const batchSize = 32
	embedded := 0
	for i := 0; i < len(allChunks); i += batchSize {
		end := i + batchSize
		if end > len(allChunks) {
			end = len(allChunks)
		}
		batch := allChunks[i:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Content
		}
		vecs, err := embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return 0, fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}
		for j := range batch {
			allChunks[i+j].Embedding = vecs[j]
		}
		embedded = end
		if progress != nil {
			progress(embedded)
		}
	}
	if err := db.UpsertChunks(ctx, allChunks); err != nil {
		return 0, err
	}
	return len(allChunks), nil
}

// safeGo runs fn in a goroutine with panic recovery. Logs panic + stack trace.
func safeGo(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

func NewServer(db store.Store, embedder embed.EmbedderBackend, llmClient llm.Client) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "tastastas",
			Version: "0.1.0",
		},
		nil,
	)

	registerTools(srv, db, embedder, llmClient)
	return srv
}

func registerTools(srv *mcp.Server, db store.Store, embedder embed.EmbedderBackend, llmClient llm.Client) {
	retriever := retrieve.New(db, retrieve.DefaultConfig())
	extractor := extract.New(extract.Config{})
	jobs := newJobStore()

	// Tool 1: remember
	mcp.AddTool(
		srv,
		&mcp.Tool{
			Name:        "remember",
			Description: "Store or update a fact/entity in memory. Computes content hash automatically. If an embedder is configured, embeds content for vector search; if not, stores without embedding (degrades gracefully).",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args RememberInput) (*mcp.CallToolResult, RememberOutput, error) {
			projectID := args.ProjectID
			if projectID == "" {
				projectID = "default"
			}
			nodeType := args.NodeType
			if nodeType == "" {
				nodeType = "generic-doc"
			}
			id := args.ID
			if id == "" {
				id = fmt.Sprintf("%s/fact/%s", projectID, genULID())
			}
			importance := args.Importance
			if importance == 0 {
				importance = 0.5
			}

			n := store.Node{
				ID: id, NodeType: nodeType, Title: args.Title, Content: args.Content,
				ProjectID: projectID, Importance: importance, SourceAdapter: "mcp",
			}

			if err := db.UpsertNode(ctx, n); err != nil {
				return errorResult(err), RememberOutput{}, nil
			}

			if _, err := chunkAndEmbedNodes(
				ctx,
				db,
				embedder,
				[]store.Node{n}, nil,
			); err != nil {
				return errorResult(err), RememberOutput{}, nil
			}

			toolResult := &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: fmt.Sprintf(`{"id":"%s","status":"stored"}`, id),
					},
				},
			}

			output := RememberOutput{
				ID:     id,
				Status: "stored",
			}

			return toolResult, output, nil
		},
	)

	// Tool 2: recall
	mcp.AddTool(
		srv,
		&mcp.Tool{
			Name:        "recall",
			Description: "Search memory by query. Flexes lexical (FTS5) or fused lexical+vector scoring depending on whether an embedder is configured. Returns scored results with graph-neighbor pull-in for context enrichment.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args RecallInput) (*mcp.CallToolResult, RecallOutput, error) {
			projectID := args.ProjectID
			if projectID == "" {
				projectID = "default"
			}
			limit := args.Limit
			if limit == 0 {
				limit = 10
			}
			params := retrieve.RecallParams{
				ProjectID: projectID,
				Query:     args.Query,
				Limit:     limit,
			}
			if embedder != nil {
				if vec, err := embedder.Embed(ctx, args.Query); err == nil {
					params.Embedding = vec
				}
			}

			result, err := retriever.Recall(ctx, params)
			if err != nil {
				return errorResult(err), RecallOutput{}, nil
			}

			items := make([]RecallItem, 0, len(result.Nodes))
			for _, s := range result.Nodes {
				items = append(
					items,
					RecallItem{
						ID:        s.ID,
						Title:     s.Title,
						Content:   s.Content,
						NodeType:  s.NodeType,
						Score:     s.Score,
						MatchType: s.MatchType,
					},
				)
			}

			links := make([]ImplicitMCPLink, 0, len(result.Links))
			for _, l := range result.Links {
				links = append(
					links,
					ImplicitMCPLink{FromChunkID: l.FromChunkID, ToChunkID: l.ToChunkID, Cosine: l.Cosine},
				)
			}

			toolResult := &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: marshalJSON(RecallOutput{
							Results: items,
							Links:   links,
						},
						),
					},
				},
			}

			output := RecallOutput{Results: items, Links: links}

			return toolResult, output, nil
		})

	// Tool 3: forget
	mcp.AddTool(srv, &mcp.Tool{
		Name: "forget", Description: "Delete a node from memory by ID.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ForgetInput) (*mcp.CallToolResult, ForgetOutput, error) {
		err := db.DeleteNode(ctx, args.ID)
		if errors.Is(err, store.ErrNotFound) {
			return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: `{"status":"not_found"}`}},
				}, ForgetOutput{
					Status: "not_found",
				}, nil
		}
		if err != nil {
			return errorResult(err), ForgetOutput{}, nil
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: `{"status":"deleted"}`},
			},
		}

		output := ForgetOutput{
			Status: "deleted",
		}

		return toolResult, output, nil
	})

	// Tool 4: link
	mcp.AddTool(srv, &mcp.Tool{
		Name: "link", Description: "Create a typed, directed edge between two nodes.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LinkInput) (*mcp.CallToolResult, LinkOutput, error) {
		if args.Confidence == 0 {
			args.Confidence = 1.0
		}

		if err := db.UpsertEdge(
			ctx,
			store.Edge{
				FromID:     args.FromID,
				ToID:       args.ToID,
				EdgeType:   args.EdgeType,
				Confidence: args.Confidence,
			},
		); err != nil {
			return errorResult(err), LinkOutput{}, nil
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: `{"status":"linked"}`}},
		}

		output := LinkOutput{
			Status: "linked",
		}

		return toolResult, output, nil
	})

	// Tool 5: ingest
	mcp.AddTool(srv, &mcp.Tool{
		Name: "ingest", Description: "Ingest documents from a filesystem root using a named adapter (docwalk, gitrepo, obsidian).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args IngestInput) (*mcp.CallToolResult, IngestOutput, error) {
		nodes, edges, _, _, err := runIngestAdapter(args.Adapter, args.Root, args.ConfigPath, args.ProjectID)
		if err != nil {
			return errorResult(err), IngestOutput{}, nil
		}
		for _, n := range nodes {
			if err := db.UpsertNode(ctx, n); err != nil {
				return errorResult(err), IngestOutput{}, nil
			}
		}
		for _, e := range edges {
			if err := db.UpsertEdge(ctx, e); err != nil {
				return errorResult(err), IngestOutput{}, nil
			}
		}

		chunkCount, err := chunkAndEmbedNodes(ctx, db, embedder, nodes, nil)
		if err != nil {
			return errorResult(err), IngestOutput{}, nil
		}

		output := IngestOutput{
			NodesIngested: len(nodes),
			EdgesCreated:  len(edges),
			ChunksCreated: chunkCount,
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: marshalJSON(output),
				},
			},
		}

		return toolResult, output, nil
	})

	// Tool 6: check_impact
	mcp.AddTool(srv, &mcp.Tool{
		Name: "check_impact", Description: "After updating a node, check which downstream nodes are affected.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args CheckImpactInput) (*mcp.CallToolResult, CheckImpactOutput, error) {
		if args.MaxDepth == 0 {
			args.MaxDepth = 2
		}

		stale, err := db.MarkStaleDownstream(ctx, args.ID, args.MaxDepth)
		if err != nil {
			return errorResult(err), CheckImpactOutput{}, nil
		}

		items := make([]StaleNode, len(stale))
		for i, n := range stale {
			items[i] = StaleNode{ID: n.ID, NodeType: n.NodeType}
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: marshalJSON(CheckImpactOutput{
						StaleNodes: items,
					},
					),
				},
			},
		}

		output := CheckImpactOutput{
			StaleNodes: items,
		}

		return toolResult, output, nil
	})

	// Tool 7: extract_and_remember — async
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "extract_and_remember",
		Description: "Extract atomic facts/entities from raw conversation text via LLM, dedupe-check each against existing memory, and store (merge on near-duplicate, insert otherwise).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ExtractAndRememberInput) (*mcp.CallToolResult, ExtractAndRememberOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}

		job := jobs.create()
		safeGo(func() {
			defer jobs.finish(job.ID, 0, 0, 0, func() error {
				facts, err := extractor.Extract(context.Background(), args.Conversation)
				if err != nil {
					return fmt.Errorf("extract: %w", err)
				}

				var storedNodes []store.Node
				for _, f := range facts {
					vec, err := embedder.Embed(context.Background(), f.Content)
					if err != nil {
						return fmt.Errorf("embed: %w", err)
					}

					candidates, err := db.SearchVector(context.Background(), projectID, vec, 5)
					if err != nil {
						log.Printf("extract_and_remember: search: %v", err)
						continue
					}

					id := fmt.Sprintf("%s/%s/%s", projectID, f.Kind, genULID())

					for _, c := range candidates {
						if c.NodeType == f.Kind && c.Score >= dedupe.DefaultThreshold {
							id = c.ID
							break
						}
					}

					node := store.Node{
						ID: id, ProjectID: projectID, NodeType: f.Kind,
						Title: f.Title, Content: f.Content, Importance: f.Importance,
						SourceAdapter: "extract_and_remember", Embedding: vec,
					}

					if err := db.UpsertNode(context.Background(), node); err != nil {
						return fmt.Errorf("upsert %s: %w", id, err)
					}
					storedNodes = append(storedNodes, node)
				}

				chunkAndEmbedNodes(context.Background(), db, embedder, storedNodes, nil)
				return nil
			}())
		})

		output := ExtractAndRememberOutput{
			Facts: []ExtractedFactResult{
				{ID: job.ID, Status: "running"},
			},
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: marshalJSON(output),
				},
			},
		}

		return toolResult, output, nil
	})

	// Tool 8: onboard — async
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "onboard",
		Description: "Onboard into a codebase. Auto-detects adapters, runs all matching, infers conventions, runs Tier 2 linking. Async — returns job_id.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args OnboardInput) (*mcp.CallToolResult, OnboardOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}

		cwd := args.CWD
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return errorResult(err), OnboardOutput{}, nil
			}
		}

		job := jobs.create()
		safeGo(func() {
			defer jobs.finish(job.ID, 0, 0, 0, func() error {
				result, err := onboard.Run(
					context.Background(),
					onboard.Config{
						CWD:       cwd,
						ProjectID: projectID,
						Scope:     args.Scope,
						Embedder:  embedder,
						Store:     db,
					},
				)
				if err != nil {
					return err
				}
				if result.AlreadyOnboarded {
					return nil
				}

				// Chunk + embed for RAG-level recall.
				_, err = chunkAndEmbedNodes(
					context.Background(),
					db,
					embedder,
					result.AllNodes,
					nil,
				)
				if err != nil {
					log.Printf("onboard: chunk+embed: %v", err)
				}

				return nil
			}())
		})

		output := OnboardOutput{ProjectID: projectID, JobID: job.ID, Status: "running"}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: marshalJSON(output),
				},
			},
		}

		return toolResult, output, nil
	})

	// Tool 9: onboard_check
	mcp.AddTool(srv, &mcp.Tool{
		Name: "onboard_check", Description: "Check graph state for a project. Read-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args OnboardCheckInput) (*mcp.CallToolResult, OnboardCheckOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}

		stats, err := db.Stats(ctx, projectID)
		if err != nil {
			return errorResult(err), OnboardCheckOutput{}, nil
		}

		output := OnboardCheckOutput{
			HasNodes:       stats.NodeCount > 0,
			HasChunks:      stats.ChunkCount > 0,
			HasEmbeddings:  stats.VecCount > 0,
			HasEdges:       stats.EdgeCount > 0,
			HasConventions: stats.ConventionCnt > 0,
			StaleCount:     stats.StaleCount,
			NodeCount:      stats.NodeCount,
			EdgeCount:      stats.EdgeCount,
			ChunkCount:     stats.ChunkCount,
			VecCount:       stats.VecCount,
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: marshalJSON(output),
				},
			},
		}

		return toolResult, output, nil
	})

	// Tool 10: query_graph - synchronous
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "query_graph",
		Description: "Query graph edges from/to a node. Returns typed relationships: who calls this function, what this doc references, etc. Use after recall to explore connections.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args QueryGraphInput) (*mcp.CallToolResult, QueryGraphOutput, error) {
		if args.NodeID == "" {
			return errorResult(fmt.Errorf("node_id is required")), QueryGraphOutput{}, nil
		}

		outgoing, incoming := true, true
		switch args.Direction {
		case "outgoing":
			incoming = false
		case "incoming":
			outgoing = false
		}

		limit := args.Limit
		if limit <= 0 {
			limit = 20
		}

		srcTitle := ""
		if src, err := db.GetNode(ctx, args.NodeID); err == nil {
			srcTitle = src.Title
		}

		var results []EdgeResult

		if outgoing {
			edges, err := db.GetEdgesFrom(ctx, args.NodeID, args.EdgeTypes)
			if err == nil {
				for _, e := range edges {
					title, ntype := resolveNodeMeta(ctx, db, e.ToID)
					results = append(results, EdgeResult{
						Direction:  "outgoing",
						NodeID:     e.ToID,
						NodeTitle:  title,
						NodeType:   ntype,
						EdgeType:   e.EdgeType,
						Confidence: e.Confidence,
					})
				}
			}
		}

		if incoming {
			edges, err := db.GetEdgesTo(ctx, args.NodeID, args.EdgeTypes)
			if err == nil {
				for _, e := range edges {
					title, ntype := resolveNodeMeta(ctx, db, e.FromID)
					results = append(results, EdgeResult{
						Direction:  "incoming",
						NodeID:     e.FromID,
						NodeTitle:  title,
						NodeType:   ntype,
						EdgeType:   e.EdgeType,
						Confidence: e.Confidence,
					})
				}
			}
		}

		sortOutgoingFirst(results)

		if len(results) > limit {
			results = results[:limit]
		}

		output := QueryGraphOutput{NodeID: args.NodeID, Title: srcTitle, Edges: results}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(output)}},
		}, output, nil
	})

	// Tool 11: project_graph - synchronous, macro-level visualization data
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "project_graph",
		Description: "Return all edges and deduplicated nodes for a project, for macro-level graph visualization. By default excludes proposed edges and caps at 5000 edges.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ProjectGraphInput) (*mcp.CallToolResult, ProjectGraphOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		maxEdges := args.MaxEdges
		if maxEdges <= 0 {
			maxEdges = 5000
		}
		edgeTypes := args.EdgeTypes

		// Default: structural + auto-linked (exclude proposed).
		if len(edgeTypes) == 0 {
			edgeTypes = []string{"specifies", "implements", "tests", "calls", "defines", "imports", "convention-member", "auto-linked", "references"}
		}

		results, total, err := db.ListEdgesByProject(ctx, projectID, edgeTypes, maxEdges, 0)
		if err != nil {
			return errorResult(err), ProjectGraphOutput{}, nil
		}

		// Deduplicate nodes from edge endpoints, count degree for weight.
		nodeMap := map[string]*GraphNode{}
		addNode := func(id, title, ntype, group string) {
			if _, ok := nodeMap[id]; !ok {
				nodeMap[id] = &GraphNode{ID: id, Title: title, Type: ntype, Group: group}
			}
			nodeMap[id].Weight++
		}
		edges := make([]GraphEdge, 0, len(results))
		for _, r := range results {
			addNode(r.FromID, r.FromTitle, r.FromType, r.FromGroup)
			addNode(r.ToID, r.ToTitle, r.ToType, r.ToGroup)
			edges = append(edges, GraphEdge{
				Source: r.FromID, Target: r.ToID,
				EdgeType: r.EdgeType, Confidence: r.Confidence,
			})
		}

		nodes := make([]GraphNode, 0, len(nodeMap))
		for _, n := range nodeMap {
			nodes = append(nodes, *n)
		}

		out := ProjectGraphOutput{
			ProjectID:  projectID,
			TotalEdges: total,
			Returned:   len(results),
			Nodes:      nodes,
			Edges:      edges,
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 11: job_status — poll any async job
	mcp.AddTool(srv, &mcp.Tool{
		Name: "job_status", Description: "Poll status of an async job (onboard, extract_and_remember, build_knowledge_graph). Returns current state.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args JobStatusInput) (*mcp.CallToolResult, JobStatusOutput, error) {
		j, ok := jobs.get(args.JobID)
		if !ok {
			return errorResult(fmt.Errorf("job %s not found", args.JobID)), JobStatusOutput{}, nil
		}
		output := JobStatusOutput{
			ID:        j.ID,
			Status:    j.Status,
			Nodes:     j.Nodes,
			Edges:     j.Edges,
			Chunks:    j.Chunks,
			Error:     j.Error,
			StartedAt: j.StartedAt.Format(time.RFC3339),
		}
		if !j.EndedAt.IsZero() {
			output.EndedAt = j.EndedAt.Format(time.RFC3339)
		}

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: marshalJSON(output),
				},
			},
		}

		return toolResult, output, nil
	})
}

func runIngestAdapter(adapter, root, configPath, projectID string) ([]store.Node, []store.Edge, int, int, error) {
	switch adapter {
	case "docwalk":
		var cfg docwalk.Config
		var err error
		if configPath != "" {
			cfg, err = docwalk.LoadConfig(configPath)
			if err != nil {
				return nil, nil, 0, 0, err
			}
		}
		if projectID != "" {
			cfg.ProjectID = projectID
		}
		return docwalk.Ingest(root, cfg)
	case "gitrepo":
		nodes, err := gitrepo.Ingest(
			gitrepo.Config{Root: root, ProjectID: projectID},
		)
		return nodes, nil, 0, 0, err
	case "obsidian":
		nodes, edges, err := obsidian.Ingest(
			obsidian.Config{Root: root, ProjectID: projectID},
		)
		return nodes, edges, 0, 0, err
	default:
		return nil, nil, 0, 0, fmt.Errorf(
			"adapter %q not implemented (must be one of: docwalk, gitrepo, obsidian)",
			adapter,
		)
	}
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: fmt.Sprintf(
					`{"error":"%s"}`,
					strings.ReplaceAll(err.Error(), `"`, `\"`),
				),
			},
		},
		IsError: true,
	}
}

func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %s"}`, err)
	}
	return string(b)
}
