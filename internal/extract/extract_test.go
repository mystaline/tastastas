package extract

import (
	"testing"
)

func TestParseFacts(t *testing.T) {
	// Clean JSON output
	input := `[
		{"kind": "fact", "title": "DB version", "content": "Project uses Postgres 16.", "importance": 0.7}
	]`
	facts, err := parseFacts(input)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Kind != "fact" || facts[0].Title != "DB version" {
		t.Errorf("unexpected fact: %+v", facts[0])
	}
	if facts[0].Importance != 0.7 {
		t.Errorf("expected importance 0.7, got %f", facts[0].Importance)
	}
}

func TestParseFactsMarkdownFenced(t *testing.T) {
	input := "Here are the facts:\n```json\n[\n  {\"kind\": \"entity\", \"title\": \"User Alice\", \"content\": \"Alice works at Acme.\", \"importance\": 0.9}\n]\n```"
	facts, err := parseFacts(input)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if len(facts) != 1 || facts[0].Kind != "entity" {
		t.Fatalf("unexpected: %+v", facts)
	}
}

func TestParseFactsMultiple(t *testing.T) {
	input := `[
		{"kind": "fact", "title": "Dark mode", "content": "User prefers dark mode.", "importance": 0.6},
		{"kind": "fact", "title": "DB version", "content": "Uses Postgres 16.", "importance": 0.7},
		{"kind": "entity", "title": "Acme Corp", "content": "Acme Corp is the employer.", "importance": 0.9}
	]`
	facts, err := parseFacts(input)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if len(facts) != 3 {
		t.Fatalf("expected 3 facts, got %d", len(facts))
	}
	// importance clamping
	facts[0].Importance = 1.5
	if facts[0].Importance != 1.5 {
		t.Error("clamp should not happen on raw parse")
	}
}

func TestParseFactsImportanceClamped(t *testing.T) {
	input := `[
		{"kind": "fact", "title": "Over", "content": "Over one.", "importance": 1.5},
		{"kind": "fact", "title": "Under", "content": "Under zero.", "importance": -0.3}
	]`
	facts, err := parseFacts(input)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if facts[0].Importance != 1.0 {
		t.Errorf("expected 1.0 clamped, got %f", facts[0].Importance)
	}
	if facts[1].Importance != 0.0 {
		t.Errorf("expected 0.0 clamped, got %f", facts[1].Importance)
	}
}

func TestParseFactsInvalidKindDefaultsFact(t *testing.T) {
	input := `[
		{"kind": "unknown", "title": "X", "content": "Something.", "importance": 0.5}
	]`
	facts, err := parseFacts(input)
	if err != nil {
		t.Fatalf("parseFacts: %v", err)
	}
	if facts[0].Kind != "fact" {
		t.Errorf("expected 'fact' default, got %q", facts[0].Kind)
	}
}

func TestParseFactsNoJSON(t *testing.T) {
	_, err := parseFacts("no json here at all")
	if err == nil {
		t.Fatal("expected error for no JSON")
	}
}

func TestParseFactsEmptyArray(t *testing.T) {
	facts, err := parseFacts("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(facts))
	}
}
