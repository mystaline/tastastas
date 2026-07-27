package onboard

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/mystaline-dev/tastastas/internal/store"
)

func TestNamePrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{ "GetUserByID", "Get" },
		{ "handleRequest", "handle" },
		{ "NewServer", "New" },
		{ "isValid", "is" },
		{ "toJSON", "to" },
		{ "x", "" },
		{ "ab", "ab" }, // two-letter lowercase prefixes are valid
		{ "abc123", "abc" },
	}
	for _, tt := range tests {
		got := namePrefix(tt.in)
		if got != tt.want {
			t.Errorf("namePrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct{ in string; want []string }{
		{"GetUserByID", []string{"get", "user", "by", "id"}},
		{"handle_request", []string{"handle", "request"}},
		{"to-json", []string{"to", "json"}},
		{"hello", []string{"hello"}},
	}
	for _, tt := range tests {
		got := tokenize(tt.in)
		if !stringSliceEqual(got, tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIdentifierOverlap(t *testing.T) {
	if identifierOverlap("GetUserByID", "GetUser") <= 0 {
		t.Error("expected positive overlap for similar identifiers")
	}
	if identifierOverlap("FooBar", "BazQux") != 0 {
		t.Error("expected zero overlap for unrelated identifiers")
	}
	if identifierOverlap("NewServer", "NewServer") != 1.0 {
		t.Error("expected 1.0 overlap for identical identifiers")
	}
}

func TestTypeCompat(t *testing.T) {
	if typeCompat("code:function", "code:function") != 1.0 {
		t.Error("same type should return 1.0")
	}
	if typeCompat("code:function", "code:type") != 0.5 {
		t.Error("same prefix should return 0.5")
	}
	if typeCompat("code:function", "generic-doc") != 0 {
		t.Error("different prefix should return 0")
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	if c := cosineSimilarity(a, b); c != 0 {
		t.Errorf("orthogonal vectors should have 0 cosine, got %f", c)
	}
	c := cosineSimilarity(a, a)
	if math.Abs(c-1.0) > 1e-6 {
		t.Errorf("same vector should have 1.0 cosine, got %f", c)
	}
	if cosineSimilarity(nil, a) != 0 {
		t.Error("nil input should return 0")
	}
}

func TestPathProximity(t *testing.T) {
	pp := pathProximity("internal/onboard/foo.go", "internal/onboard/bar.go")
	if pp <= 0 {
		t.Errorf("same dir should have positive proximity, got %f", pp)
	}
	pp2 := pathProximity("internal/onboard/foo.go", "internal/mcp/bar.go")
	if pp2 >= pp {
		t.Errorf("different dirs should have lower proximity")
	}
	if pathProximity("", "a.go") != 0 {
		t.Error("empty path should return 0")
	}
}

func TestSplitLastDot(t *testing.T) {
	pkg, name := splitLastDot("foo.bar.baz")
	if pkg != "foo.bar" || name != "baz" {
		t.Errorf(`splitLastDot("foo.bar.baz") = (%q, %q), want ("foo.bar", "baz")`, pkg, name)
	}
	pkg, name = splitLastDot("no-dot")
	if pkg != "no-dot" || name != "" {
		t.Errorf(`splitLastDot("no-dot") = (%q, %q), want ("no-dot", "")`, pkg, name)
	}
}

func docNode(typ, path string) store.Node {
	return store.Node{ID: path, NodeType: typ, Title: filepath.Base(path), SourcePath: path}
}

func codeNode(typ, path string) store.Node {
	return store.Node{ID: path, NodeType: typ, SourcePath: path}
}

func TestTemplateCollisionPenalty(t *testing.T) {
	tests := []struct {
		name string
		a, b store.Node
		want float64
	}{
		{"docwalk numbered template, different features",
			docNode("prd-detail", "PRJ/MDL/PRD/config-rbac/03-schema.md"),
			docNode("prd-detail", "PRJ/MDL/PRD/module-y/03-schema.md"),
			-0.20},
		{"markdown-glob README, different dirs",
			docNode("generic-doc", "docs/auth/README.md"),
			docNode("generic-doc", "docs/billing/README.md"),
			-0.20},
		{"template collision, same doc type, diff features",
			docNode("template", "feature-a/03-functional.md"),
			docNode("template", "feature-b/03-functional.md"),
			-0.20},
		{"different filenames, same feature",
			docNode("prd-detail", "PRJ/MDL/PRD/module-y/03-functional.md"),
			docNode("prd-detail", "PRJ/MDL/PRD/module-y/03-schema.md"),
			0},
		{"same directory",
			docNode("prd-detail", "PRJ/MDL/PRD/module-y/03-schema.md"),
			docNode("prd-detail", "PRJ/MDL/PRD/module-y/03-schema.md"),
			0},
		{"code to code, same base name",
			codeNode("code:function", "internal/store/sqlite/sqlite.go"),
			codeNode("code:function", "internal/store/libsql/sqlite.go"),
			0},
		{"mixed code and doc",
			codeNode("code:function", "internal/foo/bar.go"),
			docNode("generic-doc", "docs/bar.md"),
			0},
		{"empty source path",
			store.Node{NodeType: "prd", Title: "03-schema.md"},
			docNode("prd", "some/path/03-schema.md"),
			0},
		{"obsidian collision across folders",
			docNode("obsidian-note", "vault/project-a/architecture.md"),
			docNode("obsidian-note", "vault/project-b/architecture.md"),
			-0.20},
		{"erd same dir, same filename",
			docNode("erd", "feature/03-schema.md"),
			docNode("erd", "feature/03-schema.md"),
			0},
		{"design-doc same name, different dirs",
			docNode("design-doc", "auth/design.md"),
			docNode("design-doc", "billing/design.md"),
			-0.20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := templateCollisionPenalty(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("templateCollisionPenalty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
