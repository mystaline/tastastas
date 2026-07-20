package embed

import (
	"context"
	"math"
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
