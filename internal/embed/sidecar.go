// Package embed — sidecar.go wires a baked ONNX embedding binary
// (tastastas-embed, bge-small-en-v1.5) as an alternative to the Ollama
// HTTP embedder. The binary is compiled per-platform under bin/<os>_<arch>/
// and selected at build time via go:embed + runtime.GOOS/GOARCH.
//
// Protocol: newline-delimited JSON over the sidecar's stdin/stdout.
//   in:  {"texts": ["a", "b"]}
//   out: {"embeddings": [[...]], "error": null}
//
// One request in flight at a time (mutex-guarded) — the sidecar is a
// single persistent subprocess, not a pool. Batches of up to 32 chunks
// keep this fast enough in practice (ingest already batches at 32).
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
)

//go:embed bin
var sidecarBinFS embed.FS

const sidecarDim = 384

// SidecarEmbedder shells out to the baked ONNX binary for embeddings.
// Implements the same interface as Embedder (Embed / EmbedBatch), so it's
// a drop-in swap wherever *embed.Embedder is used.
type SidecarEmbedder struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *bufio.Reader
	binPath string
}

type sidecarRequest struct {
	Texts []string `json:"texts"`
}

type sidecarResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      *string     `json:"error"`
}

// NewSidecar extracts the platform-appropriate baked binary to a temp file
// and starts it as a persistent subprocess. Returns an error if this
// platform has no baked binary (caller should fall back to Ollama).
func NewSidecar() (*SidecarEmbedder, error) {
	binName := fmt.Sprintf("bin/%s_%s/tastastas-embed", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}

	data, err := sidecarBinFS.ReadFile(binName)
	if err != nil {
		return nil, fmt.Errorf("embed: no baked sidecar for %s/%s: %w", runtime.GOOS, runtime.GOARCH, err)
	}

	tmpDir, err := os.MkdirTemp("", "tastastas-embed-")
	if err != nil {
		return nil, fmt.Errorf("embed: create sidecar tempdir: %w", err)
	}
	binPath := filepath.Join(tmpDir, filepath.Base(binName))
	if err := os.WriteFile(binPath, data, 0o755); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("embed: write sidecar binary: %w", err)
	}

	cmd := exec.Command(binPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("embed: sidecar stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("embed: sidecar stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("embed: start sidecar: %w", err)
	}

	return &SidecarEmbedder{
		cmd:     cmd,
		in:      stdin,
		out:     bufio.NewReader(stdout),
		binPath: binPath,
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

// EmbedBatch embeds up to 32 texts in a single sidecar round-trip.
func (s *SidecarEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > 32 {
		return nil, fmt.Errorf("embed: batch size %d exceeds max 32", len(texts))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	body, err := json.Marshal(sidecarRequest{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal sidecar request: %w", err)
	}
	body = append(bytes.TrimRight(body, "\n"), '\n')

	if _, err := s.in.Write(body); err != nil {
		return nil, fmt.Errorf("embed: write to sidecar: %w", err)
	}

	line, err := s.out.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("embed: read from sidecar: %w", err)
	}

	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
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
