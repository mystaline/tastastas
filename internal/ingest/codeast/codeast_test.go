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

// TestCodeastSameNamedMethodsDifferentReceivers is a regression test for
// the duplicate-ID bug: two types in the same package each with a method
// of the same name (Close) used to collide on the same node ID
// "{pkgpath}.Close", silently dropping one method's node and later
// tripping a UNIQUE constraint when both were chunked. Each method must
// now get a distinct, receiver-qualified ID and its own "calls" edges.
func TestCodeastSameNamedMethodsDifferentReceivers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testpkg\n\ngo 1.24\n"), 0644)
	os.WriteFile(filepath.Join(dir, "db.go"), []byte(`package testpkg

type DB struct{}

func (d *DB) Close() error { return dbClose() }
func dbClose() error { return nil }

type Conn struct{}

func (c *Conn) Close() error { return connClose() }
func connClose() error { return nil }
`), 0644)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(context.Background(), dbPath, 8)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := Config{Root: dir, ProjectID: "test", Languages: []string{"go"}}
	ingestor := New(db, cfg)
	nodes, edges, err := ingestor.Ingest(context.Background())
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	wantIDs := []string{
		"test/code:function/testpkg.DB.Close",
		"test/code:function/testpkg.Conn.Close",
	}
	seen := map[string]int{}
	for _, n := range nodes {
		seen[n.ID]++
	}
	for _, id := range wantIDs {
		if seen[id] != 1 {
			t.Errorf("expected exactly one node %s, got %d", id, seen[id])
		}
	}

	// Each Close() must call its own package-level helper — proves the
	// call-edge "from" side also resolved to the receiver-qualified ID,
	// not a shared bare-name ID.
	wantCalls := map[string]string{
		"test/code:function/testpkg.DB.Close":   "test/code:function/testpkg.dbClose",
		"test/code:function/testpkg.Conn.Close": "test/code:function/testpkg.connClose",
	}
	gotCalls := map[string]string{}
	for _, e := range edges {
		if e.EdgeType == "calls" {
			gotCalls[e.FromID] = e.ToID
		}
	}
	for from, wantTo := range wantCalls {
		if gotCalls[from] != wantTo {
			t.Errorf("calls edge from %s: want to=%s, got to=%s", from, wantTo, gotCalls[from])
		}
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