// Package dedupe provides cosine-similarity-based deduplication for
// extracted facts before they are stored. The threshold is derived
// empirically by prototype/scoring.py against real transcript data.
package dedupe

import (
	"math"
)

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

// Dedupe checks a candidate embedding against existing embeddings.
// Returns true if the candidate is a duplicate (should merge, not insert).
// threshold is the cosine similarity cutoff — derived empirically by
// prototype/scoring.py, typically 0.85-0.95 for sentence-transformers.
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
