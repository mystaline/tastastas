package treesitter

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
)

// GoExt is the tree-sitter extractor for Go. Used as a fallback when
// go/packages.Load is unavailable (no go toolchain in the runtime image,
// private modules unreachable) — same degraded path as TS/Python/Rust:
// name-matched calls at Confidence 0.7 instead of Go's type-checked 1.0.
type GoExt struct{}

func (e *GoExt) Language() *sitter.Language {
	return sitter.NewLanguage(golang.Language())
}

func (e *GoExt) Queries() map[string]string {
	return map[string]string{
		"func":       `(function_declaration name: (identifier) @name) @node`,
		"method":     `(method_declaration receiver: (parameter_list (parameter_declaration type: (type_identifier) @recv)) name: (field_identifier) @name) @node`,
		"method_ptr": `(method_declaration receiver: (parameter_list (parameter_declaration type: (pointer_type (type_identifier) @recv))) name: (field_identifier) @name) @node`,
		"type":       `(type_declaration (type_spec name: (type_identifier) @name) @node)`,
		"typealias":  `(type_alias name: (type_identifier) @name) @node`,
	}
}

func (e *GoExt) SymbolKind(grammarName string) string {
	switch grammarName {
	case "method_declaration":
		return "code:method"
	case "type_spec", "type_alias":
		return "code:type"
	default:
		return "code:function"
	}
}

// ImportRule is nil: Go imports are package paths resolved through the module
// graph, which the degraded tree-sitter path cannot map to file nodes without
// packages.Load. Skipping avoids false import edges.
func (e *GoExt) ImportRule() *importRule { return nil }

func (e *GoExt) TypeRefQueries() map[string]string {
	return map[string]string{
		"param_type":    `(parameter_declaration type: (type_identifier) @type) @enclosing`,
		"return_type":   `(function_declaration result: (type_identifier) @type) @enclosing`,
		"method_return": `(method_declaration result: (type_identifier) @type) @enclosing`,
		"var_type":      `(var_declaration (var_spec type: (type_identifier) @type)) @enclosing`,
		"field_type":    `(field_declaration type: (type_identifier) @type) @enclosing`,
	}
}

func (e *GoExt) CallQueries() map[string]string {
	return map[string]string{
		"call":        `(call_expression function: (identifier) @callee) @node`,
		"member_call": `(call_expression function: (selector_expression field: (field_identifier) @callee) @member) @node`,
	}
}

// isPrimitiveGo returns true for Go built-in types that should not produce
// reference edges.
func isPrimitiveGo(name string) bool {
	switch name {
	case "string", "bool", "byte", "rune", "error", "any", "comparable",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128",
		"map", "slice", "chan", "func", "interface", "struct":
		return true
	}
	return strings.HasPrefix(name, "[]") // []byte, []string, ...
}
