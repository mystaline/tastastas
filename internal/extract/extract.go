// Package extract extracts atomic facts and entities from conversation text
// via an LLM call. The prompt template and output parsing are ported from
// prototype/scoring.py after threshold calibration against real transcripts.
package extract

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

// Fact is one extracted atomic unit of memory from a conversation.
type Fact struct {
	Kind       string  `json:"kind"`       // "fact" or "entity"
	Title      string  `json:"title"`      // short label, <= 8 words
	Content    string  `json:"content"`    // one self-contained sentence
	Importance float64 `json:"importance"` // 0..1
}

// Config holds extraction settings.
type Config struct {
	OllamaURL  string  // e.g. "http://localhost:11434"
	Model      string  // e.g. "llama3.2"
	MaxFacts   int     // 0 = use default (3)
	MinImportance float64 // discard facts below this (0 = keep all)
}

// Extractor calls a local LLM to pull structured facts from conversation text.
type Extractor struct {
	cfg    Config
	client *http.Client
}

// New creates an Extractor with the given config.
func New(cfg Config) *Extractor {
	if cfg.OllamaURL == "" {
		cfg.OllamaURL = "http://localhost:11434"
	}
	if cfg.Model == "" {
		cfg.Model = "llama3.2"
	}
	if cfg.MaxFacts == 0 {
		cfg.MaxFacts = 3
	}
	return &Extractor{
		cfg:    cfg,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

const systemPrompt = `You extract discrete, atomic facts from a conversation snippet for long-term memory storage. Output ONLY a JSON array, no prose. Each item:
{
  "kind": "fact" | "entity",
  "title": "short label, <= 8 words",
  "content": "one self-contained sentence, no pronouns referring outside itself",
  "importance": 0.0-1.0  (0.9+ = identity/durable preference/hard constraint,
                          0.5 = useful context, 0.2 = trivial/likely stale soon)
}
Skip anything that is: a question, a task-in-progress, small talk, or already-obvious-from-context filler. Prefer 0-3 high quality facts over many weak ones.`

// Extract runs the LLM against the given conversation snippet and returns
// parsed facts. Returns empty slice (not nil) on LLM error or empty output.
func (e *Extractor) Extract(ctx context.Context, conversation string) ([]Fact, error) {
	payload := ollamaRequest{
		Model: e.cfg.Model,
		Messages: []ollamaMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: conversation},
		},
		Stream: false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("extract: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.cfg.OllamaURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("extract: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extract: ollama call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("extract: ollama returned %d: %s", resp.StatusCode, string(b))
	}

	var ollamaResp ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("extract: decode response: %w", err)
	}

	facts, err := parseFacts(ollamaResp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("extract: parse output: %w", err)
	}

	// Filter by minimum importance
	if e.cfg.MinImportance > 0 {
		filtered := facts[:0]
		for _, f := range facts {
			if f.Importance >= e.cfg.MinImportance {
				filtered = append(filtered, f)
			}
		}
		facts = filtered
	}

	// Cap at MaxFacts
	if len(facts) > e.cfg.MaxFacts {
		facts = facts[:e.cfg.MaxFacts]
	}

	return facts, nil
}

// parseFacts extracts a JSON array from LLM output that may contain
// markdown fences or trailing prose.
func parseFacts(raw string) ([]Fact, error) {
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences if present
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		start, end := 0, len(lines)
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				if start == 0 {
					start = i + 1
				} else {
					end = i
				}
			}
		}
		if start > 0 {
			raw = strings.Join(lines[start:end], "\n")
		}
	}

	// Find the JSON array boundaries
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array found in LLM output: %q", truncate(raw, 200))
	}
	raw = raw[start : end+1]

	var facts []Fact
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w (raw: %q)", err, truncate(raw, 200))
	}

	// Validate and normalize
	for i := range facts {
		facts[i].Kind = strings.ToLower(strings.TrimSpace(facts[i].Kind))
		if facts[i].Kind != "fact" && facts[i].Kind != "entity" {
			facts[i].Kind = "fact" // default
		}
		if facts[i].Importance < 0 {
			facts[i].Importance = 0
		}
		if facts[i].Importance > 1 {
			facts[i].Importance = 1
		}
	}

	return facts, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Ollama API types ---

type ollamaRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}
