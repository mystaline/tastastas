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
	"time"
)

// Config holds embedder settings.
type Config struct {
	OllamaURL string // e.g. "http://localhost:11434"
	Model     string // e.g. "nomic-embed-text"
}

// Embedder calls a local Ollama embedding model.
type Embedder struct {
	cfg    Config
	client *http.Client
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
		cfg:    cfg,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns the embedding vector for text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.cfg.Model, Input: text})
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
	if len(out.Embeddings) == 0 {
		return nil, fmt.Errorf("embed: no embeddings returned")
	}
	return out.Embeddings[0], nil
}
