package codeast

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mystaline-dev/tastastas/internal/store/sqlite"
	"github.com/mystaline-dev/tastastas/internal/store"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

func TestCodeastIngestGo(t *testing.T) {
	// Create a minimal Go module
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testpkg\n\ngo 1.24\n"), 0644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package testpkg

// Foo does foo
func Foo() string {
	return "foo"
}

// Bar calls Foo
func Bar() string {
	return Foo()
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte(`package testpkg

import "fmt"

type Widget struct {
	Name string
}

// Show prints
func (w *Widget) Show() {
	fmt.Println(w.Name)
}
`), 0644)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := Config{
		Root:      dir,
		ProjectID: "test",
		Languages: []string{"go"},
	}
	ingestor := New(db, cfg)
	nodes, edges, err := ingestor.Ingest(context.Background())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if len(nodes) == 0 {
		t.Fatal("expected nodes, got 0")
	}

	// Should have at least: package, Foo, Bar, Widget, Widget.Show
	want := map[string]bool{
		"test/code:package/testpkg":           false,
		"test/code:function/testpkg.Foo":     false,
		"test/code:function/testpkg.Bar":     false,
		"test/code:type/testpkg.Widget":      false,
	}
	for _, n := range nodes {
		if _, ok := want[n.ID]; ok {
			want[n.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing node: %s", id)
		}
	}

	// Should have at least one "calls" edge (Bar -> Foo)
	hasCall := false
	for _, e := range edges {
		if e.EdgeType == "calls" && e.Confidence == 1.0 {
			hasCall = true
		}
	}
	if !hasCall {
		t.Error("expected at least one calls edge")
	}

	// Should have "defines" edges
	hasDefines := false
	for _, e := range edges {
		if e.EdgeType == "defines" {
			hasDefines = true
		}
	}
	if !hasDefines {
		t.Error("expected at least one defines edge")
	}
}

func TestCodeastUnsupportedLanguage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := Config{
		Root:      t.TempDir(),
		ProjectID: "test",
		Languages: []string{"cobol"},
	}
	ingestor := New(db, cfg)
	_, _, err = ingestor.Ingest(context.Background())
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

var _ store.Store = (*sqlite.Store)(nil)