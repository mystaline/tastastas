// Package embed — sidecar.go wires a baked ONNX embedding binary
// (tastastas-embed, bge-small-en-v1.5) as an alternative to the Ollama
// HTTP embedder. The binary is compiled per-platform under bin/<os>_<arch>/
// and selected at build time via go:embed + runtime.GOOS/GOARCH.
//
// Protocol: newline-delimited JSON over the sidecar's stdin/stdout.
//
//	in:  {"texts": ["a", "b"]}
//	out: {"embeddings": [[...]], "error": null}
//
// One request in flight at a time (mutex-guarded) — the sidecar is a
// single persistent subprocess, not a pool.
package embed

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

//go:embed bin
var sidecarBinFS embed.FS

const sidecarDim = 384

// SidecarConfig controls sidecar subprocess behaviour.
type SidecarConfig struct {
	IntraThreads int // 0 = ONNX default (all cores)
	MaxBatchSize int // 0 = default 32
}

func (c SidecarConfig) intraThreadsArgs() []string {
	if c.IntraThreads <= 0 {
		return nil
	}
	return []string{"--intra-threads", fmt.Sprint(c.IntraThreads)}
}

func (c SidecarConfig) maxBatch() int {
	if c.MaxBatchSize <= 0 {
		return 32
	}
	return c.MaxBatchSize
}

// SidecarEmbedder shells out to the baked ONNX binary for embeddings.
// Implements the same interface as Embedder (Embed / EmbedBatch), so it's
// a drop-in swap wherever *embed.Embedder is used.
type SidecarEmbedder struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	in       io.WriteCloser
	out      *bufio.Reader
	binPath  string
	maxBatch int
}

type sidecarRequest struct {
	Texts []string `json:"texts"`
}

type sidecarResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      *string     `json:"error"`
}

// extractSidecarBin extracts the platform-appropriate baked binary to an
// OS temp dir and returns the path. The binary is extracted once and shared
// across all pool workers — each calls startSidecar with the same binPath.
func extractSidecarBin() (string, error) {
	binName := fmt.Sprintf("bin/%s_%s/tastastas-embed", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	data, err := sidecarBinFS.ReadFile(binName)
	if err != nil {
		return "", fmt.Errorf("embed: no baked sidecar for %s/%s: %w", runtime.GOOS, runtime.GOARCH, err)
	}
	tmpDir, err := os.MkdirTemp("", "tastastas-embed-")
	if err != nil {
		return "", fmt.Errorf("embed: create sidecar tempdir: %w", err)
	}
	binPath := filepath.Join(tmpDir, filepath.Base(binName))
	if err := os.WriteFile(binPath, data, 0o755); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("embed: write sidecar binary: %w", err)
	}
	return binPath, nil
}

// NewSidecar extracts the baked binary to a temp file and starts it as
// a persistent subprocess. For pool usage, prefer extractSidecarBin + startSidecar
// to avoid extracting the binary N times.
func NewSidecar() (*SidecarEmbedder, error) {
	return NewSidecarWithConfig(SidecarConfig{})
}

// NewSidecarWithConfig creates a sidecar with the given config.
func NewSidecarWithConfig(cfg SidecarConfig) (*SidecarEmbedder, error) {
	binPath, err := extractSidecarBin()
	if err != nil {
		return nil, err
	}
	return startSidecar(binPath, cfg)
}

// startSidecar starts a sidecar subprocess from an existing binary path.
// Used by SidecarPool to spawn N workers from one extracted binary.
func startSidecar(binPath string, cfg SidecarConfig) (*SidecarEmbedder, error) {
	args := cfg.intraThreadsArgs()
	cmd := exec.Command(binPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("embed: sidecar stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("embed: sidecar stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("embed: start sidecar: %w", err)
	}

	return &SidecarEmbedder{
		cmd:      cmd,
		in:       stdin,
		out:      bufio.NewReader(stdout),
		binPath:  binPath,
		maxBatch: cfg.maxBatch(),
	}, nil
}

// Close terminates the sidecar subprocess and removes its temp binary.
func (s *SidecarEmbedder) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.in.Close()
	err := s.cmd.Wait()
	os.RemoveAll(filepath.Dir(s.binPath))
	return err
}

// Embed returns the embedding vector for a single text.
func (s *SidecarEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := s.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embed: sidecar returned no embeddings")
	}
	return vecs[0], nil
}

// EmbedBatch embeds up to maxBatch texts in a single sidecar round-trip.
func (s *SidecarEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > s.maxBatch {
		return nil, fmt.Errorf("embed: batch size %d exceeds max %d", len(texts), s.maxBatch)
	}

	// Lock with context awareness — TryLock polling loop avoids blocking
	// indefinitely when context is cancelled (e.g. recall timeout).
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.mu.TryLock() {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	defer s.mu.Unlock()

	body, err := json.Marshal(sidecarRequest{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal sidecar request: %w", err)
	}
	body = append(bytes.TrimRight(body, "\n"), '\n')

	// Write with context check
	done := make(chan error, 1)
	go func() {
		_, err := s.in.Write(body)
		done <- err
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("embed: write to sidecar: %w", err)
		}
	}

	// Read with context check
	type readResult struct {
		line []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		line, err := s.out.ReadBytes('\n')
		readCh <- readResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("embed: sidecar read timeout (30s) — worker may have died")
	case r := <-readCh:
		if r.err != nil {
			return nil, fmt.Errorf("embed: read from sidecar: %w", r.err)
		}
		var resp sidecarResponse
		if err := json.Unmarshal(r.line, &resp); err != nil {
			return nil, fmt.Errorf("embed: decode sidecar response: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("embed: sidecar error: %s", *resp.Error)
		}
		if len(resp.Embeddings) != len(texts) {
			return nil, fmt.Errorf("embed: expected %d embeddings, got %d", len(texts), len(resp.Embeddings))
		}
		return resp.Embeddings, nil
	}
}
