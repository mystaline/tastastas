package chunker

import (
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	ts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

func TestChunkGoCode(t *testing.T) {
	goLang := sitter.NewLanguage(golang.Language())

	source := `package main

// Fibonacci returns the nth Fibonacci number.
func Fibonacci(n int) int {
	if n <= 1 {
		return n
	}
	return Fibonacci(n-1) + Fibonacci(n-2)
}

// Greeter is a simple greeting service.
type Greeter struct {
	Name string
}

func (g *Greeter) Greet() string {
	return "Hello, " + g.Name
}
`

	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	chunks, err := ChunkGoCode("test/file.go", source, goLang, cfg)
	if err != nil {
		t.Fatalf("ChunkGoCode: %v", err)
	}

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (Fibonacci func, Greeter struct, Greet method), got %d", len(chunks))
	}

	if chunks[0].Type != ChunkTypeCodeFunction {
		t.Errorf("chunk 0: expected code_function, got %s", chunks[0].Type)
	}
	if chunks[0].Language != "go" {
		t.Errorf("chunk 0: expected language=go, got %s", chunks[0].Language)
	}
	t.Logf("chunk 0: heading=%q start=%d end=%d", chunks[0].HeadingPath[0], chunks[0].StartLine, chunks[0].EndLine)

	if chunks[1].Type != ChunkTypeCodeStruct {
		t.Errorf("chunk 1: expected code_struct, got %s", chunks[1].Type)
	}
	t.Logf("chunk 1: heading=%q start=%d end=%d", chunks[1].HeadingPath[0], chunks[1].StartLine, chunks[1].EndLine)

	if chunks[2].Type != ChunkTypeCodeFunction {
		t.Errorf("chunk 2: expected code_function, got %s", chunks[2].Type)
	}
	t.Logf("chunk 2: heading=%q start=%d end=%d", chunks[2].HeadingPath[0], chunks[2].StartLine, chunks[2].EndLine)
}

func TestChunkTypeScript(t *testing.T) {
	tsLang := sitter.NewLanguage(ts.LanguageTypescript())

	source := `
// Greet returns a greeting message.
function greet(name: string): string {
	return "Hello, " + name
}

// User represents a system user.
class User {
	name: string

	constructor(n: string) {
		this.name = n
	}

	greet(): string {
		return "Hi, " + this.name
	}
}

// Sum adds two numbers.
export function sum(a: number, b: number): number {
	return a + b
}
`

	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	chunks, err := ChunkTypeScript("test/file.ts", source, tsLang, cfg)
	if err != nil {
		t.Fatalf("ChunkTypeScript: %v", err)
	}

	// Expect: greet (func), User (class), sum (exported func) = 3
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (greet, User, sum), got %d", len(chunks))
	}

	for i, c := range chunks {
		t.Logf("chunk %d: heading=%q type=%s language=%s start=%d end=%d",
			i, c.HeadingPath[0], c.Type, c.Language, c.StartLine, c.EndLine)
	}

	if chunks[0].HeadingPath[0] != "greet" {
		t.Errorf("chunk 0: expected heading 'greet', got %q", chunks[0].HeadingPath[0])
	}
	if chunks[1].HeadingPath[0] != "User" {
		t.Errorf("chunk 1: expected heading 'User', got %q", chunks[1].HeadingPath[0])
	}
	if chunks[1].Type != ChunkTypeCodeStruct {
		t.Errorf("chunk 1: expected code_struct for class, got %s", chunks[1].Type)
	}
	if chunks[2].HeadingPath[0] != "sum" {
		t.Errorf("chunk 2: expected heading 'sum', got %q", chunks[2].HeadingPath[0])
	}
}
