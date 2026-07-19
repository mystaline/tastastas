package dedupe

import (
	"testing"
)

func TestCosineSimilarityIdentical(t *testing.T) {
	v := []float32{1, 0, 0}
	s := CosineSimilarity(v, v)
	if s != 1.0 {
		t.Errorf("expected 1.0 for identical vectors, got %f", s)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	s := CosineSimilarity(a, b)
	if s != 0.0 {
		t.Errorf("expected 0.0 for orthogonal vectors, got %f", s)
	}
}

func TestCosineSimilarityEmpty(t *testing.T) {
	s := CosineSimilarity(nil, nil)
	if s != 0.0 {
		t.Errorf("expected 0.0 for empty vectors, got %f", s)
	}
	s = CosineSimilarity([]float32{1}, []float32{1, 2})
	if s != 0.0 {
		t.Errorf("expected 0.0 for mismatched dims, got %f", s)
	}
}

func TestCosineSimilaritySimilar(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2, 3.1}
	s := CosineSimilarity(a, b)
	if s < 0.99 {
		t.Errorf("expected near-1.0 for very similar vectors, got %f", s)
	}
}

func TestIsDuplicateAboveThreshold(t *testing.T) {
	candidate := []float32{1, 0, 0}
	existing := [][]float32{
		{1, 0, 0},   // identical → dup
		{0, 1, 0},   // orthogonal → not dup
	}
	if !IsDuplicate(candidate, existing, 0.95) {
		t.Error("expected duplicate (identical vector present)")
	}
}

func TestIsDuplicateBelowThreshold(t *testing.T) {
	candidate := []float32{1, 0, 0}
	existing := [][]float32{
		{0, 1, 0}, // orthogonal
		{0, 0, 1}, // orthogonal
	}
	if IsDuplicate(candidate, existing, 0.95) {
		t.Error("expected no duplicate (all orthogonal)")
	}
}

func TestIsDuplicateEmptyExisting(t *testing.T) {
	candidate := []float32{1, 2, 3}
	if IsDuplicate(candidate, nil, 0.9) {
		t.Error("expected no duplicate with empty existing")
	}
}

func TestFindMostSimilar(t *testing.T) {
	candidate := []float32{1, 0, 0}
	existing := [][]float32{
		{0, 1, 0}, // orthogonal → score 0
		{1, 0, 0}, // identical → score 1
		{0, 0, 1}, // orthogonal → score 0
	}
	idx, score := FindMostSimilar(candidate, existing)
	if idx != 1 {
		t.Errorf("expected index 1, got %d", idx)
	}
	if score < 0.999 {
		t.Errorf("expected score ~1.0, got %f", score)
	}
}

func TestFindMostSimilarEmpty(t *testing.T) {
	idx, _ := FindMostSimilar([]float32{1}, nil)
	if idx != -1 {
		t.Errorf("expected -1 for empty, got %d", idx)
	}
}

// TestDedupMergeScenario: two near-duplicate facts should be flagged as dupes.
func TestDedupMergeScenario(t *testing.T) {
	// Simulating embedding similarity: these are close but not identical
	candidate := []float32{0.8, 0.6, 0.1}
	existing := [][]float32{
		{0.81, 0.59, 0.12}, // very close → should merge
	}
	if !IsDuplicate(candidate, existing, 0.95) {
		t.Error("near-duplicate should be flagged as dupe at threshold 0.95")
	}
	sim := CosineSimilarity(candidate, existing[0])
	t.Logf("near-duplicate cosine similarity: %f", sim)
}

// TestDedupDistinctScenario: genuinely different facts should both be kept.
func TestDedupDistinctScenario(t *testing.T) {
	candidate := []float32{0.1, 0.9, 0.4}   // completely different topic
	existing := [][]float32{
		{0.8, 0.1, 0.5}, // different direction
	}
	sim := CosineSimilarity(candidate, existing[0])
	t.Logf("distinct-fact cosine similarity: %f", sim)
	if IsDuplicate(candidate, existing, 0.85) {
		t.Error("genuinely different facts should NOT be flagged as dupes")
	}
}
