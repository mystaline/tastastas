package embed

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEmbedRequestMarshal is a lightweight ad-hoc check of wire shape —
// no network call, just confirms the JSON keys Ollama's /api/embed expects.
func TestEmbedRequestMarshal(t *testing.T) {
	b, err := json.Marshal(embedRequest{Model: "nomic-embed-text", Input: []string{"hello", "world"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inputs, ok := m["input"].([]any)
	if m["model"] != "nomic-embed-text" || !ok || len(inputs) != 2 || inputs[0] != "hello" || inputs[1] != "world" {
		t.Errorf("unexpected wire shape: %s", b)
	}
}

func TestNewDefaults(t *testing.T) {
	e := New(Config{})
	if e.cfg.OllamaURL != "http://localhost:11434" {
		t.Errorf("expected default OllamaURL, got %q", e.cfg.OllamaURL)
	}
	if e.cfg.Model != "nomic-embed-text" {
		t.Errorf("expected default model, got %q", e.cfg.Model)
	}
}

func TestEmbedBatchRejectsOversized(t *testing.T) {
	e := New(Config{})
	texts := make([]string, 33)
	for i := range texts {
		texts[i] = "x"
	}
	_, err := e.EmbedBatch(context.Background(), texts)
	if err == nil {
		t.Fatal("expected error for batch size > 32, got nil")
	}
}

func TestEmbedBatchEmpty(t *testing.T) {
	e := New(Config{})
	vecs, err := e.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error for empty batch, got %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil result for empty batch, got %v", vecs)
	}
}
