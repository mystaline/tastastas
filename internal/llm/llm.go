// Package llm provides LLM client abstraction for Tier 1/3 linking.
// Reuses the same Ollama HTTP pattern as internal/extract.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Option struct {
	Model  string // override default model
	Format any    // JSON schema for structured output
	MaxTok int    // 0 = no override
}

type Client interface {
	Chat(ctx context.Context, msgs []Message, opts ...Option) (string, error)
}

type OllamaClient struct {
	endpoint string
	model    string
	client   *http.Client
}

func NewOllama(endpoint, model string) *OllamaClient {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	if model == "" {
		model = "qwen3.5:2b-q4_K_M"
	}
	return &OllamaClient{
		endpoint: endpoint,
		model:    model,
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *OllamaClient) Chat(ctx context.Context, msgs []Message, opts ...Option) (string, error) {
	model := c.model
	var format any
	for _, o := range opts {
		if o.Model != "" {
			model = o.Model
		}
		if o.Format != nil {
			format = o.Format
		}
	}

	req := ollamaChatRequest{
		Model:    model,
		Messages: msgs,
		Stream:   false,
		Think:    false, // mandatory for qwen3.5 family
	}

	if format != nil {
		req.Format = format
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("llm: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: create req: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("llm: call: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("llm: %s returned %d: %s", model, resp.StatusCode, string(b))
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return "", fmt.Errorf("llm: decode: %w", err)
	}

	return ollamaResp.Message.Content, nil
}

type ollamaChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Think    bool      `json:"think"`
	Format   any       `json:"format,omitempty"`
}

type ollamaChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}
