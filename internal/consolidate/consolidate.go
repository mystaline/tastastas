// Package consolidate detects co-occurring node access patterns and
// creates co-accessed edges. Runs as a periodic background cron.
package consolidate

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mystaline-dev/tastastas/internal/store"
)

// Run scans access_log for session-based co-occurrence and upserts
// co-accessed edges for pairs seen together in ≥3 sessions.
func Run(ctx context.Context, db store.Store) (int, error) {
	pairs, err := db.GetSessionPairs(ctx, 86400, 3)
	if err != nil {
		return 0, fmt.Errorf("consolidate: session pairs: %w", err)
	}
	if len(pairs) == 0 {
		return 0, nil
	}

	edges := make([]store.Edge, 0, len(pairs))
	for _, p := range pairs {
		edges = append(edges, store.Edge{
			FromID:         p.NodeA,
			ToID:           p.NodeB,
			EdgeType:       "co-accessed",
			Confidence:     0.6,
			ConfidenceTier: "INFERRED",
			Bidirectional:  true,
		})
	}

	if err := db.UpsertEdges(ctx, edges); err != nil {
		return 0, fmt.Errorf("consolidate: upsert edges: %w", err)
	}

	log.Printf("consolidate: created %d co-accessed edges", len(edges))
	return len(edges), nil
}

// RunPeriodic runs the consolidation cron at the given interval.
func RunPeriodic(ctx context.Context, db store.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := Run(ctx, db)
			if err != nil {
				log.Printf("consolidate: %v", err)
			} else if n > 0 {
				log.Printf("consolidate: created %d edges", n)
			}
		}
	}
}
