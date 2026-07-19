package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestGenULIDLength(t *testing.T) {
	id := genULID()
	if len(id) != 26 {
		t.Fatalf("expected 26-char ULID, got %d chars: %q", len(id), id)
	}
}

func TestGenULIDCrockfordAlphabet(t *testing.T) {
	id := genULID()
	for _, c := range id {
		if !strings.ContainsRune(crockfordAlphabet, c) {
			t.Fatalf("ULID contains non-Crockford-Base32 char %q: %s", c, id)
		}
	}
}

func TestGenULIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := genULID()
		if seen[id] {
			t.Fatalf("duplicate ULID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestGenULIDMonotonicByTimestamp(t *testing.T) {
	// ULIDs generated at increasing timestamps should sort lexicographically
	// in the same order (first 10 chars are the timestamp component).
	first := genULID()
	time.Sleep(2 * time.Millisecond)
	second := genULID()

	if first[:10] > second[:10] {
		t.Errorf("expected first ULID timestamp prefix <= second: %s vs %s", first[:10], second[:10])
	}
}
