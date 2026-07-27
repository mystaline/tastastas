package treesitter

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsBind "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

type TSExt struct{}

func (e *TSExt) Language() *sitter.Language {
	return sitter.NewLanguage(tsBind.LanguageTypescript())
}

func (e *TSExt) Queries() map[string]string {
	return map[string]string{
		"func": `(function_declaration name: (identifier) @name) @node`,
		"method": `(method_definition name: (property_identifier) @name) @node`,
		"class":  `(class_declaration name: (type_identifier) @name) @node`,
		"intf":   `(interface_declaration name: (type_identifier) @name) @node`,
	}
}

func (e *TSExt) SymbolKind(grammarName string) string {
	switch grammarName {
	case "function_declaration":
		return "code:function"
	case "method_definition":
		return "code:method"
	case "class_declaration", "interface_declaration":
		return "code:type"
	default:
		return "code:function"
	}
}

func (e *TSExt) ImportRule() *importRule {
	return &importRule{
		query: `(import_statement source: (string) @path) @imp`,
		srcFrom: func(m *sitter.QueryMatch, src []byte) string {
			for _, cap := range m.Captures {
				text := cap.Node.Utf8Text(src)
				text = strings.Trim(text, "\"'")
				if text != "" && !strings.HasPrefix(text, ".") && !strings.HasPrefix(text, "/") {
					return text
				}
			}
			return ""
		},
	}
}
