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
	StructuralEdgeTypes = []string{"specifies", "implements", "tests", "calls", "defines", "imports", "convention-member"}
	InferredEdgeTypes   = []string{"auto-linked"}

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
		MaxChunks:      15,
		PreviewChunksPerNode: 3,
		ExcerptLen:     200,
		CrossSourceThreshold: 0.75,
		RRFK:                 60,
	}
}

// RecallParams is the input to Recall.
type RecallParams struct {
	ProjectID string
	Query     string
	Embedding []float32 // optional; nil = lexical-only mode
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
	Nodes []ScoredNode        `json:"nodes"`
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
	MatchType      string   // "lexical" | "semantic" | "hybrid" | "graph"
	Excerpt        string   `json:"excerpt"`          // truncated excerpt
	PreviewChunks  []string `json:"preview_chunks"`   // first N chunk contents
	TotalChunks    int      `json:"total_chunks"`
	MoreAvailable  bool     `json:"more_available"`
	NextChunkStart int      `json:"next_chunk_start"` // -1 if done
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
	// FTS5 MATCH treats bare words as phrase. Convert multi-word queries to OR.
	ftsq := toFTSQuery(params.Query)
	lexHits, err := r.store.SearchLexical(ctx, params.ProjectID, ftsq, limit*3)
	if err != nil {
		return nil, err
	}

	// Build a map of node ID → lexical relevance for fusion later.
	lexScores := make(map[string]float64, len(lexHits))
	for _, h := range lexHits {
		lexScores[h.ID] = rankToRelevance(h.Score)
	}

	// 2. Vector search (only if embedding provided)
	var vecScores map[string]float64
	if len(params.Embedding) > 0 {
		vecNodes, err := r.store.SearchVector(ctx, params.ProjectID, params.Embedding, limit*2)
		if err == nil {
			vecScores = make(map[string]float64, len(vecNodes))
			for _, v := range vecNodes {
				vecScores[v.ID] = v.Score // cosine similarity already in [0,1]
			}
		}
		// On error, proceed lexical-only — graceful degradation.
	}

	// 3. Collect all candidate node IDs (union of lexical + vector hits)
	candidateIDs := make(map[string]bool, len(lexScores)+len(vecScores))
	for id := range lexScores {
		candidateIDs[id] = true
	}
	for id := range vecScores {
		candidateIDs[id] = true
	}

	// 4. RRF fusion scoring
	now := time.Now()
	candNodes := r.fetchCandidates(ctx, candidateIDs)
	scored := r.rrfScore(candNodes, lexScores, vecScores, now)

	// 5. Graph pull-in: for top hits, fetch N-hop neighbors
	seen := map[string]bool{}
	for _, s := range scored {
		seen[s.ID] = true
	}
	if r.cfg.NeighborDepth > 0 && len(scored) > 0 {
		pullFrom := scored
		if len(pullFrom) > 5 {
			pullFrom = pullFrom[:5]
		}
		for _, s := range pullFrom {
			neighbors, confidence, err := r.store.NeighborsWithConfidence(
				ctx,
				s.Node.ID,
				r.cfg.NeighborTypes,
				r.cfg.NeighborDepth,
			)
			if err != nil {
				continue
			}

			for _, n := range neighbors {
				if seen[n.ID] {
					continue
				}

				seen[n.ID] = true
				recency := r.recencyDecay(now.Sub(parseTime(n)))
				ns := 0.5 * recency * n.Importance
				if confidence[n.ID] > 0.8 {
					ns *= 1.2
				}
				scored = append(scored, ScoredNode{
					Node:      n,
					Score:     ns,
					MatchType: "graph",
				})
			}
		}
	}

	// 6. Sort by score descending, cap at limit
	sortByScore(scored)
	if len(scored) > limit {
		scored = scored[:limit]
	}

	// 7. Chunk search (optional, only if embedding provided + IncludeChunks)
	var chunkResults []store.ScoredChunk
	if r.cfg.IncludeChunks && len(params.Embedding) > 0 {
		chunks, err := r.store.SearchChunks(ctx, params.ProjectID, params.Embedding, r.cfg.MaxChunks)
		if err == nil {
			chunkResults = chunks
		}
	}

	// 8. Cross-source implicit links: compare chunks pairwise by cosine
	// similarity when they come from different source adapters.
	var links []ImplicitLink
	if len(chunkResults) > 1 {
		links = findCrossSourceLinks(chunkResults, r.cfg.CrossSourceThreshold)
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
func (r *Retriever) fetchCandidates(ctx context.Context, ids map[string]bool) []store.Node {
	out := make([]store.Node, 0, len(ids))
	for id := range ids {
		n, err := r.store.GetNode(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// rrfScore scores candidates using Reciprocal Rank Fusion.
// RRF: for each candidate, score = Σ 1/(k + rank_in_set) across all search sets.
// When only lexical set exists, score ∝ 1/(k+rank) ≈ rank-based relevance.
func (r *Retriever) rrfScore(
	nodes []store.Node,
	lexScores, vecScores map[string]float64,
	now time.Time,
) []ScoredNode {
	k := float64(r.cfg.RRFK)
	if k == 0 {
		k = 60
	}

	// ponytail: estimate rank from relevance score [0,1].
	// Exact rank could use an ordered list from each search backend.
	out := make([]ScoredNode, 0, len(nodes))
	for _, node := range nodes {
		rankSum := 0.0
		sets := 0

		if rel, ok := lexScores[node.ID]; ok && rel > 0 {
			// Estimate rank from relevance score [0,1].
			// We use inverse: higher relevance = earlier rank ≈ 1
			// Map [0,1] relevance to rank 1..len
			estRank := int((1.0-rel)*float64(len(lexScores)-1)) + 1
			if estRank < 1 {
				estRank = 1
			}
			rankSum += 1.0 / (k + float64(estRank))
			sets++
		}
		if vec, ok := vecScores[node.ID]; ok && vec > 0 {
			estRank := int((1.0-vec)*float64(len(vecScores)-1)) + 1
			if estRank < 1 {
				estRank = 1
			}
			rankSum += 1.0 / (k + float64(estRank))
			sets++
		}

		if sets == 0 {
			continue
		}

		fused := rankSum // RRF: sum, not average — appearing in more sets naturally boosts score
		matchType := "lexical"
		if _, ok := lexScores[node.ID]; ok {
			if _, ok := vecScores[node.ID]; ok {
				matchType = "hybrid"
			}
		} else if _, ok := vecScores[node.ID]; ok {
			matchType = "semantic"
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

	return out
}

// fuseScore combines lexical relevance and vector cosine similarity.
// When no vectors are available (vecAvailable=false), returns pure lexical.
func fuseScore(lexRel, vecSim, alpha float64, vecAvailable bool) float64 {
	if !vecAvailable || vecSim == 0 {
		return lexRel
	}
	if lexRel == 0 {
		return vecSim
	}
	return alpha*lexRel + (1-alpha)*vecSim
}

// matchTypeOf classifies how a result was found: lexical-only, semantic-only,
// or hybrid (both signals contributed).
func matchTypeOf(lexRel, vecSim float64) string {
	switch {
	case lexRel > 0 && vecSim > 0:
		return "hybrid"
	case vecSim > 0:
		return "semantic"
	default:
		return "lexical"
	}
}

// rankToRelevance normalizes FTS5 BM25 rank (negative, lower = better)
// into a [0,1] relevance factor using a sigmoid-like decay.
func rankToRelevance(rank float64) float64 {
	return 1.0 / (1.0 + math.Exp(rank/2.0))
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

func sortByScore(s []ScoredNode) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j].Score < key.Score {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}

// findCrossSourceLinks compares chunk embeddings pairwise and returns
// implicit links for chunks from different source adapters whose cosine
// similarity exceeds the threshold.
func findCrossSourceLinks(chunks []store.ScoredChunk, threshold float64) []ImplicitLink {
	if len(chunks) < 2 {
		return nil
	}

	// Group chunks by source adapter (derived from ParentNodeID prefix)
	sourceOf := func(id string) string {
		// Chunk IDs are parent_node_id/chunk_N; parent_node_id pattern:
		// project/adapter/path...  e.g. "default/docwalk/README.md",
		// "default/gitrepo/cmd/main.go", "default/obsidian/note.md"
		parts := splitN(id, "/", 3)
		if len(parts) >= 2 {
			return parts[1] // adapter name
		}
		return "unknown"
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
		src := sourceOf(c.ParentNodeID)
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

// splitN splits s by sep and returns at most n parts.
func splitN(s, sep string, n int) []string {
	parts := make([]string, 0, n)
	rest := s
	for i := 0; i < n-1; i++ {
		idx := indexOf(rest, sep)
		if idx < 0 {
			parts = append(parts, rest)
			return parts
		}
		parts = append(parts, rest[:idx])
		rest = rest[idx+len(sep):]
	}
	parts = append(parts, rest)
	return parts
}

func indexOf(s, sep string) int {
	for i := 0; i <= len(s)-len(sep); i++ {
		if s[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
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
