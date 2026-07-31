// Package retrieve provides scored retrieval: relevance * recency * importance
// with graph-neighbor pull-in for context enrichment.
//
// When an embedding is provided via RecallParams.Embedding, Retrieve fuses
// lexical (FTS5 BM25) and vector (cosine similarity) scores:
//
//	fused = alpha * lexical_relevance + (1-alpha) * cosine_similarity
//
// Default alpha = 0.4 (weight lexical lower — BM25 keyword match is a weaker
// signal than semantic proximity for natural-language queries).
// When embedding is nil, operates in pure lexical mode (no embedder needed).
package retrieve

import (
	"context"
	"math"
	"time"

	"strings"

	"github.com/mystaline-dev/tastastas/internal/store"
)

// Edge type tiers for enrichNode edge filtering.
// Structural edges are config/parser-derived (confidence 1.0).
// Inferred edges are embedding-derived (~0.80-0.89).
// Proposed edges are excluded — they are the review queue, not knowledge.
var (
	StructuralEdgeTypes = []string{
		"specifies",
		"implements",
		"tests",
		"calls",
		"defines",
		"imports",
		"convention-member",
	}
	InferredEdgeTypes = []string{"auto-linked"}

	StructuralEdgeCap = 10
	InferredEdgeCap   = 10
)

// Config holds retrieval tuning parameters.
type Config struct {
	// RecencyHalfLife is how long until a fact decays to 50% importance.
	// Default: 7 days.
	RecencyHalfLife time.Duration

	// NeighborDepth is how many hops to walk from each top hit for pull-in.
	// 0 = no pull-in, 1 = direct neighbors, 2 = neighbors of neighbors.
	NeighborDepth int

	// NeighborTypes restricts pull-in to these edge types. nil = all types.
	NeighborTypes []string

	// MaxResults caps the final returned count after scoring + pull-in.
	MaxResults int

	// Alpha is the weight given to lexical (FTS5) score in the fused score.
	// (1-alpha) goes to vector (cosine similarity) score.
	// Default: 0.4 (favor semantic over keyword).
	Alpha float64

	// IncludeChunks, when true, searches chunk vectors in addition to node
	// vectors. Chunks are returned alongside nodes, tagged with their
	// ParentNodeID so the caller can reconstruct context.
	IncludeChunks bool

	// MaxChunks caps the number of chunk results returned by SearchChunks. Default: 15.
	MaxChunks int

	// PreviewChunksPerNode caps how many chunk previews are returned per node. Default: 3.
	PreviewChunksPerNode int

	// ExcerptLen caps the node content excerpt length. Default: 200 (chars).
	ExcerptLen int

	// RRFK is the constant k for Reciprocal Rank Fusion.
	// Standard value: 60. Larger = more weight to lower ranks.
	RRFK int

	// CrossSourceThreshold is the minimum cosine similarity between two
	// chunks from different source adapters to surface as an implicit
	// semantic link. Default: 0.85.
	CrossSourceThreshold float64
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		RecencyHalfLife:      7 * 24 * time.Hour,
		NeighborDepth:        1,
		NeighborTypes:        nil,
		MaxResults:           20,
		Alpha:                0.4,
		IncludeChunks:        true,
		MaxChunks:            15,
		PreviewChunksPerNode: 3,
		ExcerptLen:           200,
		CrossSourceThreshold: 0.75,
		RRFK:                 60,
	}
}

// RecallParams is the input to Recall.
type RecallParams struct {
	ProjectID string
	Query     string
	Embedding []float32 // optional; nil = lexical-only mode
	ModelID   string   // filter vectors by model; "" = no filter (legacy)
	Limit     int

	// LinkThreshold overrides Config.CrossSourceThreshold when > 0.
	LinkThreshold float64
}

// ImplicitLink represents a cross-source semantic link between two chunks
// whose embeddings have cosine similarity above a threshold but share no
// explicit graph edge. Surfaces as metadata in recall results.
type ImplicitLink struct {
	FromChunkID string  `json:"from_chunk_id"`
	ToChunkID   string  `json:"to_chunk_id"`
	Cosine      float64 `json:"cosine"`
}

// RecallResult groups nodes, chunks, and implicit links returned by Recall.
type RecallResult struct {
	Nodes  []ScoredNode        `json:"nodes"`
	Chunks []store.ScoredChunk `json:"-"`
	Links  []ImplicitLink      `json:"links,omitempty"`
}

// EdgeRef represents a single outgoing edge from a node, with the target's
// metadata resolved for display. Used to surface relationships in recall.
type EdgeRef struct {
	ToID       string  `json:"to_id"`
	ToTitle    string  `json:"to_title"`
	EdgeType   string  `json:"edge_type"`
	Confidence float64 `json:"confidence"`
}

// ScoredNode is a node with its computed retrieval score.
type ScoredNode struct {
	store.Node
	Score          float64
	MatchType      string    // "lexical" | "semantic" | "hybrid" | "graph"
	Excerpt        string    `json:"excerpt"`        // truncated excerpt
	PreviewChunks  []string  `json:"preview_chunks"` // first N chunk contents
	TotalChunks    int       `json:"total_chunks"`
	MoreAvailable  bool      `json:"more_available"`
	NextChunkStart int       `json:"next_chunk_start"` // -1 if done
	Edges          []EdgeRef `json:"edges,omitempty"`
	InferredEdges  []EdgeRef `json:"inferred_edges,omitempty"`
}

// Retriever scores and retrieves facts from the store.
type Retriever struct {
	store store.Store
	cfg   Config
}

// New creates a Retriever.
func New(st store.Store, cfg Config) *Retriever {
	if cfg.RecencyHalfLife == 0 {
		cfg.RecencyHalfLife = 7 * 24 * time.Hour
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = 20
	}
	if cfg.Alpha == 0 {
		cfg.Alpha = 0.4
	}
	if cfg.MaxChunks == 0 {
		cfg.MaxChunks = 15
	}
	if cfg.PreviewChunksPerNode == 0 {
		cfg.PreviewChunksPerNode = 3
	}
	if cfg.ExcerptLen == 0 {
		cfg.ExcerptLen = 200
	}
	if cfg.CrossSourceThreshold == 0 {
		cfg.CrossSourceThreshold = 0.85
	}
	return &Retriever{store: st, cfg: cfg}
}

// Recall performs scored retrieval with optional semantic (vector) fusion.
//
// Always runs lexical (FTS5) search. When params.Embedding is non-nil,
// also runs vector search over node vectors and chunk vectors, then fuses
// scores. Graph neighbor pull-in follows.
func (r *Retriever) Recall(ctx context.Context, params RecallParams) (*RecallResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = r.cfg.MaxResults
	}

	// 1. Lexical search — always runs (fast, no external deps)
	ftsq := toFTSQuery(params.Query)
	lexHits, err := r.store.SearchLexical(ctx, params.ProjectID, ftsq, limit*3)
	if err != nil {
		return nil, err
	}

	// 2. Vector search (only if embedding provided)
	var vecHits []store.ScoredNode
	if len(params.Embedding) > 0 {
		vh, err := r.store.SearchVector(ctx, params.ProjectID, params.Embedding, limit*2, params.ModelID)
		if err == nil {
			vecHits = vh
		}
		// On error, proceed lexical-only — graceful degradation.
	}

	// 3. RRF fusion scoring using real rank from ordered result lists
	now := time.Now()
	scored := r.rrfScore(lexHits, vecHits, nil, now, limit)

	// 4. Graph pull-in — walk edges from top-5 RRF results
	seen := map[string]bool{}
	for _, s := range scored {
		seen[s.ID] = true
	}
	var graphNodes []ScoredNode
	if r.cfg.NeighborDepth > 0 && len(scored) > 0 {
		pullFrom := scored
		if len(pullFrom) > 5 {
			pullFrom = pullFrom[:5]
		}
		for _, s := range pullFrom {
			neighbors, confidence, err := r.store.NeighborsWithConfidence(
				ctx, s.Node.ID, r.cfg.NeighborTypes, r.cfg.NeighborDepth,
			)
			if err != nil {
				continue
			}
			for _, n := range neighbors {
				if seen[n.ID] {
					continue
				}
				seen[n.ID] = true
				graphNodes = append(graphNodes, ScoredNode{
					Node:      n,
					Score:     confidence[n.ID],
					MatchType: "graph",
				})
			}
		}
	}
	if len(graphNodes) > 0 {
		// Merge graph nodes via RRF: treat them as appearing in a weak third
		// "graph expansion" set at back ranks so their scores stay in the same
		// magnitude as search results, never dominating them.
		scored = r.rrfScore(lexHits, vecHits, graphNodes, now, limit)
	}

	// 7. Chunk search (optional, only if embedding provided + IncludeChunks)
	var chunkResults []store.ScoredChunk
	if r.cfg.IncludeChunks && len(params.Embedding) > 0 {
		chunks, err := r.store.SearchChunks(ctx, params.ProjectID, params.Embedding, r.cfg.MaxChunks, params.ModelID)
		if err == nil {
			chunkResults = chunks
		}
	}

	// 8a. Access boost: increase scores for frequently/recently accessed nodes
	for i := range scored {
		count, lastAt, err := r.store.GetAccessStats(ctx, scored[i].ID)
		if err == nil {
			scored[i].Score *= applyAccessBoost(count, lastAt)
		}
	}

	// 8b. Enrich scored nodes with excerpt + chunk preview + pagination
	for i := range scored {
		r.enrichNode(ctx, &scored[i])
	}

	// 9. Cross-source implicit links: compare chunks pairwise by cosine
	// similarity when they come from different source adapters.
	threshold := r.cfg.CrossSourceThreshold
	if params.LinkThreshold > 0 {
		threshold = params.LinkThreshold
	}
	var links []ImplicitLink
	if len(chunkResults) > 1 {
		links = findCrossSourceLinks(chunkResults, threshold)
	}

	return &RecallResult{Nodes: scored, Chunks: chunkResults, Links: links}, nil
}

// toFTSQuery converts user input to FTS5 MATCH query:
// Multi-word → OR terms in doublequotes. Single term → escaped with
// doublequotes (FTS5 treats hyphens as column operators and quotes as
// syntax — wrapping bare terms in doublequotes prevents both).
// Empty → match-all prefix.
func toFTSQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return "*"
	}
	if len(fields) == 1 {
		return `"` + escapeFTS(q) + `"`
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = `"` + escapeFTS(f) + `"`
	}
	return strings.Join(parts, " OR ")
}

// escapeFTS quotes any bare double quotes inside a term so FTS5 doesn't
// interpret them as syntax. Hyphens, parens, and other special chars are
// handled by wrapping in doublequotes at the call site.
func escapeFTS(s string) string {
	return strings.ReplaceAll(s, `"`, `""`)
}

// fetchCandidates loads nodes by ID from the store.
// rrfScore scores candidates using Reciprocal Rank Fusion.
// RRF: score = Σ 1/(k + rank_in_set) across all search sets.
// lexHits and vecHits are ordered by the backend (best match first).
// graphNodes are unordered — each gets rank = runnerUp + proximity within
// the graph expansion set, so graph-neighbor scores never dominate search.
func (r *Retriever) rrfScore(
	lexHits, vecHits []store.ScoredNode,
	graphHits []ScoredNode,
	now time.Time,
	limit int,
) []ScoredNode {
	k := float64(r.cfg.RRFK)
	if k == 0 {
		k = 60
	}

	// Build rank for each search set: node ID → 1-based rank
	lexRank := make(map[string]int, len(lexHits))
	for i, h := range lexHits {
		lexRank[h.ID] = i + 1
	}
	vecRank := make(map[string]int, len(vecHits))
	for i, h := range vecHits {
		vecRank[h.ID] = i + 1
	}

	// Union of all node IDs across all sets
	allIDs := make(map[string]bool, len(lexRank)+len(vecRank)+len(graphHits))
	for id := range lexRank {
		allIDs[id] = true
	}
	for id := range vecRank {
		allIDs[id] = true
	}
	for _, g := range graphHits {
		allIDs[g.ID] = true
	}

	// Graph set: use the graph confidence as a pseudo-rank so nodes
	// closer to the frontier rank higher than deeper expansions.
	graphRank := make(map[string]int, len(graphHits))
	for _, g := range graphHits {
		// Confidence [0,1] → pseudo-rank 1..N within graph set
		pr := int((1.0-g.Score)*float64(len(graphHits)-1)) + 1
		if pr < 1 {
			pr = 1
		}
		graphRank[g.ID] = pr
	}

	out := make([]ScoredNode, 0, len(allIDs))
	for id := range allIDs {
		rankSum := 0.0
		sets := 0

		if rank, ok := lexRank[id]; ok {
			rankSum += 1.0 / (k + float64(rank))
			sets++
		}
		if rank, ok := vecRank[id]; ok {
			rankSum += 1.0 / (k + float64(rank))
			sets++
		}
		if rank, ok := graphRank[id]; ok {
			startOffset := len(lexHits) + len(vecHits) + 1
			if startOffset < 5 {
				startOffset = 5
			}
			rankSum += 1.0 / (k + float64(startOffset+rank))
			sets++
		}
		if sets == 0 {
			continue
		}

		fused := rankSum
		matchType := "lexical"
		if _, ok := lexRank[id]; ok {
			if _, ok := vecRank[id]; ok {
				matchType = "hybrid"
			}
		} else if _, ok := vecRank[id]; ok {
			matchType = "semantic"
		} else if _, ok := graphRank[id]; ok {
			matchType = "graph"
		}

		// Fetch node data for the score calculation.
		// If the node is in lexHits or vecHits, reuse that data; otherwise
		// fetch from the store (graph-only nodes).
		var node store.Node
		found := false
		for _, h := range lexHits {
			if h.ID == id {
				node = h.Node
				if h.Node.Importance != 0 {
					found = true
					break
				}
			}
		}
		if !found {
			for _, h := range vecHits {
				if h.ID == id {
					node = h.Node
					if h.Node.Importance != 0 {
						found = true
						break
					}
				}
			}
		}
		if !found {
			for _, g := range graphHits {
				if g.ID == id {
					node = g.Node
					found = true
					break
				}
			}
		}
		if !found {
			n, err := r.store.GetNode(context.Background(), id)
			if err != nil {
				continue
			}
			node = n
		}

		recency := r.recencyDecay(now.Sub(parseTime(node)))
		score := fused * recency * node.Importance
		if score > 0 {
			out = append(out, ScoredNode{
				Node:      node,
				Score:     score,
				MatchType: matchType,
			})
		}
	}

	// Sort by score descending
	for i := 1; i < len(out); i++ {
		key := out[i]
		j := i - 1
		for j >= 0 && out[j].Score < key.Score {
			out[j+1] = out[j]
			j--
		}
		out[j+1] = key
	}
	if len(out) > limit {
		out = out[:limit]
	}

	return out
}

// parseTime extracts the UpdatedAt timestamp from a node, falling back
// to CreatedAt, returning zero time if both are empty/invalid.
func parseTime(n store.Node) time.Time {
	const layout = "2006-01-02T15:04:05.000Z"
	if n.UpdatedAt != "" {
		if t, err := time.Parse(layout, n.UpdatedAt); err == nil {
			return t
		}
	}
	if n.CreatedAt != "" {
		if t, err := time.Parse(layout, n.CreatedAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

// recencyDecay returns a factor in (0, 1] based on how old the fact is.
// Uses exponential decay: factor = 2^(-age / halfLife).
func (r *Retriever) recencyDecay(age time.Duration) float64 {
	if age <= 0 {
		return 1.0
	}
	halfLife := r.cfg.RecencyHalfLife.Seconds()
	if halfLife <= 0 {
		return 1.0
	}
	return math.Pow(2, -age.Seconds()/halfLife)
}

// applyAccessBoost adjusts recall scores upward for nodes that have been
// accessed frequently or recently. score *= (1 + 0.2 * log2(1 + accessCount)).
// Recent access (within 1 hour) adds an additional 30% boost.
func applyAccessBoost(accessCount int, lastAccessedAt string) float64 {
	if accessCount <= 0 {
		return 1.0
	}
	boost := 1.0 + 0.2*math.Log2(1+float64(accessCount))
	if lastAccessedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, lastAccessedAt); err == nil {
			if time.Since(t) < time.Hour {
				boost *= 1.3 // session bias: recent access in same session
			}
		}
	}
	return boost
}

// excerpt truncates content to n runes, appending "..." if truncated.
func excerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// enrichNode fills a scored node's pagination fields from chunk metadata
// and populates edges from the graph store.
func (r *Retriever) enrichNode(ctx context.Context, s *ScoredNode) {
	s.Excerpt = excerpt(s.Node.Content, r.cfg.ExcerptLen)

	// Populate edges from the node's outgoing edges (1 hop).
	// Split into structural and inferred tiers, sort by confidence desc, cap per tier.
	// Proposed edges are excluded entirely (review queue, not knowledge).
	edges, err := r.store.GetEdgesFrom(ctx, s.Node.ID, nil)
	if err == nil && len(edges) > 0 {
		var structEdges, inferredEdges []EdgeRef
		for _, e := range edges {
			title := store.DisplayName(e.ToID)
			if t, nerr := r.store.GetNode(ctx, e.ToID); nerr == nil {
				title = t.Title
			}
			ref := EdgeRef{
				ToID:       e.ToID,
				ToTitle:    title,
				EdgeType:   e.EdgeType,
				Confidence: e.Confidence,
			}
			// Classify by tier
			isStructural := false
			for _, st := range StructuralEdgeTypes {
				if e.EdgeType == st {
					isStructural = true
					break
				}
			}
			isInferred := false
			for _, it := range InferredEdgeTypes {
				if e.EdgeType == it {
					isInferred = true
					break
				}
			}
			switch {
			case isStructural:
				structEdges = append(structEdges, ref)
			case isInferred:
				inferredEdges = append(inferredEdges, ref)
			}
			// proposed and unknown types are dropped
		}
		// Sort each tier by confidence desc
		sortEdgesByConfidence(structEdges)
		sortEdgesByConfidence(inferredEdges)
		// Cap per tier
		if len(structEdges) > StructuralEdgeCap {
			structEdges = structEdges[:StructuralEdgeCap]
		}
		if len(inferredEdges) > InferredEdgeCap {
			inferredEdges = inferredEdges[:InferredEdgeCap]
		}
		s.Edges = structEdges
		s.InferredEdges = inferredEdges
	}

	total, err := r.store.CountChunksByParent(ctx, s.Node.ID)
	if err != nil || total == 0 {
		s.TotalChunks = 0
		s.MoreAvailable = false
		s.NextChunkStart = -1
		return
	}
	s.TotalChunks = total

	// Fetch first PreviewChunksPerNode chunks as preview
	preview, err := r.store.GetChunksByParent(ctx, s.Node.ID, r.cfg.PreviewChunksPerNode, 0)
	if err != nil {
		s.MoreAvailable = total > 0
		s.NextChunkStart = 0
		return
	}
	s.PreviewChunks = make([]string, len(preview))
	for i, c := range preview {
		s.PreviewChunks[i] = c.Content
	}

	got := len(preview)
	s.MoreAvailable = got < total
	if s.MoreAvailable {
		s.NextChunkStart = got
	} else {
		s.NextChunkStart = -1
	}
}

// findCrossSourceLinks compares chunk embeddings pairwise and returns
// implicit links for chunks from different source adapters whose cosine
// similarity exceeds the threshold.
func findCrossSourceLinks(chunks []store.ScoredChunk, threshold float64) []ImplicitLink {
	if len(chunks) < 2 {
		return nil
	}

	type idxVec struct {
		idx int
		vec []float32
		src string
	}
	items := make([]idxVec, 0, len(chunks))
	for i, c := range chunks {
		if len(c.Embedding) == 0 {
			continue
		}
		src := c.SourceAdapter
		if src == "" {
			src = "unknown"
		}
		items = append(items, idxVec{idx: i, vec: c.Embedding, src: src})
	}

	var links []ImplicitLink
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			if a.src == b.src {
				continue // same adapter — not cross-source
			}
			cos := cosineSimilarity(a.vec, b.vec)
			if cos < threshold {
				continue
			}
			links = append(links, ImplicitLink{
				FromChunkID: chunks[a.idx].ID,
				ToChunkID:   chunks[b.idx].ID,
				Cosine:      cos,
			})
		}
	}
	return links
}

// cosineSimilarity computes the cosine similarity between two float32 vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// sortEdgesByConfidence sorts a slice of EdgeRef by confidence descending.
func sortEdgesByConfidence(edges []EdgeRef) {
	for i := 1; i < len(edges); i++ {
		key := edges[i]
		j := i - 1
		for j >= 0 && edges[j].Confidence < key.Confidence {
			edges[j+1] = edges[j]
			j--
		}
		edges[j+1] = key
	}
}
