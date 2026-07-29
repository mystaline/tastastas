package chunker

import (
	"strings"
	"testing"
)

func TestChunkCodeByPattern_HappyPath_WellFormatted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

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

const DefaultGreeting = "world"
var counter int
`

	chunks, err := ChunkCodeByPattern("test/file.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 4 {
		t.Fatalf("expected >=4 chunks (Fibonacci, Greeter, Greet, const, var), got %d", len(chunks))
	}
	// Verify no chunk exceeds MaxChunkSize (oversize splitting applied)
	for _, c := range chunks {
		if len(c.Content) > cfg.MaxChunkSize+cfg.OverlapSize {
			t.Errorf("chunk %s exceeds max size: %d > %d", c.ID, len(c.Content), cfg.MaxChunkSize)
		}
	}
	// Verify Fibonacci function has its own chunk
	hasFibonacci := false
	for _, c := range chunks {
		if c.HeadingPath[0] == "Fibonacci" {
			hasFibonacci = true
		}
	}
	if !hasFibonacci {
		t.Error("expected chunk with Fibonacci heading")
	}
	// Verify type Greeter chunk
	hasGreeter := false
	hasGreet := false
	for _, c := range chunks {
		if c.HeadingPath[0] == "Greeter" {
			hasGreeter = true
		}
		if c.HeadingPath[0] == "Greet" {
			hasGreet = true
		}
	}
	if !hasGreeter {
		t.Error("expected chunk with Greeter heading")
	}
	if !hasGreet {
		t.Error("expected chunk with Greet heading")
	}
}

func TestChunkCodeByPattern_HappyPath_TypeScript(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	source := `import { Component } from '@angular/core';

@Component({ selector: 'app-root' })
export class AppComponent {
  title = 'my-app';
}

export function add(a: number, b: number): number {
  return a + b;
}

interface User {
  name: string;
  age: number;
}

const PI = 3.14159;
`

	chunks, err := ChunkCodeByPattern("test/file.ts", source, "typescript", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks (AppComponent, add, User, PI), got %d", len(chunks))
	}
}

func TestChunkCodeByPattern_HappyPath_SingleFunction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	source := `package main

func ThisIsTheOnlyFunction() string {
	return "hello"
}
`

	chunks, err := ChunkCodeByPattern("test/single.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}
	hasFunc := false
	for _, c := range chunks {
		if c.HeadingPath[0] == "ThisIsTheOnlyFunction" {
			hasFunc = true
		}
	}
	if !hasFunc {
		t.Error("expected a chunk with heading ThisIsTheOnlyFunction")
	}
}

func TestChunkCodeByPattern_EmptyFile(t *testing.T) {
	chunks, err := ChunkCodeByPattern("test/empty.go", "", "go", DefaultConfig())
	if err != nil {
		t.Fatalf("ChunkCodeByPattern with empty source: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for empty source, got %d", len(chunks))
	}
}

func TestChunkCodeByPattern_TypeScriptExportDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	source := `export default function handler(req, res) {
  res.json({ ok: true });
}

export const metadata = { title: 'Test' };

async function helper() {
  return null;
}
`

	chunks, err := ChunkCodeByPattern("test/handler.ts", source, "typescript", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(chunks))
	}
	hasHandler := false
	hasHelper := false
	for _, c := range chunks {
		if c.HeadingPath[0] == "handler" {
			hasHandler = true
		}
		if c.HeadingPath[0] == "helper" {
			hasHelper = true
		}
	}
	if !hasHandler {
		t.Error("expected chunk with heading handler")
	}
	if !hasHelper {
		t.Error("expected chunk with heading helper")
	}
}

func TestChunkCodeByPattern_GoVendorTranspile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10
	cfg.MaxChunkSize = 2000

	// Simulates a C-to-Go transpiled file: one flat function, few lines, wide braces.
	source := `package sqlite

import "C"
import "unsafe"
import "sync"

// sqlite3_step wraps the C API call.
func Xsqlite3_step(stmt uintptr) int {
	result := C.sqlite3_step((*C.sqlite3_stmt)(unsafe.Pointer(stmt)))
	mu.Lock()
	defer mu.Unlock()
	if result == C.SQLITE_ROW {
		return 1
	}
	if result == C.SQLITE_DONE {
		return 0
	}
	lastErr = int(result)
	return -1
}
`

	chunks, err := ChunkCodeByPattern("test/sqlite.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk for the function, got %d", len(chunks))
	}
	// All chunks must be within max size
	for _, c := range chunks {
		if len(c.Content) > cfg.MaxChunkSize+cfg.OverlapSize {
			t.Errorf("chunk %s exceeds max size: %d > %d", c.ID, len(c.Content), cfg.MaxChunkSize)
		}
	}
}

func TestChunkCodeByPattern_NoFormatting_AllOneLine(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	// Minified code: everything on one line.
	source := `package main; func Add(a,b int) int { return a + b }; func Sub(a,b int) int { return a - b }; const Zero = 0`
	chunks, err := ChunkCodeByPattern("test/minified.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk for minified code")
	}
}

func TestChunkCodeByPattern_NoFormatting_GoBrutal(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	source := `package main
func add(a int,b int)int{
return a+b
}
func sub(a int,b int)int{
return a-b
}
func main(){
println(add(1,2))
}`

	chunks, err := ChunkCodeByPattern("test/brutal.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks for three functions, got %d", len(chunks))
	}
}

func TestChunkCodeByPattern_FormatterVariations(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	tests := []struct {
		name   string
		source string
		min    int // minimum chunks expected
	}{
		{
			name: "gofmt standard",
			source: `package main

func Foo() {
}

func Bar() {
}
`,
			min: 2,
		},
		{
			name: "goimports (extra blank lines between functions)",
			source: `package main

func Foo() {
}


func Bar() {
}
`,
			min: 2,
		},
		{
			name: "no blank lines between functions",
			source: `package main
func Foo() {
}
func Bar() {
}
`,
			min: 2,
		},
		{
			name: "mixed braces (go brace on new line style)",
			source: `package main

func Foo()
{
}

func Bar()
{
}
`,
			min: 2,
		},
		{
			name: "lots of comments and doc strings",
			source: `package main

// PackageDoc is a test.
//
// It has multiple paragraphs and lots of text.

// Foo does something interesting.
//
// It takes no parameters and returns nothing.
func Foo() {
}

// Bar is another function.
//
// Deprecated: use Foo instead.
func Bar() {
}
`,
			min: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := ChunkCodeByPattern("test/file.go", tt.source, "go", cfg)
			if err != nil {
				t.Fatalf("ChunkCodeByPattern: %v", err)
			}
			if len(chunks) < tt.min {
				t.Errorf("expected >=%d chunks, got %d", tt.min, len(chunks))
			}
		})
	}
}

func TestChunkCodeByPattern_EdgeCases(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	tests := []struct {
		name   string
		source string
		min    int
	}{
		{
			name: "generic struct with nested type",
			source: `package main

type Slice[T any] struct {
	items []T
}

func (s *Slice[T]) Add(item T) {
	s.items = append(s.items, item)
}
`,
			min: 2,
		},
		{
			name: "braces in string literals",
			source: `package main

func StringWithBraces() string {
	return "func something() { return 1; }"
}

func Normal() {
}
`,
			min: 2,
		},
		{
			name: "backtick raw strings with braces",
			source: "package main\n\nfunc RawLiteral() string {\n\treturn `package main\nfunc foo() {\n\treturn 1\n}\n`\n}\n\nfunc NormalFunc() {\n}\n",
			min: 2,
		},
		{
			name: "multiple const/var blocks",
			source: `package main

const (
	A = 1
	B = 2
)

const C = 3

var (
	X = "x"
	Y = "y"
)
`,
			min: 3,
		},
		{
			name: "init function",
			source: `package main

func init() {
	register()
}

func init() {
	initDB()
}

func main() {
	run()
}
`,
			min: 3,
		},
		{
			name: "receiver variations (pointer, value, generic)",
			source: `package main

func (s *Store) Get() {}

func (s Store) Set() {}

func (s *Store[T]) Find() {}
`,
			min: 3,
		},
		{
			name: "type alias and type def",
			source: `package main

type Handler func(string) error

type Result struct {
	Value string
	Err   error
}
`,
			min: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := ChunkCodeByPattern("test/file.go", tt.source, "go", cfg)
			if err != nil {
				t.Fatalf("ChunkCodeByPattern: %v", err)
			}
			if len(chunks) < tt.min {
				t.Errorf("expected >=%d chunks, got %d", tt.min, len(chunks))
			}
		})
	}
}

func TestChunkCodeByPattern_TypeScriptEdgeCases(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	tests := []struct {
		name   string
		source string
		min    int
	}{
		{
			name: "arrow functions in class",
			source: `export class ApiClient {
  request = async (url: string) => {
    const res = await fetch(url);
    return res.json();
  };

  get = () => this.request('/data');
}

export function helper() {}
`,
			min: 2,
		},
		{
			name: "decorators and annotations",
			source: `@Injectable({ providedIn: 'root' })
export class DataService {
  api = inject(ApiClient);

  fetch() {}
}

@Component({ selector: 'app-test' })
class TestComponent {}
`,
			min: 2,
		},
		{
			name: "nested functions in TS",
			source: `function outer() {
  function inner() {
    return 1;
  }
  return inner();
}

function standalone() {}
`,
			min: 2,
		},
		{
			name: "type and interface with generics",
			source: `interface Page<T> {
  items: T[];
  total: number;
}

type Nullable<T> = T | null;

function process<T>(data: T): T {
  return data;
}
`,
			min: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := ChunkCodeByPattern("test/file.ts", tt.source, "typescript", cfg)
			if err != nil {
				t.Fatalf("ChunkCodeByPattern: %v", err)
			}
			if len(chunks) < tt.min {
				t.Errorf("expected >=%d chunks, got %d", tt.min, len(chunks))
			}
		})
	}
}

func TestChunkCodeByPattern_OversizeSplitting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxChunkSize = 200
	cfg.MinChunkSize = 10

	// Simulate a very large vendor function (like C-to-Go transpiled).
	var body strings.Builder
	body.WriteString("package main\n\n")
	body.WriteString("func GiantFunction() {\n")
	for i := 0; i < 2000; i++ {
		body.WriteString("\tstatement()\n")
	}
	body.WriteString("}\n")

	source := body.String()

	chunks, err := ChunkCodeByPattern("test/giant.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (oversize split), got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c.Content) > cfg.MaxChunkSize+cfg.OverlapSize {
			t.Errorf("chunk[%d] %s exceeds max size: %d > %d", i, c.ID, len(c.Content), cfg.MaxChunkSize)
		}
	}
}

func TestChunkCodeByPattern_HeadingExtraction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	tests := []struct {
		line    string
		wanted string
	}{
		{"func Get() string", "Get"},
		{"func (s *Store) Get() string", "Get"},
		{"func (s Store[T]) Find()", "Find"},
		{"type User struct", "User"},
		{"type Handler func(string) error", "Handler"},
		{"const DefaultSize = 100", "DefaultSize"},
		{"var counter int", "counter"},
		{"class AppComponent {}", "AppComponent"},
		{"interface Page<T> {}", "Page"},
		{"struct Person {}", "Person"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := derivePatternHeading(tt.line)
			if got != tt.wanted {
				t.Errorf("derivePatternHeading(%q) = %q, want %q", tt.line, got, tt.wanted)
			}
		})
	}
}

func TestChunkCodeByPattern_InitAndMain(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	source := `package main

func init() {
	http.HandleFunc("/health", healthHandler)
}

func main() {
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
`
	chunks, err := ChunkCodeByPattern("test/initmain.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	// Should have 3 chunks: init, main, healthHandler
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks for init+main+handler, got %d", len(chunks))
	}
}

func TestChunkCodeByPattern_ConsecutiveFunctions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	source := `func a() {}
func b() {}
func c() {}
func d() {}
`

	chunks, err := ChunkCodeByPattern("test/consecutive.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 4 {
		t.Fatalf("expected 4 chunks for 4 consecutive functions, got %d", len(chunks))
	}
}

func TestChunkCodeByPattern_FormattingBloat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinChunkSize = 10

	// Excessive blank lines between functions (prettier-style or linter autocorrect).
	source := `package main







func spaced() {}










func alsoSpaced() {}`

	chunks, err := ChunkCodeByPattern("test/spaced.go", source, "go", cfg)
	if err != nil {
		t.Fatalf("ChunkCodeByPattern: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks for spaced functions, got %d", len(chunks))
	}
}
