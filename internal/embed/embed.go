// Package embed calls Ollama's /api/embed endpoint to compute text
// embeddings, mirroring the HTTP-call pattern already used in
// internal/extract/extract.go's /api/chat client.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EmbedderBackend is the common interface both the Ollama HTTP embedder and
// the baked ONNX sidecar embedder satisfy. Callers (internal/mcp) depend on
// this instead of a concrete type so the backend is swappable at startup.
type EmbedderBackend interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// Config holds embedder settings.
type Config struct {
	OllamaURL    string // e.g. "http://localhost:11434"
	Model        string // e.g. "nomic-embed-text"
	MaxBatchSize int    // 0 = default 32
}

func (c Config) maxBatch() int {
	if c.MaxBatchSize <= 0 {
		return 32
	}
	return c.MaxBatchSize
}

// ProbeOllamaDim sends a single test embed request to Ollama and returns the
// native output dimension for the given model.
func ProbeOllamaDim(url, model string) (int, error) {
	if url == "" {
		url = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	body, _ := json.Marshal(embedRequest{Model: model, Input: []string{"test"}})
	resp, err := http.Post(strings.TrimRight(url, "/")+"/api/embed", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("ollama probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("ollama probe: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("ollama probe: decode: %w", err)
	}
	if len(out.Embeddings) == 0 {
		return 0, fmt.Errorf("ollama probe: empty embeddings")
	}
	return len(out.Embeddings[0]), nil
}

// Embedder calls a local Ollama embedding model.
type Embedder struct {
	cfg      Config
	client   *http.Client
	maxBatch int
}

// New creates an Embedder with the given config.
func New(cfg Config) *Embedder {
	if cfg.OllamaURL == "" {
		cfg.OllamaURL = "http://localhost:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "nomic-embed-text"
	}
	return &Embedder{
		cfg:      cfg,
		client:   &http.Client{Timeout: 60 * time.Second},
		maxBatch: cfg.maxBatch(),
	}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns the embedding vector for text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embed: no embeddings returned")
	}
	return vecs[0], nil
}

// EmbedBatch embeds texts in a single Ollama /api/embed call.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > e.maxBatch {
		return nil, fmt.Errorf("embed: batch size %d exceeds max %d", len(texts), e.maxBatch)
	}

	body, err := json.Marshal(embedRequest{Model: e.cfg.Model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("embed: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.cfg.OllamaURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: ollama call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed: ollama returned %d: %s", resp.StatusCode, string(b))
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed: expected %d embeddings, got %d", len(texts), len(out.Embeddings))
	}
	return out.Embeddings, nil
}
