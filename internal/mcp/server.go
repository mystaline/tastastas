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
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mystaline/tastastas/internal/dedupe"
	"github.com/mystaline/tastastas/internal/embed"
	"github.com/mystaline/tastastas/internal/extract"
	"github.com/mystaline/tastastas/internal/ingest/docwalk"
	"github.com/mystaline/tastastas/internal/ingest/gitrepo"
	"github.com/mystaline/tastastas/internal/ingest/obsidian"
	"github.com/mystaline/tastastas/internal/onboard"
	"github.com/mystaline/tastastas/internal/retrieve"
	"github.com/mystaline/tastastas/internal/store"
)

// Version is set at build time via -ldflags. Falls back to "dev".
var Version = "dev"

// safeGo runs fn in a goroutine with panic recovery. Logs panic + stack trace.
// Each call is tracked in jobWG so shutdown can wait for in-flight jobs.
func safeGo(fn func()) {
	jobWG.Add(1)
	go func() {
		defer jobWG.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

// resolveRef resolves the git ref for a working directory.
// If explicitRef is non-empty, returns it directly.
// If cwd is non-empty, attempts auto-detection via `git rev-parse --abbrev-ref HEAD`.
// Returns error if ref cannot be determined.
func resolveRef(cwd, explicitRef string) (string, error) {
	if explicitRef != "" {
		return explicitRef, nil
	}
	if cwd == "" {
		return "", fmt.Errorf("ref required")
	}
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not detect git ref from cwd: %w (specify ref explicitly)", err)
	}
	ref := strings.TrimSpace(string(out))
	return ref, nil
}

// normalizePath expands a leading ~/ or ~\ to the user's home directory and
// normalizes Windows backslashes to forward slashes. MCP clients pass paths
// verbatim (no shell), so tilde wouldn't resolve.
func normalizePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// remapRoot maps a host-side path to the docker-visible path using the
// HOST_WORKSPACE_DIR prefix mapping. The docker side is fixed at workspaceRoot
// (the image-declared /workspaces volume); the host side varies per machine
// and is configured via env. Unset host prefix → path returned unchanged.
// Case-insensitive comparison handles Windows drive letters (D:/Kerja ↔ d:/Kerja).
// A path outside the host prefix is an error — the client must pass a path
// under HOST_WORKSPACE_DIR.
func remapRoot(hostPath string) (string, error) {
	hostPrefix := os.Getenv("HOST_WORKSPACE_DIR")
	if hostPrefix == "" {
		return hostPath, nil
	}
	cleaned := filepath.ToSlash(filepath.Clean(hostPath))
	prefix := filepath.ToSlash(filepath.Clean(hostPrefix))
	lowerCleaned := strings.ToLower(cleaned)
	lowerPrefix := strings.ToLower(prefix)
	if strings.HasPrefix(lowerCleaned, lowerPrefix+"/") || strings.EqualFold(cleaned, prefix) {
		return filepath.ToSlash(filepath.Clean(workspaceRoot)) + cleaned[len(prefix):], nil
	}
	return "", fmt.Errorf(
		"path %q is outside HOST_WORKSPACE_DIR %q — pass only paths under the workspace dir",
		hostPath, hostPrefix,
	)
}

// walkRootPreflight fails fast when the walk root is missing or not a
// directory, instead of letting the async ingest silently walk nothing.
func walkRootPreflight(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("walk root %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("walk root %s: not a directory", path)
	}
	return nil
}

func NewServer(db store.Store, embedder embed.EmbedderBackend, batchSize int, modelID string) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "tastastas",
			Version: Version,
		},
		nil,
	)

	registerTools(srv, db, embedder, batchSize, modelID)
	return srv
}

// workspaceRoot is the canonical workspace mount point declared by the image
// (VOLUME /workspaces). File walks and future git clones target this path
// regardless of host-side origin.
const workspaceRoot = "/workspaces"

// modelWarning returns a warning string if the model's data is dirty or
// missing. Returns "" if everything is fine. Never blocks tool execution.
func modelWarning(ctx context.Context, db store.Store, projectID, modelID string) string {
	if modelID == "" {
		return ""
	}
	stats, err := db.Stats(ctx, projectID, "")
	if err != nil || stats.NodeCount == 0 {
		return ""
	}
	status, err := db.GetEmbedModelStatus(ctx, projectID, modelID)
	if err != nil {
		return ""
	}
	if status == "dirty" {
		return fmt.Sprintf(
			"data for model %q may be incomplete (previous ingest crashed). Run 'onboard' again.",
			modelID,
		)
	}
	if status == "" {
		return fmt.Sprintf("no data found for model %q. Run 'onboard' or 'ingest' first.", modelID)
	}
	return ""
}

func registerTools(srv *mcp.Server, db store.Store, embedder embed.EmbedderBackend, batchSize int, modelID string) {
	retriever := retrieve.New(db, retrieve.DefaultConfig())
	extractor := extract.New(extract.Config{})
	jobs := newJobStore(db)

	// Tool 1: init
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "init",
		Description: "Initialize tastastas and get capability overview for your session. AI should call init first, then onboard_check to see project state, then onboard/ingest if needed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, InitOutput, error) {
		help := `Tastastas Memory Backend - Typed graph + vector + lexical hybrid.

Workflow:
  1. init                    — start here.
  2. onboard_check           — check if project already has embeddings for the current model.
  3a. onboard                — gated (skips if model already clean). Async. Produces nodes + edges + chunks + vectors.
  3b. ingest cwd="..."       — ungated, always runs. Idempotent upsert. Same pipeline as onboard.
  4. job_status              — poll async job progress (phase: walking → embedding → persisting → done).
  5. recall / recall_chunks  — search memory.
  6. remember / link         — store a single fact or create edges.
  7. clear_project           — default: clears current model only. purge:true for full wipe.

Rules:
  - Always init first.
  - Prefer recall over ad-hoc searches.
  - Use link for complex relationships (ERD, PRD, API spec).
  - Ingest is idempotent; run on every push.`
		projects, _ := db.ListProjects(ctx)
		output := InitOutput{Help: help, ModelID: modelID, Projects: projects}
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
			cwd = workspaceRoot
		}
		cwd = normalizePath(cwd)
		walkCwd, err := remapRoot(cwd)
		if err != nil {
			return errorResult(err), OnboardOutput{}, nil
		}
		if err := walkRootPreflight(walkCwd); err != nil {
			return errorResult(err), OnboardOutput{}, nil
		}

		ref, refErr := resolveRef(walkCwd, args.Ref)
		if refErr != nil {
			return errorResult(refErr), OnboardOutput{}, nil
		}
		if ref != "" && ref != "HEAD" && !strings.Contains(projectID, ref) {
			log.Printf(
				"WARNING: ref %q not found in project_id %q — consider using %s-%s",
				ref,
				projectID,
				projectID,
				ref,
			)
		}

		job := jobs.create(projectID)
		runCtx, cancel := context.WithCancel(jobCtx)
		jobs.setCancel(job.ID, cancel)
		safeGo(func() {
			// Per-model AlreadyOnboarded guard.
			if modelID != "" {
				status, _ := db.GetEmbedModelStatus(ctx, projectID, modelID)
				if status == "clean" {
					jobs.finish(job.ID, 0, 0, 0, nil)
					return
				}
			}

			ingestMu.Lock()
			result, err := onboard.Run(runCtx, onboard.Config{
				CWD:       walkCwd,
				ProjectID: projectID,
				Scope:     args.Scope,
				ModelID:   modelID,
				Embedder:  embedder,
				Store:     db,
				BatchSize: batchSize,
				OnChunkProgress: func(embedded, total int) {
					jobs.updatePhase(job.ID, "embedding")
					jobs.updateChunksEmbedded(job.ID, embedded, total)
				},
				OnPersistChunks: func() { jobs.updatePhase(job.ID, "persisting") },
			})
			ingestMu.Unlock()

			if err != nil {
				jobs.finish(job.ID, 0, 0, 0, err)
				return
			}
			jobs.finish(job.ID, len(result.AllNodes), result.CallGraphEdges, result.ChunkCount, nil,
				result.ConventionsInferred, result.AutoLinked, result.ProposalsQueued)
		})
		// Report walk counts and transition phase to "embedding" — same as ingest path.

		output := OnboardOutput{ProjectID: projectID, JobID: job.ID, Status: "running", Ref: ref}

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
		Name: "onboard_check", Description: "Check graph state for a project filtered by model. Read-only. model_id optional (default: current server model). Pass empty string for all models.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args OnboardCheckInput) (*mcp.CallToolResult, OnboardCheckOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		checkModelID := args.ModelID
		if checkModelID == "" {
			checkModelID = modelID // fallback to server's current model
		}

		stats, err := db.Stats(ctx, projectID, checkModelID)
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
			Description: "Store or update a fact/entity in memory. Computes content hash automatically. If an embedder is configured, embeds content for vector search; if not, stores without embedding (degrades gracefully). Use `links` to reference existing node IDs this fact relates to — creates `references` edges visible in the graph.",
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
				ModelID: modelID,
			}

			edges := make([]store.Edge, 0, len(args.Links))
			for _, target := range args.Links {
				edges = append(edges, store.Edge{
					FromID: id, ToID: target,
					EdgeType: "references", Confidence: 1.0,
				})
			}

			if _, err := onboard.Run(ctx, onboard.Config{
				Nodes:           []store.Node{n},
				Edges:           edges,
				SkipPostProcess: true,
				ProjectID:       projectID,
				ModelID:         modelID,
				Embedder:        embedder,
				Store:           db,
				BatchSize:       batchSize,
			}); err != nil {
				if !errors.Is(err, store.ErrVectorSkipped) {
					return errorResult(err), RememberOutput{}, nil
				}
			}

			output := RememberOutput{
				ID:     id,
				Status: "stored",
			}

			toolResult := &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: marshalJSON(output),
					},
				},
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
			projectIDs := []string{}
			if len(args.ProjectIDs) > 0 {
				projectIDs = args.ProjectIDs
			} else if args.AllProjects {
				ids, err := db.ListProjectIDs(ctx)
				if err == nil {
					projectIDs = ids
				}
			}
			if len(projectIDs) == 0 {
				pid := args.ProjectID
				if pid == "" {
					pid = "default"
				}
				projectIDs = []string{pid}
			}

			limit := args.Limit
			if limit == 0 {
				limit = 10
			}

			sessionID := fmt.Sprintf("sess-%x", time.Now().UnixNano())

			// Phase 1: Run per-project searches in parallel
			type projRecall struct {
				projectID string
				items     []RecallItem
				warn      string
			}
			ch := make(chan projRecall, len(projectIDs))
			var wg sync.WaitGroup

			for _, projectID := range projectIDs {
				wg.Add(1)
				go func(pid string) {
					defer wg.Done()
					w := modelWarning(ctx, db, pid, modelID)

					params := retrieve.RecallParams{
						ProjectID:     pid,
						Query:         args.Query,
						ModelID:       modelID,
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
						ch <- projRecall{projectID: pid, warn: w}
						return
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

					// Log access for each result node (P3a)
					for _, s := range result.Nodes {
						_ = db.LogAccess(ctx, pid, s.ID, sessionID)
					}

					ch <- projRecall{projectID: pid, items: items, warn: w}
				}(projectID)
			}

			wg.Wait()
			close(ch)

			// Phase 2: RRF fuse across projects
			var allProjectResults [][]RecallItem
			var warn string
			for r := range ch {
				if r.warn != "" {
					warn = r.warn
				}
				if len(r.items) > 0 {
					allProjectResults = append(allProjectResults, r.items)
				}
			}

			const rrfK = 60.0
			rrfScores := map[string]float64{}
			itemMap := map[string]RecallItem{}
			for _, items := range allProjectResults {
				for rank, item := range items {
					rrfScores[item.ID] += 1.0 / (rrfK + float64(rank))
					itemMap[item.ID] = item
				}
			}

			type scoredID struct {
				id    string
				score float64
			}
			sorted := make([]scoredID, 0, len(rrfScores))
			for id, s := range rrfScores {
				sorted = append(sorted, scoredID{id: id, score: s})
			}
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].score > sorted[j].score
			})

			n := limit
			if len(sorted) < n {
				n = len(sorted)
			}
			allItems := make([]RecallItem, n)
			for i, si := range sorted[:n] {
				allItems[i] = itemMap[si.id]
			}

			// Fuzzy match: if no results and a single project_id was queried, suggest alternatives.
			if len(allItems) == 0 && len(args.ProjectIDs) == 0 && args.ProjectID != "" {
				projects, _ := db.ListProjects(ctx)
				if suggestions := fuzzyMatchProjects(args.ProjectID, projects); len(suggestions) > 0 {
					warn = fmt.Sprintf("project_id '%s' not found. Did you mean: %s?",
						args.ProjectID, strings.Join(suggestions, ", "))
				}
			}

			// Aggregate links from all results (simplified — last project only)
			var links []ImplicitMCPLink
			recallOut := RecallOutput{Results: allItems, Links: links, Warning: warn}

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
			cwd = workspaceRoot
		}
		cwd = normalizePath(cwd)
		walkCwd, err := remapRoot(cwd)
		if err != nil {
			return errorResult(err), IngestOutput{}, nil
		}
		if err := walkRootPreflight(walkCwd); err != nil {
			return errorResult(err), IngestOutput{}, nil
		}

		if projectID == "" {
			// Fallback to .memoryrc.yaml project_id if available
			cfg, err := docwalk.LoadConfig(filepath.Join(walkCwd, ".memoryrc.yaml"))
			if err == nil && cfg.ProjectID != "" {
				projectID = cfg.ProjectID
			} else {
				projectID = "default"
			}
		}

		ref, refErr := resolveRef(walkCwd, args.Ref)
		if refErr != nil {
			return errorResult(refErr), IngestOutput{}, nil
		}
		if ref != "" && ref != "HEAD" && !strings.Contains(projectID, ref) {
			log.Printf(
				"WARNING: ref %q not found in project_id %q — consider using %s-%s",
				ref,
				projectID,
				projectID,
				ref,
			)
		}

		job := jobs.create(projectID)
		runCtx, cancel := context.WithCancel(jobCtx)
		jobs.setCancel(job.ID, cancel)
		safeGo(func() {
			ingestMu.Lock()
			result, err := onboard.Run(runCtx, onboard.Config{
				CWD:       walkCwd,
				ProjectID: projectID,
				ModelID:   modelID,
				Embedder:  embedder,
				Store:     db,
				BatchSize: batchSize,
				OnChunkProgress: func(embedded, total int) {
					jobs.updatePhase(job.ID, "embedding")
					jobs.updateChunksEmbedded(job.ID, embedded, total)
				},
				OnPersistChunks: func() { jobs.updatePhase(job.ID, "persisting") },
			})
			ingestMu.Unlock()

			if err != nil {
				jobs.finish(job.ID, 0, 0, 0, err)
				return
			}
			jobs.finish(job.ID, len(result.AllNodes), result.CallGraphEdges, result.ChunkCount, nil,
				result.ConventionsInferred, result.AutoLinked, result.ProposalsQueued)
		})

		output := IngestOutput{JobID: job.ID, Status: "running", Ref: ref}
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

		job := jobs.create(projectID)
		safeGo(func() {
			var err error
			defer func() {
				jobs.finish(job.ID, 0, 0, 0, err)
			}()

			if embedder == nil {
				err = fmt.Errorf("extract_and_remember requires a configured embedder")
				return
			}
			facts, xerr := extractor.Extract(jobCtx, args.Conversation)
			if xerr != nil {
				err = fmt.Errorf("extract: %w", xerr)
				return
			}

			var storedNodes []store.Node
			for _, f := range facts {
				vec, verr := embedder.Embed(jobCtx, f.Content)
				if verr != nil {
					err = fmt.Errorf("embed: %w", verr)
					return
				}

				candidates, cerr := db.SearchVector(jobCtx, projectID, vec, 5, modelID)
				if cerr != nil {
					log.Printf("extract_and_remember: search: %v", cerr)
					continue
				}

				id := fmt.Sprintf("%s/%s/%s", projectID, f.Kind, genULID())

				for _, c := range candidates {
					if c.NodeType == f.Kind && c.Score >= dedupe.DefaultThreshold {
						id = c.ID
						break
					}
				}

				storedNodes = append(storedNodes, store.Node{
					ID: id, ProjectID: projectID, NodeType: f.Kind,
					Title: f.Title, Content: f.Content, Importance: f.Importance,
					SourceAdapter: "extract_and_remember", Embedding: vec,
					ModelID: modelID,
				})
			}

			if len(storedNodes) == 0 {
				return
			}

			_, runErr := onboard.Run(jobCtx, onboard.Config{
				Nodes:             storedNodes,
				Edges:             nil,
				SkipNodeEmbedding: true,
				SkipPostProcess:   true,
				ProjectID:         projectID,
				ModelID:           modelID,
				Embedder:          embedder,
				Store:             db,
				BatchSize:         batchSize,
			})
			if runErr != nil {
				err = runErr
			}
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
		var contentExcerpt string
		if src, err := db.GetNode(ctx, args.NodeID); err == nil {
			srcTitle = src.Title
			if len(src.Content) > 200 {
				contentExcerpt = src.Content[:200]
			} else {
				contentExcerpt = src.Content
			}
		}

		var outgoingResults, incomingResults []EdgeResult

		if outgoing {
			edges, err := db.GetEdgesFrom(ctx, args.NodeID, args.EdgeTypes)
			if err == nil {
				for _, e := range edges {
					title, ntype := resolveNodeMeta(ctx, db, e.ToID)
					outgoingResults = append(outgoingResults, EdgeResult{
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
					incomingResults = append(incomingResults, EdgeResult{
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

		// Build neighbor counts from full edge lists (before limiting)
		neighborCounts := map[string]int{}
		for _, r := range outgoingResults {
			neighborCounts[r.EdgeType]++
		}
		for _, r := range incomingResults {
			neighborCounts[r.EdgeType]++
		}

		// Sort each direction by confidence DESC, take top limit each
		sortByConfidenceDesc(outgoingResults)
		sortByConfidenceDesc(incomingResults)
		if len(outgoingResults) > limit {
			outgoingResults = outgoingResults[:limit]
		}
		if len(incomingResults) > limit {
			incomingResults = incomingResults[:limit]
		}

		results := append(outgoingResults, incomingResults...)

		output := QueryGraphOutput{
			NodeID:         args.NodeID,
			Title:          srcTitle,
			ContentExcerpt: contentExcerpt,
			NeighborCounts: neighborCounts,
			Edges:          results,
		}
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

		// Filter by confidence tiers if specified
		if len(args.ConfidenceTiers) > 0 {
			filtered := make([]store.EdgeResult, 0, len(results))
			for _, r := range results {
				for _, tier := range args.ConfidenceTiers {
					if r.ConfidenceTier == tier {
						filtered = append(filtered, r)
						break
					}
				}
			}
			results = filtered
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

	// Tool 15: clear_project — synchronous, requires confirm
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "clear_project",
		Description: "Delete project data. Default: clears current model only (safe — keeps other models' vectors). model_id optional: override target model. purge=true: clear ALL models (full wipe). Requires confirm: true.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ClearProjectInput) (*mcp.CallToolResult, ClearProjectOutput, error) {
		if !args.Confirm {
			return errorResult(fmt.Errorf("clear_project requires confirm: true")), ClearProjectOutput{}, nil
		}
		if args.ProjectID == "" {
			return errorResult(fmt.Errorf("project_id is required")), ClearProjectOutput{}, nil
		}

		targetModelID := args.ModelID
		if args.Purge {
			targetModelID = "" // clear all models
		} else if targetModelID == "" {
			targetModelID = modelID // default: current server model
		}

		result, err := db.ClearProject(ctx, args.ProjectID, targetModelID)
		if err != nil {
			return errorResult(err), ClearProjectOutput{}, nil
		}

		out := ClearProjectOutput{
			Status:        "cleared",
			DeletedNodes:  result.Nodes,
			DeletedEdges:  result.Edges,
			DeletedChunks: result.Chunks,
			DeletedVecs:   result.Vectors,
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 16: list_projects — synchronous, read-only
	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_projects", Description: "List all projects with basic stats (node count, edge count). Read-only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, ListProjectsOutput, error) {
		projects, err := db.ListProjects(ctx)
		if err != nil {
			return errorResult(err), ListProjectsOutput{}, nil
		}
		out := ListProjectsOutput{Projects: projects}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 17: check_recent
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "check_recent",
		Description: "List nodes updated within the past N days. Default 7 days.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args CheckRecentInput) (*mcp.CallToolResult, CheckRecentOutput, error) {
		projectID := args.ProjectID
		if projectID == "" {
			projectID = "default"
		}
		days := args.Days
		if days <= 0 {
			days = 7
		}
		after := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
		nodes, err := db.ListNodesByUpdatedAfter(ctx, projectID, after, 100)
		if err != nil {
			return errorResult(err), CheckRecentOutput{}, nil
		}
		items := make([]CheckRecentNode, 0, len(nodes))
		for _, n := range nodes {
			items = append(items, CheckRecentNode{
				ID: n.ID, Title: n.Title,
				NodeType: n.NodeType, UpdatedAt: n.UpdatedAt,
			})
		}
		out := CheckRecentOutput{Nodes: items}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 18: find_path
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_path",
		Description: "BFS shortest path between two nodes. Returns path hops and total count.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args FindPathInput) (*mcp.CallToolResult, FindPathOutput, error) {
		if args.FromID == "" || args.ToID == "" {
			return errorResult(fmt.Errorf("from_id and to_id are required")), FindPathOutput{}, nil
		}
		maxDepth := args.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 10
		}

		// BFS with parent tracking
		type bfsNode struct {
			id       string
			parentID string
			edgeType string
			depth    int
		}
		queue := []bfsNode{{id: args.FromID, depth: 0}}
		visited := map[string]bool{args.FromID: true}
		parent := map[string]bfsNode{}
		var found bfsNode

		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]

			if cur.id == args.ToID {
				found = cur
				break
			}
			if cur.depth >= maxDepth {
				continue
			}

			edges, err := db.GetEdgesFrom(ctx, cur.id, args.EdgeTypes)
			if err != nil {
				continue
			}
			for _, e := range edges {
				if !visited[e.ToID] {
					visited[e.ToID] = true
					parent[e.ToID] = bfsNode{id: cur.id, edgeType: e.EdgeType}
					queue = append(
						queue,
						bfsNode{id: e.ToID, parentID: cur.id, edgeType: e.EdgeType, depth: cur.depth + 1},
					)
				}
			}
		}

		if found.id == "" {
			out := FindPathOutput{Hops: 0}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
			}, out, nil
		}

		// Reconstruct path
		var path []PathHop
		for cur := found; cur.id != args.FromID; cur = parent[cur.id] {
			title := store.DisplayName(cur.id)
			if n, err := db.GetNode(ctx, cur.id); err == nil {
				title = n.Title
			}
			path = append([]PathHop{{
				NodeID: cur.id, Title: title,
				EdgeType: cur.edgeType, Direction: "incoming",
			}}, path...)
		}
		// Add starting node
		title := store.DisplayName(args.FromID)
		if n, err := db.GetNode(ctx, args.FromID); err == nil {
			title = n.Title
		}
		path = append([]PathHop{{
			NodeID: args.FromID, Title: title,
		}}, path...)

		out := FindPathOutput{Path: path, Hops: len(path) - 1}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 19: link_projects
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "link_projects",
		Description: "Run cross-project linking for a project. Links code symbols to matching symbols in all other known projects.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args LinkProjectsInput) (*mcp.CallToolResult, LinkProjectsOutput, error) {
		if args.ProjectID == "" {
			return errorResult(fmt.Errorf("project_id is required")), LinkProjectsOutput{}, nil
		}
		// Load all nodes for this project
		allNodes, err := db.ListNodesByType(ctx, args.ProjectID, nil, 10000, 0)
		if err != nil {
			return errorResult(err), LinkProjectsOutput{}, nil
		}
		edges, err := onboard.CrossProjectLink(ctx, db, args.ProjectID, allNodes)
		if err != nil {
			return errorResult(err), LinkProjectsOutput{}, nil
		}
		if len(edges) > 0 {
			_ = db.UpsertEdges(ctx, edges)
		}
		out := LinkProjectsOutput{EdgesCreated: len(edges)}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(out)}},
		}, out, nil
	})

	// Tool 20: abort_ingestion
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "abort_ingestion",
		Description: "Cancel running ingest/onboard jobs. No project_id = cancel all. Partial data is left in place; re-run ingest to converge (idempotent upsert).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args AbortInput) (*mcp.CallToolResult, AbortOutput, error) {
		count := jobs.abort(args.ProjectID)
		output := AbortOutput{Cancelled: count, ProjectID: args.ProjectID}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: marshalJSON(output)}},
		}, output, nil
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

// sortByConfidenceDesc sorts edge results by confidence descending.
func sortByConfidenceDesc(results []EdgeResult) {
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Confidence > results[i].Confidence {
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
