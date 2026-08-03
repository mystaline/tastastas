// Package scope implements branch-aware project scoping: encoding a stage
// (branch/ref) into a project ID, decoding it back, and the edge-visibility
// invariant shared by both store backends.
package scope

import (
	"fmt"
	"net/url"
	"strings"
)

// Marker separates the base project ID from its encoded stage.
const Marker = "::stage:"

// markerLen must equal len(Marker); asserted in scope_test.go.
const markerLen = 8

// Encode combines a base project ID and stage into an effective project ID.
func Encode(baseProjectID, stage string) (string, error) {
	if baseProjectID == "" {
		baseProjectID = "default"
	}
	if stage == "" {
		return "", fmt.Errorf("scope: stage must not be empty")
	}
	for _, r := range stage {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("scope: stage contains control character")
		}
	}
	if strings.Contains(baseProjectID, Marker) {
		return "", fmt.Errorf("scope: base project ID %q already contains marker", baseProjectID)
	}
	return baseProjectID + Marker + url.PathEscape(stage), nil
}

// Decode splits an effective project ID into base and stage. Unscoped IDs
// return staged=false with the ID unchanged as base.
func Decode(effectiveID string) (base, stage string, staged bool, err error) {
	i := strings.Index(effectiveID, Marker)
	if i < 0 {
		return effectiveID, "", false, nil
	}
	raw := effectiveID[i+markerLen:]
	stage, err = url.PathUnescape(raw)
	if err != nil {
		return "", "", false, fmt.Errorf("scope: decode stage: %w", err)
	}
	if stage == "" {
		return "", "", false, fmt.Errorf("scope: decoded stage is empty")
	}
	return effectiveID[:i], stage, true, nil
}

// Resolve returns the project ID to use for a tool call. If stage is set,
// it encodes. Otherwise it either errors (requireStage) or returns
// baseProjectID unchanged (legacy path).
func Resolve(baseProjectID, stage string, requireStage bool) (string, error) {
	if stage != "" {
		return Encode(baseProjectID, stage)
	}
	if requireStage {
		return "", fmt.Errorf("stage is required for this operation")
	}
	return baseProjectID, nil
}

// RawStage returns the encoded (not unescaped) stage substring after the
// marker, or "" if unscoped. Used by EdgeAllowed so Go and SQL compare
// identical bytes.
func RawStage(effectiveID string) string {
	i := strings.Index(effectiveID, Marker)
	if i < 0 {
		return ""
	}
	return effectiveID[i+markerLen:]
}

// EdgeAllowed implements the invariant: an edge is rejected iff both
// endpoints have a non-empty stage and those stages differ.
func EdgeAllowed(fromProjectID, toProjectID string) bool {
	fs, ts := RawStage(fromProjectID), RawStage(toProjectID)
	return fs == "" || ts == "" || fs == ts
}

// NormalizeRepositoryURL reduces a git remote URL to a stable identity:
// host lowercased, path case preserved, .git suffix and trailing slash
// stripped, credentials/query/fragment dropped. The result is globally
// unique and survives directory renames, so it is usable as a project ID.
func NormalizeRepositoryURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "git@") {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) == 2 {
			raw = "ssh://" + parts[0] + "/" + parts[1]
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(strings.TrimSuffix(raw, ".git"), "/")
	}
	u.Host = strings.ToLower(u.Host)
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, ".git"), "/")
	return u.Host + u.Path
}

// ProjectIDFromRemote derives a stable project identity from a git remote
// URL. The normalized form is host/org/repo (host lowercased, path case
// preserved), which is globally unique and survives directory renames.
// name is the repo basename, for human-readable display and fuzzy search.
// ok is false when raw is empty or yields no path segment.
func ProjectIDFromRemote(raw string) (id, name string, ok bool) {
	id = NormalizeRepositoryURL(raw)
	if id == "" || !strings.Contains(id, "/") {
		return "", "", false
	}
	name = id[strings.LastIndex(id, "/")+1:]
	if name == "" {
		return "", "", false
	}
	return id, name, true
}

// EdgeFilterSQL returns a SQL boolean predicate implementing the same
// invariant as EdgeAllowed, for use in WHERE clauses over LEFT-JOINed
// endpoint tables aliased fromAlias/toAlias. COALESCE is mandatory: a
// LEFT JOIN NULL endpoint must evaluate to allowed, not NULL.
func EdgeFilterSQL(fromAlias, toAlias string) string {
	return `NOT (
		instr(COALESCE(` + fromAlias + `.project_id, ''), '` + Marker + `') > 0
		AND instr(COALESCE(` + toAlias + `.project_id, ''), '` + Marker + `') > 0
		AND substr(COALESCE(` + fromAlias + `.project_id, ''), instr(COALESCE(` + fromAlias + `.project_id, ''), '` + Marker + `') + ` + fmt.Sprint(markerLen) + `)
		 <> substr(COALESCE(` + toAlias + `.project_id, ''), instr(COALESCE(` + toAlias + `.project_id, ''), '` + Marker + `') + ` + fmt.Sprint(markerLen) + `)
	)`
}
