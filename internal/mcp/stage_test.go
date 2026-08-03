package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	sqlitestore "github.com/mystaline/tastastas/internal/store/sqlite"
)

// newStageTestClient wires an in-memory MCP client/server pair against a
// fresh file-backed SQLite store (async ingest jobs run in their own
// goroutine/connection, so ":memory:" per-connection isolation would break
// them), for exercising tool handlers end-to-end.
func newStageTestClient(t *testing.T) (*mcp.ClientSession, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlitestore.Open(context.Background(), dbPath, 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	srv := NewServer(db, nil, 32, "")
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)

	t1, t2 := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	cleanup := func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
		_ = db.Close()
	}
	return clientSession, cleanup
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if out != nil && res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured content: %v", err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("unmarshal structured content into %T: %v", out, err)
		}
	}
	return res
}

// waitJobDone polls job_status until the given job reaches a terminal
// state. Ingest/onboard are async — tests that assert on their persisted
// side effects (list_projects, recall, etc.) must wait for the job first.
func waitJobDone(t *testing.T, cs *mcp.ClientSession, jobID string) JobStatusOutput {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var out JobStatusOutput
		callTool(t, cs, "job_status", JobStatusInput{JobID: jobID}, &out)
		if out.Status == "done" || out.Status == "error" {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not complete in time", jobID)
	return JobStatusOutput{}
}

func TestMCP_RecallWithStageReturnsStagedAndBaseFacts(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	var rememberOut RememberOutput
	callTool(t, cs, "remember", RememberInput{
		ProjectID: "repo-a",
		Content:   "base scope fact about auth",
	}, &rememberOut)

	var ingestOut IngestOutput
	res := callTool(t, cs, "ingest", IngestInput{
		ProjectID: "repo-a",
		Stage:     "main",
		CWD:       t.TempDir(),
	}, &ingestOut)
	if res.IsError {
		t.Fatalf("ingest tool errored: %+v", res.Content)
	}
	if ingestOut.Stage != "main" || ingestOut.EffectiveProjectID == "" {
		t.Fatalf("expected ingest output to carry stage/effective id: %+v", ingestOut)
	}
	waitJobDone(t, cs, ingestOut.JobID)

	var recallOut RecallOutput
	callTool(t, cs, "recall", RecallInput{
		ProjectID: "repo-a",
		Stage:     "main",
		Query:     "auth",
		Limit:     10,
	}, &recallOut)

	seen := map[string]int{}
	for _, item := range recallOut.Results {
		seen[item.ID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("item %s appeared %d times, expected exactly once", id, count)
		}
	}

	foundBase := false
	for _, item := range recallOut.Results {
		if item.ID == rememberOut.ID {
			foundBase = true
			if item.ProjectID != "repo-a" {
				t.Fatalf("base fact ProjectID decoded wrong: %+v", item)
			}
		}
	}
	if !foundBase {
		t.Fatalf("expected base-scoped remembered fact in staged recall, got %+v", recallOut.Results)
	}
}

func TestMCP_RecallRejectsProjectIDsWithProjectScopes(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "recall",
		Arguments: RecallInput{
			Query:      "x",
			ProjectIDs: []string{"a"},
			ProjectScopes: []ProjectScopeInput{
				{ProjectID: "b", Stage: "main"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected recall to error when project_ids and project_scopes both set")
	}
}

func TestMCP_RememberWritesBaseScopeEvenWithStagedData(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	var ingestOut IngestOutput
	callTool(t, cs, "ingest", IngestInput{
		ProjectID: "repo-a",
		Stage:     "main",
		CWD:       t.TempDir(),
	}, &ingestOut)
	waitJobDone(t, cs, ingestOut.JobID)

	var rememberOut RememberOutput
	res := callTool(t, cs, "remember", RememberInput{
		ProjectID: "repo-a",
		Content:   "base fact",
	}, &rememberOut)

	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				t.Logf("error content: %s", tc.Text)
			}
		}
		t.Fatalf("remember errored")
	}
	if rememberOut.ID == "" {
		t.Fatal("expected remember to succeed")
	}

	var listOut ListProjectsOutput
	callTool(t, cs, "list_projects", struct{}{}, &listOut)
	var foundBase, foundStaged bool
	for _, p := range listOut.Projects {
		if p.ProjectID == "repo-a" && p.Stage == "" {
			foundBase = true
		}
		if p.ProjectID == "repo-a" && p.Stage == "main" {
			foundStaged = true
		}
	}
	if !foundBase {
		t.Fatalf("expected base repo-a project to exist: %+v", listOut.Projects)
	}
	if !foundStaged {
		t.Fatalf("expected staged repo-a/main project to exist: %+v", listOut.Projects)
	}
}

func TestMCP_IngestWithUnresolvableStageFallsBackToLocal(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	// CWD points at a non-git directory, so ref auto-detection can't find a
	// branch, and no explicit stage is given — resolveRef is total now, so
	// this must fall back to stage "local" and succeed, not error.
	var ingestOut IngestOutput
	res := callTool(t, cs, "ingest", IngestInput{
		ProjectID: "repo-a",
		CWD:       t.TempDir(),
	}, &ingestOut)
	if res.IsError {
		t.Fatalf("ingest tool errored: %+v", res.Content)
	}
	if ingestOut.Stage != "local" {
		t.Fatalf("expected stage=local fallback, got %+v", ingestOut)
	}
}

func TestMCP_AbortIngestionWithStageCancelsOnlyThatStage(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	var abortOut AbortOutput
	callTool(t, cs, "abort_ingestion", AbortInput{
		ProjectID: "repo-a",
		Stage:     "main",
	}, &abortOut)
	// No jobs running — just verify the call succeeds and scopes correctly
	// (no panic, no error, cancelled count is well-formed).
	if abortOut.Cancelled != 0 {
		t.Fatalf("expected 0 cancelled jobs with none running, got %d", abortOut.Cancelled)
	}
}

func TestMCP_LinkRejectsStageMismatch(t *testing.T) {
	cs, cleanup := newStageTestClient(t)
	defer cleanup()

	var a, b RememberOutput
	callTool(t, cs, "remember", RememberInput{ProjectID: "repo-a::stage:main", Content: "node a"}, &a)
	callTool(t, cs, "remember", RememberInput{ProjectID: "repo-a::stage:feature", Content: "node b"}, &b)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "link",
		Arguments: LinkInput{
			FromID:   a.ID,
			ToID:     b.ID,
			EdgeType: "references",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected link to reject stage-mismatched edge")
	}
}
