package embed

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAIDefaults(t *testing.T) {
	o := NewOpenAI("sk-test", "", "", 0, 0)
	if o.model != "text-embedding-3-small" {
		t.Errorf("expected default model text-embedding-3-small, got %q", o.model)
	}
	if o.baseURL != "https://api.openai.com/v1" {
		t.Errorf("expected default base URL, got %q", o.baseURL)
	}
	if o.dim != 1536 {
		t.Errorf("expected default dim 1536, got %d", o.dim)
	}
}

func TestOpenAIEmbedRequestMarshal(t *testing.T) {
	req := openaiEmbedRequest{
		Model:      "text-embedding-3-small",
		Input:      []string{"hello", "world"},
		Dimensions: 1536,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["model"] != "text-embedding-3-small" {
		t.Errorf("bad model: %v", m["model"])
	}
	inputs, ok := m["input"].([]any)
	if !ok || len(inputs) != 2 || inputs[0] != "hello" || inputs[1] != "world" {
		t.Errorf("bad input: %v", m["input"])
	}
	dims, ok := m["dimensions"].(float64)
	if !ok || int(dims) != 1536 {
		t.Errorf("bad dimensions: %v", m["dimensions"])
	}
}

func TestOpenAIEmbedBatchRejectsOversized(t *testing.T) {
	o := NewOpenAI("sk-test", "", "", 0, 2048)
	texts := make([]string, 2049)
	_, err := o.EmbedBatch(nil, texts)
	if err == nil {
		t.Fatal("expected error for batch size > 2048, got nil")
	}
	if !strings.Contains(err.Error(), "2048") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAIEmbedBatchEmpty(t *testing.T) {
	o := NewOpenAI("sk-test", "", "", 0, 0)
	vecs, err := o.EmbedBatch(nil, nil)
	if err != nil {
		t.Fatalf("expected no error for nil input, got %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil result for empty batch, got %v", vecs)
	}
	vecs, err = o.EmbedBatch(nil, []string{})
	if err != nil {
		t.Fatalf("expected no error for empty input, got %v", err)
	}
	if vecs != nil {
		t.Errorf("expected nil result for empty batch, got %v", vecs)
	}
}

func TestOpenAIDimParam(t *testing.T) {
	req := openaiEmbedRequest{
		Model:      "text-embedding-3-small",
		Input:      []string{"test"},
		Dimensions: 256,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"dimensions":256`) {
		t.Fatalf("expected dimensions=256 in JSON, got %s", b)
	}
}
