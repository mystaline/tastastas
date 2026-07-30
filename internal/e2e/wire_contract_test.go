// Package e2e — TestWireContract asserts that every MCP tool's output struct
// tag names match the exact JSON field names expected by the plan's contract.
// This is the prevention layer for bug class #2 from the original review:
// wire-shape drift that compiles fine but silently breaks clients.
//
// Strategy: reflect over each output type's struct tags at compile time and
// assert the exact set of json fields matches what the plan specifies. This
// catches field renames, additions, removals, and tag typos — all of which
// are silent to go vet and silent to any test that doesn't deserialize the
// actual wire JSON back into the struct.
package e2e

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	mcpserver "github.com/mystaline-dev/tastastas/internal/mcp"
)

// expectedWireShapes maps each MCP tool name to the exact JSON field names
// its output struct MUST contain on the wire — this is the plan's contract,
// not a test artifact. If someone adds a field, renames a tag, or removes
// one, this test breaks with a clear diff showing exactly what changed.
var expectedWireShapes = map[string][]string{
	"remember":             {"id", "status"},
	"recall":               {"links", "results", "warning"},
	"forget":               {"status"},
	"link":                 {"status"},
	"check_impact":         {"stale_nodes"},
	"onboard_check":       {"chunk_count", "edge_count", "edge_type_counts", "has_chunks", "has_conventions", "has_edges", "has_embeddings", "has_nodes", "node_count", "stale_count", "vec_count"},
	"extract_and_remember": {"facts"},
	"ingest":               {"auto_linked", "chunks_created", "conventions_inferred", "edges_created", "job_id", "nodes_ingested", "proposals_queued", "status"},
	"query_graph":          {"edges", "node_id", "title"},
	"clear_project":        {"deleted_chunks", "deleted_edges", "deleted_nodes", "deleted_vectors", "status"},
	"list_projects":        {"projects"},
}

// jsonFields extracts the sorted set of json struct tag names from a type.
func jsonFields(t reflect.Type) []string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var fields []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := tag
		// strip omitempty, etc.
		if idx := indexOf(tag, ','); idx >= 0 {
			name = tag[:idx]
		}
		fields = append(fields, name)
	}
	sort.Strings(fields)
	return fields
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// TestOutputWireContract reflects over every MCP tool's Go output type and
// asserts that the json struct tag names match the exact set expected by the
// plan contract. A field rename, addition, or removal breaks this test with
// a clear message showing what changed.
func TestOutputWireContract(t *testing.T) {
	toolTypes := map[string]reflect.Type{
		"remember":             reflect.TypeOf(mcpserver.RememberOutput{}),
		"recall":               reflect.TypeOf(mcpserver.RecallOutput{}),
		"forget":               reflect.TypeOf(mcpserver.ForgetOutput{}),
		"link":                 reflect.TypeOf(mcpserver.LinkOutput{}),
		"check_impact":         reflect.TypeOf(mcpserver.CheckImpactOutput{}),
		"onboard_check":       reflect.TypeOf(mcpserver.OnboardCheckOutput{}),
		"extract_and_remember": reflect.TypeOf(mcpserver.ExtractAndRememberOutput{}),
		"ingest":               reflect.TypeOf(mcpserver.IngestOutput{}),
		"query_graph":          reflect.TypeOf(mcpserver.QueryGraphOutput{}),
		"clear_project":        reflect.TypeOf(mcpserver.ClearProjectOutput{}),
		"list_projects":        reflect.TypeOf(mcpserver.ListProjectsOutput{}),
	}

	for tool, expected := range expectedWireShapes {
		typ, ok := toolTypes[tool]
		if !ok {
			t.Fatalf("tool %q: no output type registered in toolTypes map", tool)
		}
		actual := jsonFields(typ)
		if len(actual) != len(expected) {
			t.Errorf("tool %q: field count mismatch — expected %d fields %v, got %d %v",
				tool, len(expected), expected, len(actual), actual)
			continue
		}
		for i := range expected {
			if actual[i] != expected[i] {
				t.Errorf("tool %q: field %d — expected %q, got %q (full: %v)",
					tool, i, expected[i], actual[i], actual)
			}
		}
	}
}

// TestCheckImpactStaleNodesFieldIsCorrectType is a targeted regression test
// for the exact bug found in the first review: CheckImpactOutput had a field
// named "stale" (singular) instead of "stale_nodes" (plural, array), and
// StaleNode had an extra "status" field not in the plan. This test asserts
// the full shape including nested types.
func TestCheckImpactStaleNodesFieldIsCorrectType(t *testing.T) {
	var out mcpserver.CheckImpactOutput
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal CheckImpactOutput: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal CheckImpactOutput to map: %v", err)
	}
	if _, ok := raw["stale_nodes"]; !ok {
		t.Fatalf("CheckImpactOutput must have field 'stale_nodes' on wire — got: %s", b)
	}
	if _, ok := raw["stale"]; ok {
		t.Fatalf("CheckImpactOutput must NOT have legacy field 'stale' (replaced by 'stale_nodes') — got: %s", b)
	}
	// Assert nested StaleNode has exactly {id, node_type} — no extra fields.
	var staleNode mcpserver.StaleNode
	nb, _ := json.Marshal(staleNode)
	var nraw map[string]json.RawMessage
	json.Unmarshal(nb, &nraw)
	if len(nraw) != 2 {
		t.Fatalf("StaleNode must have exactly 2 fields (id, node_type) — got %d: %s", len(nraw), nb)
	}
	if _, ok := nraw["id"]; !ok {
		t.Fatalf("StaleNode must have 'id' field — got: %s", nb)
	}
	if _, ok := nraw["node_type"]; !ok {
		t.Fatalf("StaleNode must have 'node_type' field — got: %s", nb)
	}
}

// TestCheckImpactInputFieldIsID is a targeted regression test for the
// original check_impact bug: input struct had ChangedID (json:"changed_id")
// instead of ID (json:"id") per the plan contract.
func TestCheckImpactInputFieldIsID(t *testing.T) {
	var in mcpserver.CheckImpactInput
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal CheckImpactInput: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal CheckImpactInput to map: %v", err)
	}
	if _, ok := raw["id"]; !ok {
		t.Fatalf("CheckImpactInput must have field 'id' — got: %s", b)
	}
	if _, ok := raw["changed_id"]; ok {
		t.Fatalf("CheckImpactInput must NOT have legacy field 'changed_id' (replaced by 'id') — got: %s", b)
	}
}
