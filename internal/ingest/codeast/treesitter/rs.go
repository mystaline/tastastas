package treesitter

import (
	sitter "github.com/tree-sitter/go-tree-sitter"
	rsBind "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

type RsExt struct{}

func (e *RsExt) Language() *sitter.Language {
	return sitter.NewLanguage(rsBind.Language())
}

func (e *RsExt) Queries() map[string]string {
	return map[string]string{
		"func":   `(function_item name: (identifier) @name) @node`,
		"struct": `(struct_item name: (type_identifier) @name) @node`,
		"impl":   `(impl_item type: (type_identifier) @name) @node`,
	}
}

func (e *RsExt) SymbolKind(grammarName string) string {
	switch grammarName {
	case "function_item":
		return "code:function"
	case "struct_item":
		return "code:type"
	case "impl_item":
		return "code:type"
	default:
		return "code:function"
	}
}

func (e *RsExt) ImportRule() *importRule {
	return &importRule{
		query: `(use_declaration argument: (use_as_clause name: (identifier) @name)) @imp`,
		srcFrom: func(m *sitter.QueryMatch, src []byte) string {
			for _, cap := range m.Captures {
				text := cap.Node.Utf8Text(src)
				if text != "" {
					return text
				}
			}
			return ""
		},
	}
}

func (e *RsExt) TypeRefQueries() map[string]string {
	return map[string]string{
		"param_type":  `(function_item parameters: (parameters (parameter type: (identifier) @type))) @enclosing`,
		"return_type": `(function_item return_type: (identifier) @type) @enclosing`,
		"let_type":    `(let_declaration type: (identifier) @type) @enclosing`,
		"field_type":  `(struct_item field_declaration type: (identifier) @type) @enclosing`,
	}
}
