package embed

import (
	"context"
	"fmt"
	"time"
)

// SidecarPool manages N SidecarEmbedder workers for parallel embedding.
type SidecarPool struct {
	workers []*SidecarEmbedder
	next    int // round-robin
}

// NewSidecarPool creates a pool of N workers (defaults to runtime.NumCPU()).
// Extracts the baked ONNX binary once and spawns N subprocesses from the
// same path — avoids writing 150 MB × numCPU to temp.
func NewSidecarPool(n int) (*SidecarPool, error) {
	if n <= 0 {
		n = 4
	}
	binPath, err := extractSidecarBin()
	if err != nil {
		return nil, err
	}
	workers := make([]*SidecarEmbedder, n)
	for i := 0; i < n; i++ {
		w, err := startSidecar(binPath)
		if err != nil {
			for j := 0; j < i; j++ {
				_ = workers[j].Close()
			}
			return nil, fmt.Errorf("sidecar pool: worker %d: %w", i, err)
		}
		workers[i] = w
	}
	return &SidecarPool{workers: workers}, nil
}

// EmbedBatch distributes the batch across workers in round-robin fashion.
// Each worker gets at most ceil(len(texts)/len(workers)) texts.
// Respects context cancellation — if ctx is cancelled, all in-flight workers
// are abandoned and the error is returned immediately.
func (p *SidecarPool) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > 32*len(p.workers) {
		return nil, fmt.Errorf("embed: total batch size %d exceeds pool capacity %d", len(texts), 32*len(p.workers))
	}

	// Per-batch deadline prevents a single dead worker from hanging
	// the pool — callers pass context.Background() so ctx.Done() alone
	// never fires.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	type result struct {
		idx int
		vec [][]float32
		err error
	}
	resCh := make(chan result, len(p.workers))

	collected := 0
	chunkSize := (len(texts) + len(p.workers) - 1) / len(p.workers)
	start := 0
	for i, w := range p.workers {
		end := start + chunkSize
		if end > len(texts) {
			end = len(texts)
		}
		if start >= end {
			break
		}
		collected++
		sub := texts[start:end]
		start = end
		go func(idx int, worker *SidecarEmbedder, batch []string) {
			vecs, err := worker.EmbedBatch(ctx, batch)
			resCh <- result{idx: idx, vec: vecs, err: err}
		}(i, w, sub)
	}

	results := make([]result, collected)
	var firstErr error
	for i := 0; i < collected; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-resCh:
			if r.err != nil && firstErr == nil {
				firstErr = r.err
			}
			if r.idx < collected {
				results[r.idx] = r
			}
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}

	// Concatenate in original order
	var out [][]float32
	for _, r := range results {
		out = append(out, r.vec...)
	}
	return out, nil
}

// Embed delegates to the next worker (round-robin).
func (p *SidecarPool) Embed(ctx context.Context, text string) ([]float32, error) {
	w := p.workers[p.next%len(p.workers)]
	p.next++
	return w.Embed(ctx, text)
}

// Close shuts down all workers.
func (p *SidecarPool) Close() error {
	var firstErr error
	for _, w := range p.workers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}