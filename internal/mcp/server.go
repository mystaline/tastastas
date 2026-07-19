// Package mcp wires tastastas store and ingest packages to MCP tool
// definitions exposed over stdio (embedded mode) or HTTP (server mode).
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mystaline-dev/tastastas/internal/ingest/docwalk"
	"github.com/mystaline-dev/tastastas/internal/store"
)

// NewServer creates an MCP server with all tastastas tools registered.
// The store is injected by the caller (cmd/tastastas/main.go) — no
// lazy init, no import cycle, clean dependency injection.
func NewServer(db store.Store) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "tastastas",
		Version: "0.1.0",
	}, nil)
	registerTools(srv, db)
	return srv
}

// registerTools adds all 5 MCP tools to the server.
func registerTools(srv *mcp.Server, db store.Store) {
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
			ID:          id,
			NodeType:    nodeType,
			Title:       args.Title,
			Content:     args.Content,
			ProjectID:   projectID,
			Importance:  importance,
			SourceAdapter: "mcp",
		}
		if err := db.UpsertNode(ctx, n); err != nil {
			return errorResult(err), RememberOutput{}, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"id":"%s","status":"stored"}`, id)}},
		}, RememberOutput{ID: id, Status: "stored"}, nil
	})

	// Tool 2: recall
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "recall",
		Description: "Search memory by lexical query. Phase 3 stub — returns FTS5 results with score=1.0. Replaced with real scoring (relevance*recency*importance + graph pull-in) in Phase 5.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args RecallInput) (*mcp.CallToolResult, RecallOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		limit := args.Limit
		if limit == 0 {
			limit = 10
		}

		results, err := db.SearchLexical(ctx, projectID, args.Query, limit)
		if err != nil {
			return errorResult(err), RecallOutput{}, nil
		}

		items := make([]RecallItem, 0, len(results))
		for _, r := range results {
			items = append(items, RecallItem{
				ID:       r.ID,
				Title:    r.Title,
				Content:  r.Content,
				NodeType: r.NodeType,
				Score:    1.0, // stub score — Phase 5 replaces this
			})
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(items)}},
		}, RecallOutput{Results: items}, nil
	})

	// Tool 3: forget
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "forget",
		Description: "Delete a node from memory by ID.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ForgetInput) (*mcp.CallToolResult, ForgetOutput, error) {
		err := db.DeleteNode(ctx, args.ID)
		if err == store.ErrNotFound {
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
		Description: "Ingest documents from a filesystem root using a named adapter. Only 'docwalk' is implemented as of Phase 3.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args IngestInput) (*mcp.CallToolResult, IngestOutput, error) {
		if args.Adapter != "docwalk" {
			return errorResult(fmt.Errorf("adapter %q not implemented (only 'docwalk' available)", args.Adapter)),
				IngestOutput{}, nil
		}

		var cfg docwalk.Config
		var err error
		if args.ConfigPath != "" {
			cfg, err = docwalk.LoadConfig(args.ConfigPath)
			if err != nil {
				return errorResult(err), IngestOutput{}, nil
			}
		}

		nodes, edges, err := docwalk.Ingest(args.Root, cfg)
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


		out := IngestOutput{NodesIngested: len(nodes), EdgesCreated: len(edges)}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})
}

// --- helpers (types in types.go) ---

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(`{"error":"%s"}`, strings.ReplaceAll(err.Error(), `"`, `\"`))}},
		IsError: true,
	}
}

func genULID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000"
	}
	return hex.EncodeToString(b[:])
}

func marshalJSON(v any) string {
	// Use encoding/json for deterministic output — no fmt.Sprintf on structs.
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal: %s"}`, err)
	}
	return string(b)
}
