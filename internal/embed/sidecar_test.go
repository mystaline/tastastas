package embed

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestSidecarEmbedRealBinary(t *testing.T) {
	s, err := NewSidecar()
	if err != nil {
		t.Skipf("no sidecar binary for this platform: %v", err)
	}
	defer s.Close()

	vec, err := s.Embed(context.Background(), "JWT validation function")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != sidecarDim {
		t.Fatalf("expected dim %d, got %d", sidecarDim, len(vec))
	}

	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm < 0.99 || norm > 1.01 {
		t.Errorf("expected L2-normalized vector (norm~1.0), got %f", norm)
	}
}

func TestSidecarEmbedBatchRealBinary(t *testing.T) {
	s, err := NewSidecar()
	if err != nil {
		t.Skipf("no sidecar binary for this platform: %v", err)
	}
	defer s.Close()

	vecs, err := s.EmbedBatch(context.Background(), []string{"hello world", "JWT validation", "goodbye"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != sidecarDim {
			t.Errorf("vec %d: expected dim %d, got %d", i, sidecarDim, len(v))
		}
	}
}

// TestSidecarEmbedLongTextRealBinary is a regression test for a bug where
// text tokenizing to >512 tokens (BERT's absolute position embedding limit)
// crashed onnxruntime with a broadcast/shape error instead of being
// truncated. A single long chunk (or a whole undersized source file with no
// heading splits) reliably exceeded this before the fix.
func TestSidecarEmbedLongTextRealBinary(t *testing.T) {
	s, err := NewSidecar()
	if err != nil {
		t.Skipf("no sidecar binary for this platform: %v", err)
	}
	defer s.Close()

	long := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 200) // ~2000 words, well past 512 tokens
	vec, err := s.Embed(context.Background(), long)
	if err != nil {
		t.Fatalf("Embed(long text): %v", err)
	}
	if len(vec) != sidecarDim {
		t.Fatalf("expected dim %d, got %d", sidecarDim, len(vec))
	}
}
