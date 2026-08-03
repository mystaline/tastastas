package mcp

import (
	"strings"

	"github.com/mystaline/tastastas/internal/scope"
	"github.com/mystaline/tastastas/internal/store"
)

// resolveToolScope returns the single effective project ID for a
// project-scoped tool.
func resolveToolScope(projectID, stage string) (string, error) {
	if projectID == "" {
		projectID = "default"
	}
	return scope.Resolve(projectID, stage, false)
}

// formatSuggestions renders fuzzy-matched projects as "base / stage" for
// staged rows and plain "base" for legacy rows.
func formatSuggestions(matches []store.ProjectInfo) string {
	labels := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.Stage != "" {
			labels = append(labels, m.ProjectID+" with stage: "+m.Stage)
		} else {
			labels = append(labels, m.ProjectID)
		}
	}
	return strings.Join(labels, ", ")
}
