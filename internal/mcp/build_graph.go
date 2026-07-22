package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mystaline-dev/tastastas/internal/embed"
	"github.com/mystaline-dev/tastastas/internal/llm"
	"github.com/mystaline-dev/tastastas/internal/onboard"
	"github.com/mystaline-dev/tastastas/internal/store"
)

type buildGraphState struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`

	DocsScanned int `json:"docs_scanned_tier1,omitempty"`
	AutoLinked  int `json:"edges_auto_linked,omitempty"`
	Proposals   int `json:"proposals_judged,omitempty"`
}

// runBuildGraph runs Tier1→Tier2→Tier3 pipeline async.
func (js *jobStore) runBuildGraph(
	ctx context.Context,
	projectID string,
	db store.Store,
	embedder embed.EmbedderBackend,
	cl llm.Client,
) *buildGraphState {
	s := &buildGraphState{
		ID:        fmt.Sprintf("bg-%d", time.Now().UnixNano()),
		Status:    "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}

	go func() {
		start := time.Now()
		if err := runPipeline(
			ctx,
			s,
			projectID,
			db,
			embedder,
			cl,
		); err != nil {
			s.Status = "error"
			s.Error = err.Error()
		} else {
			s.Status = "done"
		}

		s.EndedAt = time.Now().UTC().Format(time.RFC3339)
		log.Printf("build_knowledge_graph: %s done in %v", projectID, time.Since(start))
	}()
	return s
}

func runPipeline(
	ctx context.Context,
	s *buildGraphState,
	projectID string,
	db store.Store,
	embedder embed.EmbedderBackend,
	cl llm.Client,
) error {
	docTypes := []string{"generic-doc", "prd", "api-spec", "erd", "test-case", "architecture-decision", "visual-design"}

	// Tier 1: LLM extraction on doc nodes
	docs, err := db.ListNodesByType(ctx, projectID, docTypes, 200, 0)
	if err != nil {
		return fmt.Errorf("list docs: %w", err)
	}

	s.DocsScanned = len(docs)
	for _, d := range docs {
		if d.Content == "" {
			continue
		}

		mentions, err := extractMentions(ctx, cl, d.Content)
		if err != nil {
			log.Printf("tier1 extract %s: %v", d.ID, err)
			continue
		}

		for _, m := range mentions {
			resolved, err := resolveMention(ctx, db, projectID, m.name)
			if err != nil || resolved == "" {
				continue
			}

			_ = db.UpsertEdge(ctx, store.Edge{
				FromID: d.ID, ToID: resolved,
				EdgeType: m.relation, Confidence: 0.85,
			})
		}
	}

	// Tier 2: embed + score nodes
	if embedder != nil {
		auto, err := tier2EmbedAndScore(ctx, db, embedder, projectID)
		if err != nil {
			log.Printf("tier2: %v", err)
		}
		s.AutoLinked = auto
	}

	// Tier 3: judge edge_proposals (in DB table)
	proposals, err := listEdgeProposals(ctx, db, projectID)
	if err != nil {
		return fmt.Errorf("list proposals: %w", err)
	}

	for _, p := range proposals {
		from, err1 := db.GetNode(ctx, p.FromID)
		to, err2 := db.GetNode(ctx, p.ToID)
		if err1 != nil || err2 != nil {
			continue
		}

		v, err := judgeProposal(ctx, cl, from, to)
		if err != nil {
			log.Printf("tier3 judge %s→%s: %v", p.FromID, p.ToID, err)
			continue
		}

		if v.accept {
			_ = db.UpsertEdge(ctx, store.Edge{
				FromID: p.FromID, ToID: p.ToID,
				EdgeType: v.edgeType, Confidence: v.confidence,
			})
		}

		if err := db.DeleteEdge(ctx, p.FromID, p.ToID, "proposed"); err == nil {
			s.Proposals++
		}
	}
	return nil
}

// tier2EmbedAndScore loads nodes, batch-embeds, runs Tier2 scoring.
func tier2EmbedAndScore(
	ctx context.Context,
	db store.Store,
	embedder embed.EmbedderBackend,
	projectID string,
) (int, error) {
	types := []string{"code:function", "code:type", "code:method", "generic-doc", "convention"}

	nodes, err := db.ListNodesByType(ctx, projectID, types, 500, 0)
	if err != nil {
		return 0, err
	}

	var toEmbed []store.Node
	var idx []int
	for i, n := range nodes {
		if n.Content != "" {
			toEmbed = append(toEmbed, n)
			idx = append(idx, i)
		}
	}

	if len(toEmbed) == 0 {
		return 0, nil
	}

	texts := make([]string, len(toEmbed))
	for i, n := range toEmbed {
		texts[i] = n.Title + ": " + n.Content
	}

	vecs, err := embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("embed batch: %w", err)
	}

	for k, v := range vecs {
		nodes[idx[k]].Embedding = v
	}

	auto, _ := onboard.Tier2ScoreAndLink(ctx, db, projectID, nodes)
	return auto, nil
}

// --- Tier 1 helpers ---

type mention struct {
	name     string
	relation string
}

var mentionJSONSchema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"relation": map[string]any{"type": "string", "enum": []string{"references", "implements", "tests"}},
		},
		"required": []string{"name", "relation"},
	},
}

func extractMentions(ctx context.Context, cl llm.Client, content string) ([]mention, error) {
	raw, err := cl.Chat(ctx, []llm.Message{
		{
			Role:    "system",
			Content: "Extract named entity mentions from this document that could be code symbols or other documents. Output JSON array only.",
		},
		{
			Role: "user",
			Content: fmt.Sprintf(
				`Document:\n%s\n\nReturn JSON array of {"name":"...","relation":"references|implements|tests"}`,
				truncateForLLM(content, 3000),
			),
		},
	}, llm.Option{Format: mentionJSONSchema})
	if err != nil {
		return nil, err
	}
	return parseMentions(raw)
}

func parseMentions(raw string) ([]mention, error) {
	raw = cleanJSON(raw)
	var list []struct {
		Name     string `json:"name"`
		Relation string `json:"relation"`
	}

	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}

	out := make([]mention, 0, len(list))
	for _, m := range list {
		if m.Name != "" {
			out = append(out, mention{name: m.Name, relation: m.Relation})
		}
	}

	return out, nil
}

func resolveMention(ctx context.Context, db store.Store, projectID, name string) (string, error) {
	results, err := db.SearchLexical(ctx, projectID, name, 3)
	if err != nil || len(results) == 0 {
		return "", err
	}

	for _, r := range results {
		if strings.EqualFold(r.Title, name) {
			return r.ID, nil
		}
	}

	return results[0].ID, nil
}

// --- Tier 3 helpers ---

type verdict struct {
	accept     bool
	edgeType   string
	confidence float64
}

var verdictJSONSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"accept":     map[string]any{"type": "boolean"},
		"edge_type":  map[string]any{"type": "string"},
		"confidence": map[string]any{"type": "number"},
		"reason":     map[string]any{"type": "string"},
	},
	"required": []string{"accept", "edge_type", "confidence"},
}

func judgeProposal(ctx context.Context, cl llm.Client, from, to store.Node) (verdict, error) {
	prompt := fmt.Sprintf(
		`Decide if these two memory nodes should be linked:

[FROM] %s (%s): %s
[TO]   %s (%s): %s

Return JSON: {"accept":true/false,"edge_type":"references|implements|depends-on|related-to","confidence":0.0-1.0,"reason":"..."}`,
		from.Title, from.NodeType, truncateForLLM(from.Content, 200),
		to.Title, to.NodeType, truncateForLLM(to.Content, 200))

	raw, err := cl.Chat(
		ctx,
		[]llm.Message{
			{
				Role:    "system",
				Content: "You judge if two memory nodes should be linked. Output JSON only.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		llm.Option{
			// TODO: make this configurable (or use the same model as the one used for Tier 1/2)
			Model:  "qwen2.5:7b",
			Format: verdictJSONSchema,
		},
	)
	if err != nil {
		return verdict{}, err
	}

	return parseVerdict(raw)
}

func parseVerdict(raw string) (verdict, error) {
	raw = cleanJSON(raw)
	var v struct {
		Accept     bool    `json:"accept"`
		EdgeType   string  `json:"edge_type"`
		Confidence float64 `json:"confidence"`
	}

	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return verdict{}, err
	}

	if v.EdgeType == "" {
		v.EdgeType = "related-to"
	}

	if v.Confidence == 0 {
		v.Confidence = 0.7
	}

	return verdict{
		accept:     v.Accept,
		edgeType:   v.EdgeType,
		confidence: v.Confidence,
	}, nil
}

// --- DB helpers ---

func listEdgeProposals(ctx context.Context, db store.Store, projectID string) ([]store.Edge, error) {
	return db.ListEdgesByType(ctx, projectID, "proposed", 100, 0)
}

// --- utility ---

func cleanJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		start, end := 0, len(lines)
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				if start == 0 {
					start = i + 1
				} else {
					end = i
				}
			}
		}
		if start > 0 && end > start {
			raw = strings.Join(lines[start:end], "\n")
		}
	}
	return raw
}

func truncateForLLM(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
