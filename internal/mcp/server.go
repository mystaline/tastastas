// Package mcp wires tastastas store and ingest packages to MCP tool
// definitions exposed over stdio (embedded mode) or HTTP (server mode).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

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
	"github.com/mystaline-dev/tastastas/internal/retrieve"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// goLanguage is lazily initialized — tree-sitter grammars require CGo
// and their Language() returns unsafe.Pointer from C.
var goLanguage = sync.OnceValue(func() *sitter.Language {
	return sitter.NewLanguage(golang.Language())
})

var tsLanguage = sync.OnceValue(func() *sitter.Language {
	return sitter.NewLanguage(ts.LanguageTypescript())
})

// NewServer creates an MCP server with all tastastas tools registered.
// The store and embedder are injected by the caller (cmd/tastastas/main.go)
// — no lazy init, no import cycle, clean dependency injection. Pass a nil
// embedder to run in lexical-only mode (no embedding-dependent features).
func NewServer(db store.Store, embedder embed.EmbedderBackend) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "tastastas",
		Version: "0.1.0",
	}, nil)
	registerTools(srv, db, embedder)
	return srv
}

// registerTools adds all 7 MCP tools to the server.
func registerTools(srv *mcp.Server, db store.Store, embedder embed.EmbedderBackend) {
	retriever := retrieve.New(db, retrieve.DefaultConfig())
	extractor := extract.New(extract.Config{})
	// Tool 1: remember
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "remember",
		Description: "Store or update a fact/entity in memory. Computes content hash automatically. If an embedder is configured, embeds content for vector search; if not, stores without embedding (degrades gracefully).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RememberInput) (*mcp.CallToolResult, RememberOutput, error) {
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
			ID:            id,
			NodeType:      nodeType,
			Title:         args.Title,
			Content:       args.Content,
			ProjectID:     projectID,
			Importance:    importance,
			SourceAdapter: "mcp",
		}
		if err := db.UpsertNode(ctx, n); err != nil {
			return errorResult(err), RememberOutput{}, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"id":"%s","status":"stored"}`, id)}},
		}, RememberOutput{ID: id, Status: "stored"}, nil
	})

	// Tool 2: recall (Phase 5/6: real scoring with recency + graph pull-in,
	// now with Phase B vector fusion when embedding is available).
	mcp.AddTool(srv, &mcp.Tool{
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

		// If embedder is available, embed the query for vector fusion.
		if embedder != nil {
			vec, err := embedder.Embed(ctx, args.Query)
			if err == nil {
				params.Embedding = vec
			}
			// If embedding fails, proceed lexical-only — graceful degradation.
		}

		result, err := retriever.Recall(ctx, params)
		if err != nil {
			return errorResult(err), RecallOutput{}, nil
		}

		items := make([]RecallItem, 0, len(result.Nodes))
		for _, s := range result.Nodes {
			items = append(items, RecallItem{
				ID:        s.ID,
				Title:     s.Title,
				Content:   s.Content,
				NodeType:  s.NodeType,
				Score:     s.Score,
				MatchType: s.MatchType,
			})
		}

		links := make([]ImplicitMCPLink, 0, len(result.Links))
		for _, l := range result.Links {
			links = append(links, ImplicitMCPLink{
				FromChunkID: l.FromChunkID,
				ToChunkID:   l.ToChunkID,
				Cosine:      l.Cosine,
			})
		}
		_ = result.Chunks // TODO: expose in RecallOutput when needed

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(RecallOutput{Results: items, Links: links})}},
		}, RecallOutput{Results: items, Links: links}, nil
	})

	// Tool 3: forget
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "forget",
		Description: "Delete a node from memory by ID.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ForgetInput) (*mcp.CallToolResult, ForgetOutput, error) {
		err := db.DeleteNode(ctx, args.ID)
		if errors.Is(err, store.ErrNotFound) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: `{"status":"not_found"}`}},
			}, ForgetOutput{Status: "not_found"}, nil
		}
		if err != nil {
			return errorResult(err), ForgetOutput{}, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: `{"status":"deleted"}`}},
		}, ForgetOutput{Status: "deleted"}, nil
	})

	// Tool 4: link
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link",
		Description: "Create a typed, directed edge between two nodes.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LinkInput) (*mcp.CallToolResult, LinkOutput, error) {
		confidence := args.Confidence
		if confidence == 0 {
			confidence = 1.0
		}

		e := store.Edge{
			FromID:     args.FromID,
			ToID:       args.ToID,
			EdgeType:   args.EdgeType,
			Confidence: confidence,
		}
		if err := db.UpsertEdge(ctx, e); err != nil {
			return errorResult(err), LinkOutput{}, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: `{"status":"linked"}`}},
		}, LinkOutput{Status: "linked"}, nil
	})

	// Tool 5: ingest
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ingest",
		Description: "Ingest documents from a filesystem root using a named adapter (docwalk, gitrepo, obsidian).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args IngestInput) (*mcp.CallToolResult, IngestOutput, error) {
		nodes, edges, err := runIngestAdapter(args.Adapter, args.Root, args.ConfigPath, args.ProjectID)
		if err != nil {
			return errorResult(err), IngestOutput{}, nil
		}

		for i := range nodes {
			if err := db.UpsertNode(ctx, nodes[i]); err != nil {
				return errorResult(err), IngestOutput{}, nil
			}
		}
		for i := range edges {
			if err := db.UpsertEdge(ctx, edges[i]); err != nil {
				return errorResult(err), IngestOutput{}, nil
			}
		}

		// Chunk and embed ingested nodes if embedder is available.
		// Batched in groups of 32 to amortize HTTP overhead per Ollama call.
		chunkCount := 0
		if embedder != nil {
			var allChunks []store.Chunk
			goLang := goLanguage()
			tsLang := tsLanguage()
			for _, n := range nodes {
				allChunks = append(allChunks, chunkForNode(n, chunker.DefaultConfig(), goLang, tsLang)...)
			}

			const batchSize = 32
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
					return errorResult(fmt.Errorf("embed batch %d-%d: %w", i, end, err)), IngestOutput{}, nil
				}
				for j := range batch {
					allChunks[i+j].Embedding = vecs[j]
				}
			}

			if len(allChunks) > 0 {
				if err := db.UpsertChunks(ctx, allChunks); err != nil {
					return errorResult(err), IngestOutput{}, nil
				}
				chunkCount = len(allChunks)
			}
		}

		out := IngestOutput{NodesIngested: len(nodes), EdgesCreated: len(edges), ChunksCreated: chunkCount}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 6: check_impact
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "check_impact",
		Description: "After updating a node, check which downstream nodes are affected. Returns list of nodes flagged stale by content change.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args CheckImpactInput) (*mcp.CallToolResult, CheckImpactOutput, error) {
		maxDepth := args.MaxDepth
		if maxDepth == 0 {
			maxDepth = 2
		}
		stale, err := db.MarkStaleDownstream(ctx, args.ID, maxDepth)
		if err != nil {
			return errorResult(err), CheckImpactOutput{}, nil
		}
		items := make([]StaleNode, 0, len(stale))
		for _, n := range stale {
			items = append(items, StaleNode{ID: n.ID, NodeType: n.NodeType})
		}
		out := CheckImpactOutput{StaleNodes: items}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 7: extract_and_remember (Phase 4/6: extraction + dedupe pipeline).
	// Distinct from `remember`: takes raw conversation text, runs it through
	// the LLM extraction pipeline, dedupe-checks each fact against existing
	// nodes of the same kind in the project (cosine similarity via
	// SearchVector), merges above threshold instead of duplicate-inserting.
	// `remember`'s direct-insert contract is untouched by this tool.
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "extract_and_remember",
		Description: "Extract atomic facts/entities from raw conversation text via LLM, dedupe-check each against existing memory, and store (merge on near-duplicate, insert otherwise).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ExtractAndRememberInput) (*mcp.CallToolResult, ExtractAndRememberOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}

		facts, err := extractor.Extract(ctx, args.Conversation)
		if err != nil {
			return errorResult(err), ExtractAndRememberOutput{}, nil
		}

		results := make([]ExtractedFactResult, 0, len(facts))
		for _, f := range facts {
			vec, err := embedder.Embed(ctx, f.Content)
			if err != nil {
				return errorResult(err), ExtractAndRememberOutput{}, nil
			}

			// Cosine-search existing nodes of the same kind in this project.
			// store.SearchVector's Score is exactly cosine similarity
			// (sqlite backend: Score = 1 - vec_distance_cosine, and cosine
			// distance IS 1 - cosine similarity) — so comparing it directly
			// against dedupe.DefaultThreshold is equivalent to running
			// dedupe.CosineSimilarity ourselves, without a redundant round
			// trip to re-fetch raw vectors the store doesn't expose back.
			candidates, err := db.SearchVector(ctx, projectID, vec, 5)
			if err != nil {
				return errorResult(err), ExtractAndRememberOutput{}, nil
			}

			id := ""
			status := "created"
			bestID, bestScore := "", -1.0
			for _, c := range candidates {
				if c.NodeType != f.Kind {
					continue
				}
				if c.Score > bestScore {
					bestID, bestScore = c.ID, c.Score
				}
			}
			if bestID != "" && bestScore >= dedupe.DefaultThreshold {
				id = bestID
				status = "merged"
			} else {
				id = fmt.Sprintf("%s/%s/%s", projectID, f.Kind, genULID())
			}

			n := store.Node{
				ID:            id,
				ProjectID:     projectID,
				NodeType:      f.Kind,
				Title:         f.Title,
				Content:       f.Content,
				Importance:    f.Importance,
				SourceAdapter: "extract_and_remember",
				Embedding:     vec,
			}
			if err := db.UpsertNode(ctx, n); err != nil {
				return errorResult(err), ExtractAndRememberOutput{}, nil
			}
			results = append(results, ExtractedFactResult{ID: id, Status: status})
		}

		out := ExtractAndRememberOutput{Facts: results}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})
}

// runIngestAdapter dispatches to the named ingest adapter and returns the
// resulting nodes/edges (gitrepo returns no edges; obsidian/docwalk do).
func runIngestAdapter(adapter, root, configPath, projectID string) ([]store.Node, []store.Edge, error) {
	switch adapter {
	case "docwalk":
		var cfg docwalk.Config
		var err error
		if configPath != "" {
			cfg, err = docwalk.LoadConfig(configPath)
			if err != nil {
				return nil, nil, err
			}
		}
		if projectID != "" {
			cfg.ProjectID = projectID
		}
		return docwalk.Ingest(root, cfg)

	case "gitrepo":
		nodes, err := gitrepo.Ingest(gitrepo.Config{Root: root, ProjectID: projectID})
		return nodes, nil, err

	case "obsidian":
		nodes, edges, err := obsidian.Ingest(obsidian.Config{Root: root, ProjectID: projectID})
		return nodes, edges, err

	default:
		return nil, nil, fmt.Errorf("adapter %q not implemented (must be one of: docwalk, gitrepo, obsidian)", adapter)
	}
}

// --- helpers (types in types.go) ---

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf(`{"error":"%s"}`, strings.ReplaceAll(err.Error(), `"`, `\"`))},
		},
		IsError: true,
	}
}

func marshalJSON(v any) string {
	// Use encoding/json for deterministic output — no fmt.Sprintf on structs.
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %s"}`, err)
	}
	return string(b)
}
