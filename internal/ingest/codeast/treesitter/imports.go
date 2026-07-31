package treesitter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveImport resolves a TS/JS import specifier to a file node ID.
// Returns empty string if unresolvable (external, non-code, not found).
func resolveImport(specifier, relDir, root, projectID string) string {
	if strings.HasPrefix(specifier, ".") {
		return resolveRelativeFile(specifier, relDir, root, projectID)
	}
	if strings.HasPrefix(specifier, "/") {
		return resolveRelativeFile(specifier, "", root, projectID)
	}
	if idx := strings.Index(specifier, "/"); idx > 0 {
		prefix := specifier[:idx]
		rest := specifier[idx+1:]
		if target := resolveAlias(prefix, rest, root, projectID); target != "" {
			return target
		}
	}
	return ""
}

func resolveRelativeFile(specifier, relDir, root, projectID string) string {
	candidate := filepath.Join(relDir, specifier)
	candidate = filepath.Clean(candidate)
	abs := filepath.Join(root, candidate)

	exts := []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}
	for _, ext := range exts {
		if fileExists(abs + ext) {
			return fmt.Sprintf("%s/code:file/%s", projectID, filepath.ToSlash(candidate+ext))
		}
	}
	for _, ext := range exts {
		index := filepath.Join(abs, "index"+ext)
		if fileExists(index) {
			rel := filepath.ToSlash(filepath.Join(candidate, "index"+ext))
			return fmt.Sprintf("%s/code:file/%s", projectID, rel)
		}
	}
	return ""
}

func resolveAlias(prefix, rest, root, projectID string) string {
	aliases := readTsconfigAliases(root)
	if aliases == nil {
		return ""
	}
	base, ok := aliases[prefix]
	if !ok {
		base, ok = aliases["@"+prefix]
		if !ok {
			return ""
		}
	}
	relDir := filepath.ToSlash(base)
	return resolveRelativeFile(rest, relDir, root, projectID)
}

type tsconfigJSON struct {
	CompilerOptions struct {
		Paths   map[string][]string `json:"paths"`
		BaseURL string              `json:"baseUrl"`
	} `json:"compilerOptions"`
}

func readTsconfigAliases(root string) map[string]string {
	tsconfig, err := findFileUpwards("tsconfig.json", root)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(tsconfig)
	if err != nil {
		return nil
	}
	cleaned := stripJSONComments(string(data))
	var cfg tsconfigJSON
	if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
		return nil
	}
	result := make(map[string]string)
	for alias, targets := range cfg.CompilerOptions.Paths {
		cleanAlias := strings.TrimSuffix(alias, "/*")
		for _, target := range targets {
			cleanTarget := strings.TrimSuffix(target, "/*")
			result[cleanAlias] = cleanTarget
			break
		}
	}
	return result
}

func findFileUpwards(name, dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, name)
		if fileExists(candidate) {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found from %s", name, dir)
		}
		dir = parent
	}
}

func stripJSONComments(s string) string {
	var out strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		if s[i] == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr && i+1 < len(s) {
			if s[i] == '/' && s[i+1] == '/' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			if s[i] == '/' && s[i+1] == '*' {
				i += 2
				for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
