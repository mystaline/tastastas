package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

type OpenAIEmbedder struct {
	apiKey   string
	model    string
	baseURL  string
	dim      int
	client   *http.Client
	maxBatch int
}

type openaiEmbedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openaiEmbedData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type openaiEmbedUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type openaiEmbedResponse struct {
	Object string            `json:"object"`
	Data   []openaiEmbedData `json:"data"`
	Model  string            `json:"model"`
	Usage  openaiEmbedUsage  `json:"usage"`
}

// ProbeOpenAIDim sends a single test embed request (no dimensions param)
// and returns the native output dimension for the given model.
func ProbeOpenAIDim(apiKey, model, baseURL string) (int, error) {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	body, _ := json.Marshal(openaiEmbedRequest{Model: model, Input: []string{"test"}})
	req, err := http.NewRequest("POST", strings.TrimRight(baseURL, "/")+"/embeddings",
		bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("openai probe: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("openai probe: request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("openai probe: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out openaiEmbedResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("openai probe: decode: %w", err)
	}
	if len(out.Data) == 0 {
		return 0, fmt.Errorf("openai probe: empty data")
	}
	return len(out.Data[0].Embedding), nil
}

func NewOpenAI(apiKey, model, baseURL string, dim, maxBatchSize int) *OpenAIEmbedder {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if dim <= 0 {
		dim = 1536
	}
	if maxBatchSize <= 0 {
		maxBatchSize = 2048
	}
	return &OpenAIEmbedder{
		apiKey:   apiKey,
		model:    model,
		baseURL:  strings.TrimRight(baseURL, "/"),
		dim:      dim,
		maxBatch: maxBatchSize,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := o.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("openai: no embeddings returned")
	}
	return vecs[0], nil
}

func (o *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > o.maxBatch {
		return nil, fmt.Errorf("openai: batch size %d exceeds max %d", len(texts), o.maxBatch)
	}

	body, err := json.Marshal(openaiEmbedRequest{
		Model:      o.model,
		Input:      texts,
		Dimensions: o.dim,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out openaiEmbedResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai: expected %d embeddings, got %d", len(texts), len(out.Data))
	}

	sort.Slice(out.Data, func(i, j int) bool {
		return out.Data[i].Index < out.Data[j].Index
	})

	result := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		if len(d.Embedding) != o.dim {
			return nil, fmt.Errorf("openai: data[%d] has dim %d, expected %d",
				i, len(d.Embedding), o.dim)
		}
		result[i] = d.Embedding
	}

	log.Printf("openai: embedded %d texts (%d tokens)", len(texts), out.Usage.TotalTokens)
	return result, nil
}

func (o *OpenAIEmbedder) Close() error { return nil }
