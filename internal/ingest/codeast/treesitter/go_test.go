package treesitter

import (
	"strings"
	"testing"
)

// TestGoExtMethodReceiverDisambiguation is the tree-sitter fallback's guard
// for the duplicate-ID bug: two types in the same package each with a method
// of the same name must get distinct, receiver-qualified node IDs.
func TestGoExtMethodReceiverDisambiguation(t *testing.T) {
	src := `package testpkg

type DB struct{}

func (d *DB) Close() error { return nil }

type Conn struct{}

func (c *Conn) Close() error { return nil }

func (c Conn) Open() error { return nil }
`
	nodes, _, _, err := Extract("test", "db.go", []byte(src), "go", &GoExt{}, ".")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var methodIDs []string
	for _, n := range nodes {
		if n.NodeType == "code:method" {
			methodIDs = append(methodIDs, n.ID)
		}
	}

	want := map[string]bool{
		"test/code:method/db.go.DB.Close":   false,
		"test/code:method/db.go.Conn.Close": false,
		"test/code:method/db.go.Conn.Open":  false,
	}
	for _, id := range methodIDs {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing method node: %s (got %v)", id, methodIDs)
		}
	}

	seen := map[string]int{}
	for _, id := range methodIDs {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("method node %s duplicated %d times", id, n)
		}
	}
}

// TestGoExtPrimitiveTypes ensures built-ins don't emit reference edges.
func TestGoExtPrimitiveTypes(t *testing.T) {
	src := `package testpkg

type Widget struct {
	Name string
	Size int
}

func New() *Widget { return nil }
`
	nodes, edges, _, err := Extract("test", "w.go", []byte(src), "go", &GoExt{}, ".")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if len(nodes) == 0 {
		t.Fatal("expected nodes")
	}
	for _, e := range edges {
		if e.EdgeType == "references" && strings.HasPrefix(e.ToID, "test/code:type/w.") {
			t.Errorf("built-in type should not produce reference edge: %s", e.ToID)
		}
	}
}
