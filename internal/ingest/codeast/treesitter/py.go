package treesitter

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	pyBind "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

type PyExt struct{}

func (e *PyExt) Language() *sitter.Language {
	return sitter.NewLanguage(pyBind.Language())
}

func (e *PyExt) Queries() map[string]string {
	return map[string]string{
		"func":  `(function_definition name: (identifier) @name) @node`,
		"class": `(class_definition name: (identifier) @name) @node`,
	}
}

func (e *PyExt) SymbolKind(grammarName string) string {
	switch grammarName {
	case "function_definition":
		return "code:function"
	case "class_definition":
		return "code:type"
	default:
		return "code:function"
	}
}

func (e *PyExt) ImportRule() *importRule {
	return &importRule{
		query: `[(import_statement name: (dotted_name) @name) (import_from_statement module_name: (dotted_name) @name)] @imp`,
		srcFrom: func(m *sitter.QueryMatch, src []byte) string {
			for _, cap := range m.Captures {
				text := cap.Node.Utf8Text(src)
				text = strings.TrimSpace(text)
				if text != "" {
					return text
				}
			}
			return ""
		},
	}
}

func (e *PyExt) TypeRefQueries() map[string]string {
	return map[string]string{
		"param_type":  `(function_definition parameters: (parameters (typed_parameter type: (identifier) @type))) @enclosing`,
		"return_type": `(function_definition return_type: (identifier) @type) @enclosing`,
		"var_type":    `(assignment type: (identifier) @type) @enclosing`,
	}
}

