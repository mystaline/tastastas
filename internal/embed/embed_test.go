package embed

import (
	"encoding/json"
	"testing"
)

// TestEmbedRequestMarshal is a lightweight ad-hoc check of wire shape —
// no network call, just confirms the JSON keys Ollama's /api/embed expects.
func TestEmbedRequestMarshal(t *testing.T) {
	b, err := json.Marshal(embedRequest{Model: "nomic-embed-text", Input: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["model"] != "nomic-embed-text" || m["input"] != "hello" {
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
