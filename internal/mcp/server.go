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
	"path/filepath"
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
	"github.com/mystaline-dev/tastastas/internal/onboard"
	"github.com/mystaline-dev/tastastas/internal/retrieve"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// Version is set at build time via -ldflags. Falls back to "dev".
var Version = "dev"

var goLanguage = sync.OnceValue(func() *sitter.Language {
	return sitter.NewLanguage(golang.Language())
})
var tsLanguage = sync.OnceValue(func() *sitter.Language {
	return sitter.NewLanguage(ts.LanguageTypescript())
})

// chunkAndEmbedNodes splits nodes into chunks, embeds any missing vectors,
// and bulk-persists everything. progress reports (embedded, total) after
// each embed batch; onPersisting fires once, right before the bulk DB
// insert starts — that insert has no progress callback of its own (one
// transaction, N sequential single-row INSERTs), so without this signal a
// multi-minute persist phase on a large project is indistinguishable from
// a hang to anyone polling job status. Both callbacks may be nil.
func chunkAndEmbedNodes(
	ctx context.Context,
	db store.Store,
	embedder embed.EmbedderBackend,
	nodes []store.Node,
	progress func(embedded, total int),
	onPersisting func(),
) (int, error) {
	if embedder == nil {
		return 0, nil
	}

	// Collect chunks: unchanged nodes keep existing chunks (hash match);
	// changed or new nodes get re-chunked. Delta-only — no full delete+re-insert.
	var allChunks []store.Chunk
	goLang := goLanguage()
	tsLang := tsLanguage()
	var chunkable []store.Node
	for _, n := range nodes {
		if n.ContentHash == "" {
			chunkable = append(chunkable, n)
			continue
		}
		existing, err := db.GetNode(ctx, n.ID)
		if err == nil && existing.ContentHash == n.ContentHash {
			// Same content — keep existing chunks and their embeddings.
			chunks, cerr := db.GetChunksByParent(ctx, n.ID, 1000, 0)
			if cerr == nil && len(chunks) > 0 {
				allChunks = append(allChunks, chunks...)
				continue
			}
		}
		// Content changed (or first time) — delete stale chunks, re-chunk.
		_ = db.DeleteChunksByParent(ctx, n.ID)
		chunkable = append(chunkable, n)
	}
	if len(chunkable) > 0 {
		type cr struct {
			idx int
			c   []store.Chunk
		}
		resCh := make(chan cr, len(chunkable))
		for w := 0; w < 4; w++ {
			go func(worker int) {
				cfg := chunker.DefaultConfig()
				for j := worker; j < len(chunkable); j += 4 {
					resCh <- cr{idx: j, c: chunkForNode(chunkable[j], cfg, goLang, tsLang)}
				}
			}(w)
		}
		for range len(chunkable) {
			r := <-resCh
			if len(r.c) > 0 {
				allChunks = append(allChunks, r.c...)
			}
		}
	}
	if len(allChunks) == 0 {
		return 0, nil
	}

	// Only embed chunks without existing vectors — unchanged nodes keep
	// their existing vectors, zero re-computation.
	var needEmbed []store.Chunk
	var needEmbedIdx []int
	for i, c := range allChunks {
		if len(c.Embedding) == 0 {
			needEmbed = append(needEmbed, c)
			needEmbedIdx = append(needEmbedIdx, i)
		}
	}
	if len(needEmbed) == 0 {
		return len(allChunks), nil
	}
	const batchSize = 32 // ponytail: 64 for sidecar (ONNX CPU, padding-insensitive), 32 for ollama safer. Both work.
	for i := 0; i < len(needEmbed); i += batchSize {
		end := i + batchSize
		if end > len(needEmbed) {
			end = len(needEmbed)
		}
		batch := needEmbed[i:end]
		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Content
		}
		vecs, err := embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return 0, fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}
		for j := range batch {
			allChunks[needEmbedIdx[i+j]].Embedding = vecs[j]
		}
		if progress != nil {
			progress(end, len(needEmbed))
		}
	}
	if onPersisting != nil {
		onPersisting()
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

func NewServer(db store.Store, embedder embed.EmbedderBackend) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "tastastas",
			Version: Version,
		},
		nil,
	)

	registerTools(srv, db, embedder)
	return srv
}

func registerTools(srv *mcp.Server, db store.Store, embedder embed.EmbedderBackend) {
	retriever := retrieve.New(db, retrieve.DefaultConfig())
	extractor := extract.New(extract.Config{})
	jobs := newJobStore(db)

	// Tool 1: init
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "init",
		Description: "Initialize tastastas and get capability overview for your session.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, InitOutput, error) {
		help := `Tastastas Memory Backend:
- Typed graph + vector + lexical hybrid.
- Tools: remember (store), recall (search), link (connect nodes), query_graph (trace), ingest (walk codebase).
- Rules:
  1. Always 'init' first.
  2. Use 'recall' for initial search; if results are large, use 'recall_chunks' to fetch full paginated content (saves context).
  3. Prefer 'link' (create edges) over 'remember' (store text or quick memorable notes) for complex relationships (ERD, PRD, API spec).
  4. 'recall' returns ranked structural edges + inferred links; use 'query_graph' to inspect proposals.
  5. Ingest is idempotent; run on every push.`
		output := InitOutput{Help: help}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: help}}}, output, nil
	})

	// Tool 2: onboard — async
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
				// Report walk counts and transition phase to "embedding" — same as ingest path.
				jobs.updateCounts(job.ID, result.FilesWalked, result.FilesSkipped)

				// Chunk + embed for RAG-level recall.
				chunkCount, err := chunkAndEmbedNodes(
					context.Background(), db, embedder, result.AllNodes,
					func(embedded, total int) { jobs.updateChunksEmbedded(job.ID, embedded, total) },
					func() { jobs.updatePhase(job.ID, "persisting") },
				)
				if err != nil {
					return fmt.Errorf("chunk/embed: %w", err)
				}
				_ = chunkCount
				return nil
			}())
			// Report walk counts and transition phase to "embedding" — same as ingest path.
		})
		// Report walk counts and transition phase to "embedding" — same as ingest path.

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

	// Tool 3: onboard_check
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
		if etc, err := db.EdgeTypeCounts(ctx, projectID); err == nil && len(etc) > 0 {
			output.EdgeTypeCounts = etc
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

	// Tool 4: remember
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
				[]store.Node{n}, nil, nil,
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

	// Tool 5: recall
	mcp.AddTool(
		srv,
		&mcp.Tool{
			Name:        "recall",
			Description: "Search memory by query (FTS5 lexical + optional vector + RRF fusion + graph neighbors). Returns scored nodes with excerpt, first 3 chunk previews, and pagination metadata. Use recall_chunks to fetch more chunks when more_available is true.",
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
				ProjectID:     projectID,
				Query:         args.Query,
				Limit:         limit,
				LinkThreshold: args.LinkThreshold,
			}
	if embedder != nil {
		embedCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if vec, err := embedder.Embed(embedCtx, args.Query); err == nil {
			params.Embedding = vec
		}
	}

			result, err := retriever.Recall(ctx, params)
			if err != nil {
				return errorResult(err), RecallOutput{}, nil
			}

			items := make([]RecallItem, 0, len(result.Nodes))
			for _, s := range result.Nodes {
				edges := make([]RecallEdge, 0, len(s.Edges))
				for _, e := range s.Edges {
					edges = append(edges, RecallEdge{
						ToID: e.ToID, ToTitle: e.ToTitle,
						EdgeType: e.EdgeType, Confidence: e.Confidence,
					})
				}
				inferredEdges := make([]RecallEdge, 0, len(s.InferredEdges))
				for _, e := range s.InferredEdges {
					inferredEdges = append(inferredEdges, RecallEdge{
						ToID: e.ToID, ToTitle: e.ToTitle,
						EdgeType: e.EdgeType, Confidence: e.Confidence,
					})
				}
				items = append(items, RecallItem{
					ID: s.ID, Title: s.Title, Excerpt: s.Excerpt,
					NodeType: s.NodeType, Score: s.Score, MatchType: s.MatchType,
					PreviewChunks: s.PreviewChunks, TotalChunks: s.TotalChunks,
					MoreAvailable: s.MoreAvailable, NextChunkStart: s.NextChunkStart,
					Edges: edges, InferredEdges: inferredEdges,
				})
			}

			links := make([]ImplicitMCPLink, 0, len(result.Links))
			for _, l := range result.Links {
				links = append(links,
					ImplicitMCPLink{FromChunkID: l.FromChunkID, ToChunkID: l.ToChunkID, Cosine: l.Cosine},
				)
			}
			recallOut := RecallOutput{Results: items, Links: links}

			toolResult := &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: marshalJSON(recallOut),
					},
				},
			}

			output := recallOut

			return toolResult, output, nil

		})

	// Tool 6: recall_chunks
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "recall_chunks",
		Description: "Fetch more chunks of a node returned by recall. Use when a recall result shows more_available=true. Pass the node id as parent_node_id with a chunk range (default 3 per page). chunk_end is exclusive (0-indexed): chunk_end=total_chunks fetches the remainder in one call.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RecallChunksInput) (*mcp.CallToolResult, RecallChunksOutput, error) {
		if args.ParentNodeID == "" {
			return errorResult(fmt.Errorf("parent_node_id is required")), RecallChunksOutput{}, nil
		}
		chunkStart := args.ChunkStart
		if chunkStart < 0 {
			chunkStart = 0
		}
		chunkEnd := args.ChunkEnd
		if chunkEnd <= chunkStart {
			chunkEnd = chunkStart + 3
		}
		parent, err := db.GetNode(ctx, args.ParentNodeID)
		if err != nil {
			return errorResult(fmt.Errorf("parent node not found: %w", err)), RecallChunksOutput{}, nil
		}
		total, err := db.CountChunksByParent(ctx, args.ParentNodeID)
		if err != nil {
			return errorResult(err), RecallChunksOutput{}, nil
		}
		if chunkEnd > total {
			chunkEnd = total
		}
		limit := chunkEnd - chunkStart
		if limit <= 0 {
			out := RecallChunksOutput{
				ParentNodeID:   args.ParentNodeID,
				ParentTitle:    parent.Title,
				TotalChunks:    total,
				ReturnedRange:  "none",
				MoreAvailable:  false,
				NextChunkStart: -1,
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}}}, out, nil
		}
		chunks, err := db.GetChunksByParent(ctx, args.ParentNodeID, limit, chunkStart)
		if err != nil {
			return errorResult(err), RecallChunksOutput{}, nil
		}
		items := make([]ChunkOutputItem, 0, len(chunks))
		for _, c := range chunks {
			items = append(items, ChunkOutputItem{
				ID: c.ID, ParentNodeID: c.ParentNodeID, ChunkIndex: c.ChunkIndex,
				Type: c.Type, HeadingPath: c.HeadingPath, Content: c.Content,
				Language: c.Language,
			})
		}
		more := chunkEnd < total
		next := chunkEnd
		if !more {
			next = -1
		}
		out := RecallChunksOutput{
			ParentNodeID: args.ParentNodeID, ParentTitle: parent.Title,
			TotalChunks: total, ReturnedRange: fmt.Sprintf("chunk %d-%d of %d", chunkStart, chunkEnd-1, total),
			Chunks: items, MoreAvailable: more, NextChunkStart: next,
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}}}, out, nil
	})

	// Tool 7: forget
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

	// Tool 8: link
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

	// Tool 9: ingest — async, auto-detect adapters
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ingest",
		Description: "Ingest a project directory into memory. Auto-detects adapters (codeast, docwalk, gitrepo, obsidian, markdown-glob), walks files, chunks, embeds, and returns a job_id for polling via job_status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args IngestInput) (*mcp.CallToolResult, IngestOutput, error) {
		projectID := args.ProjectID
		cwd := args.CWD
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return errorResult(err), IngestOutput{}, nil
			}
		}

		if projectID == "" {
			// Fallback to .memoryrc.yaml project_id if available
			cfg, err := docwalk.LoadConfig(filepath.Join(cwd, ".memoryrc.yaml"))
			if err == nil && cfg.ProjectID != "" {
				projectID = cfg.ProjectID
			} else {
				projectID = "default"
			}
		}

		job := jobs.create()
		safeGo(func() {
			nodes, edges, _, filesWalked, filesSkipped, err := onboard.AutoDetectAdapters(
				context.Background(),
				db,
				cwd,
				projectID,
			)
			if err != nil {
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("detect adapters: %w", err))
				return
			}
			jobs.updateCounts(job.ID, filesWalked, filesSkipped)
			for _, n := range nodes {
				if err := db.UpsertNode(context.Background(), n); err != nil {
					jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert node: %w", err))
					return
				}
			}
			for _, e := range edges {
				if err := db.UpsertEdge(context.Background(), e); err != nil {
					jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert edge: %w", err))
					return
				}
			}
			chunkCount, err := chunkAndEmbedNodes(
				context.Background(), db, embedder, nodes,
				func(embedded, total int) { jobs.updateChunksEmbedded(job.ID, embedded, total) },
				func() { jobs.updatePhase(job.ID, "persisting") },
			)
			if err != nil {
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("chunk/embed: %w", err))
				return
			}
			if err := onboard.EmbedNodes(context.Background(), db, nodes, embedder); err != nil {
				jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("embed nodes: %w", err))
				return
			}
			convNodes := onboard.InferConventions(context.Background(), db, projectID, nodes)
			for _, cn := range convNodes {
				_ = db.UpsertNode(context.Background(), cn)
			}
			nodes = append(nodes, convNodes...)

			hierNodes, hierEdges := onboard.BuildHierarchy(projectID, nodes)
			for _, n := range hierNodes {
				if err := db.UpsertNode(context.Background(), n); err != nil {
					jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert hierarchy node: %w", err))
					return
				}
			}
			for _, e := range hierEdges {
				if err := db.UpsertEdge(context.Background(), e); err != nil {
					jobs.finish(job.ID, 0, 0, 0, fmt.Errorf("upsert hierarchy edge: %w", err))
					return
				}
			}
			nodes = append(nodes, hierNodes...)

			auto, proposals := onboard.Tier2ScoreAndLink(context.Background(), db, projectID, nodes)
			jobs.finish(job.ID, len(nodes), len(edges), chunkCount, nil, len(convNodes), auto, proposals)
		})

		output := IngestOutput{JobID: job.ID, Status: "running"}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(output)}},
		}, output, nil
	})

	// Tool 10: check_impact
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

	// Tool 11: extract_and_remember — async
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

				chunkAndEmbedNodes(context.Background(), db, embedder, storedNodes, nil, nil)
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

	// Tool 12: query_graph - synchronous
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

	// Tool 13: project_graph - synchronous, macro-level visualization data
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
			edgeTypes = []string{
				"specifies",
				"implements",
				"tests",
				"calls",
				"defines",
				"imports",
				"convention-member",
				"auto-linked",
				"references",
			}
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

	// Tool 14: job_status — poll any async job
	mcp.AddTool(srv, &mcp.Tool{
		Name: "job_status", Description: "Poll status of an async job (onboard, extract_and_remember). Returns current state.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args JobStatusInput) (*mcp.CallToolResult, JobStatusOutput, error) {
		j, ok := jobs.get(args.JobID)
		if !ok {
			return errorResult(fmt.Errorf("job %s not found", args.JobID)), JobStatusOutput{}, nil
		}
		output := JobStatusOutput{
			ID:              j.ID,
			Status:          j.Status,
			Phase:           j.Phase,
			Nodes:           j.Nodes,
			Edges:           j.Edges,
			Chunks:          j.Chunks,
			ChunksTotal:     j.ChunksTotal,
			Conventions:     j.Conventions,
			AutoLinked:      j.AutoLinked,
			ProposalsQueued: j.ProposalsQueued,
			Error:           j.Error,
			StartedAt:       j.StartedAt.Format(time.RFC3339),
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

// resolveNodeMeta fetches a node's title and type. Errors return empty strings.
func resolveNodeMeta(ctx context.Context, db store.Store, id string) (title, nodeType string) {
	n, err := db.GetNode(ctx, id)
	if err != nil {
		title := store.DisplayName(id)
		nt := "code:function"
		if strings.Contains(id, "/code:type/") {
			nt = "code:type"
		} else if strings.Contains(id, "/code:package/") {
			nt = "code:package"
		}
		return title, nt
	}
	title = n.Title
	nt := n.NodeType
	if nt == "" {
		nt = "code:function"
		if strings.Contains(id, "/code:type/") {
			nt = "code:type"
		} else if strings.Contains(id, "/code:package/") {
			nt = "code:package"
		}
	}
	return title, nt
}

// sortOutgoingFirst sorts edge results so outgoing edges come first,
// then by confidence descending within each direction.
func sortOutgoingFirst(results []EdgeResult) {
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			swap := false
			if results[i].Direction != "outgoing" && results[j].Direction == "outgoing" {
				swap = true
			} else if results[i].Direction == results[j].Direction && results[j].Confidence > results[i].Confidence {
				swap = true
			}
			if swap {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
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
