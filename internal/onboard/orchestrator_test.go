package onboard

import (
	"math"
	"testing"
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
