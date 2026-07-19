// Package retrieve provides scored retrieval: relevance * recency * importance
// with graph-neighbor pull-in for context enrichment.
package retrieve

import (
	"context"
	"math"
	"time"

	"github.com/mystaline-dev/tastastas/internal/store"
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
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		RecencyHalfLife: 7 * 24 * time.Hour,
		NeighborDepth:   1,
		NeighborTypes:   nil, // all
		MaxResults:      20,
	}
}

// ScoredNode is a node with its computed retrieval score.
type ScoredNode struct {
	store.Node
	Score float64
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
	return &Retriever{store: st, cfg: cfg}
}

// Recall performs scored retrieval: FTS search + scoring + graph pull-in.
// This replaces the Phase 3 stub that returned score=1.0 for all results.
func (r *Retriever) Recall(ctx context.Context, projectID, query string, limit int) ([]ScoredNode, error) {
	if limit <= 0 {
		limit = r.cfg.MaxResults
	}

	// 1. FTS search — get initial candidates
	hits, err := r.store.SearchLexical(ctx, projectID, query, limit*3) // over-fetch for scoring
	if err != nil {
		return nil, err
	}

	// 2. Score each hit
	now := time.Now()
	scored := make([]ScoredNode, 0, len(hits))
	seen := map[string]bool{}
	for _, h := range hits {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		scored = append(scored, ScoredNode{
			Node:  h,
			Score: r.score(h, now),
		})
	}

	// 3. Graph pull-in: for top hits, fetch N-hop neighbors
	if r.cfg.NeighborDepth > 0 && len(scored) > 0 {
		// Only pull in from top 5 hits (or fewer) to avoid explosion
		pullFrom := scored
		if len(pullFrom) > 5 {
			pullFrom = pullFrom[:5]
		}
		for _, s := range pullFrom {
			neighbors, edges, err := r.store.Neighbors(ctx, s.Node.ID, r.cfg.NeighborTypes, r.cfg.NeighborDepth)
			if err != nil {
				continue // non-fatal
			}
			for i, n := range neighbors {
				if seen[n.ID] {
					continue
				}
				seen[n.ID] = true
				// Neighbor score: original score * 0.5 (pulled in, not directly matched)
				ns := r.score(n, now) * 0.5
				// Boost if connected by a strong edge (confidence > 0.8)
				if i < len(edges) && edges[i].Confidence > 0.8 {
					ns *= 1.2
				}
				scored = append(scored, ScoredNode{Node: n, Score: ns})
			}
		}
	}

	// 4. Sort by score descending, cap at limit
	sortByScore(scored)
	if len(scored) > limit {
		scored = scored[:limit]
	}

	return scored, nil
}

// score computes: relevance (1.0 for FTS hits) * recency_decay * importance
func (r *Retriever) score(n store.Node, now time.Time) float64 {
	var t time.Time
	if n.UpdatedAt != "" {
		t, _ = time.Parse("2006-01-02T15:04:05.000Z", n.UpdatedAt)
	}
	if t.IsZero() && n.CreatedAt != "" {
		t, _ = time.Parse("2006-01-02T15:04:05.000Z", n.CreatedAt)
	}
	recency := r.recencyDecay(now.Sub(t))
	return recency * n.Importance
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
	// Insertion sort — fine for small slices (typically < 100)
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
