// Package dedupe provides cosine-similarity-based deduplication for
// extracted facts before they are stored. The threshold is derived
// empirically by prototype/scoring.py against real transcript data.
//
// Calibration run 2026-07-20, v2 (real data): 22 real facts from
// ~/.claude/projects/-home-mystaline-dev-Workspace-Personal-sandbox/memory/
// (a personal side project's session-memory files — no NDA concerns, real
// decisions/corrections from actual Claude Code sessions, not synthetic
// fixtures). Each fact has two independently-authored terse phrasings: the
// MEMORY.md index one-liner and the file's YAML frontmatter description —
// both written for different audiences (human skim vs semantic search), so
// comparing them is a genuine "same fact, reworded" test, not a paraphrase
// I invented. Embedding via nomic-embed-text (768-dim) against local Ollama.
// See prototype/fixtures/sandbox_calibration_records.json and
// prototype/sandbox_calibration_output.json for the raw data.
//
// This superseded the v1 calibration (8 synthetic repo-fixture snippets,
// mixed narrative-vs-bullet register, threshold 0.80) for two reasons: (1)
// it's real data instead of synthetic, and (2) v1 compared mismatched
// registers (a verbose narrative fact vs a terse one-line paraphrase),
// which is NOT what extract_and_remember actually compares in production —
// it always compares two similarly-terse LLM-extracted facts. Matching the
// production register changed the result substantially.
//
// SAME-REGISTER true-positive (same fact, 2 independent terse phrasings,
// n=22): min=0.641 median=0.877 max=0.939.
// SAME-REGISTER true-negative (distinct facts, both terse, n=231):
// min=0.340 median=0.532 p95=0.647 max=0.710.
// The two distributions are cleanly separated (true-neg max 0.710 barely
// touches true-pos min 0.641) — much better separation than v1's noisy
// mixed-register run. Sweeping thresholds against this data: t=0.71 gives
// the minimum total error (2/253 pairs misclassified, 0.8%): 2 missed
// merges (9% of true dups), 0 wrong merges. Chose 0.71 over the raw
// error-minimizing value because wrong merges (silently conflating two
// distinct facts) are worse than missed merges (a near-dup just stays a
// separate node) — 0.71 has zero wrong-merges in this sample while missing
// only 2/22 true dups.
//
// Still a v2 starting point, not settled: n=22 facts from one project by
// one author. Re-calibrate against a larger, more diverse real corpus
// (multiple users/projects/domains) once available.
package dedupe

import (
	"math"
)

// DefaultThreshold is the empirically-derived cosine similarity cutoff
// above which two facts are treated as duplicates (merge, not insert).
// See package doc comment above for the calibration run this came from.
const DefaultThreshold = 0.71

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
