// Package dedupe provides cosine-similarity-based deduplication for
// extracted facts before they are stored. The threshold is derived
// empirically by prototype/scoring.py against real transcript data.
//
// Calibration run 2026-07-20 (see prototype/calibration_output.jsonl and
// prototype/fixtures/snippets_from_repo_testdata.jsonl): 8 snippets derived
// from repo test fixtures (obsidian vault + docwalk acme-style testdata) —
// NOT real user conversation data, since this session had no access to
// actual user conversation history. Extraction via qwen3.5:2b-q4_K_M,
// embedding via nomic-embed-text (768-dim), both against local Ollama.
//
// True near-duplicate pairs (same underlying fact, differently worded
// snippet — e.g. "PostgreSQL... full-text search" restated twice, or
// "coupon redemption" restated twice) scored 0.82-0.84 cosine.
// Genuinely-distinct-but-same-topic facts (coupon expiry vs coupon
// validity vs coupon checkout location — all about coupons, but different
// facts) scored 0.62-0.76 cosine. The two distributions separate but with
// a narrow gap and a small sample (13 facts, 78 pairs, 4 known-dup pairs
// identified by hand) — re-calibrate against a larger, more diverse
// real-conversation corpus once available. Treat 0.80 as a reasonable v1
// starting point, not a settled constant.
package dedupe

import (
	"math"
)

// DefaultThreshold is the empirically-derived cosine similarity cutoff
// above which two facts are treated as duplicates (merge, not insert).
// See package doc comment above for the calibration run this came from.
const DefaultThreshold = 0.80

// CosineSimilarity computes cosine similarity between two vectors.
// Returns 0 if either vector is empty or dimensions don't match.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// IsDuplicate checks a candidate embedding against existing embeddings.
// Returns true if the candidate is a duplicate (should merge, not insert).
// threshold is the cosine similarity cutoff — see DefaultThreshold and the
// package doc comment for the empirical calibration this was derived from.
func IsDuplicate(candidate []float32, existing [][]float32, threshold float64) bool {
	for _, e := range existing {
		if CosineSimilarity(candidate, e) >= threshold {
			return true
		}
	}
	return false
}

// FindMostSimilar returns the index and score of the most similar vector
// in existing to candidate, or -1 if empty. Used for merge target selection.
func FindMostSimilar(candidate []float32, existing [][]float32) (int, float64) {
	bestIdx := -1
	bestScore := 0.0
	for i, e := range existing {
		s := CosineSimilarity(candidate, e)
		if s > bestScore {
			bestScore = s
			bestIdx = i
		}
	}
	return bestIdx, bestScore
}
