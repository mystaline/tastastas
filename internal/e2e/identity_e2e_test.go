// Package e2e — TestE2EIdentityUserStory drives the remote-derived project
// identity user story through the real MCP wire end to end:
//
//  1. remote-only ingest derives identity (host/org/repo) from repository_url
//  2. project_name + repository_url surfaced by init
//  3. fuzzy short-name recall still finds the project
//  4. onboard_check gates a project already clean for the model
//  5. a repo absent from the workspace errors and names clone_repo
//  6. an ambiguous cwd errors and names repository_url
//  7. a non-git dir keeps basename identity + stage local
//
// Without these, the whole identity feature could be deleted and the suite
// would stay green — the pre-existing e2e IdentityAdjacentTest only routes
// through the legacy project_id override path.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectIdentity spawns the binary with -embed-backend sidecar so modelID is
// non-empty (sidecar mode sets it even when the baked binary is missing and
// the embedder falls back to nil). That matters for the gated story: the
// clean-model bookkeeping only runs when modelID != "".
//
// ponytail: with a real sidecar present this genuinely embeds the fixture;
// add a fixture-remote clone happy-path assertion here once a reachable
// remote exists (real-network clone stays manual-only).
func connectIdentity(t *testing.T, bin, dbPath string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "-db", dbPath, "-embed-backend", "sidecar")
	transport := &mcp.CommandTransport{Command: cmd}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-identity", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// callRaw calls a tool and returns (isError, raw text content) without
// failing the test — used for the error-path assertions where callTool (which
// Fatals on IsError) is the wrong tool.
func callRaw(t *testing.T, sess *mcp.ClientSession, name string, args any) (bool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return res.IsError, sb.String()
}

func waitForDone(t *testing.T, sess *mcp.ClientSession, jobID string) *jobStatus {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		var st jobStatus
		callTool(t, sess, "job_status", map[string]any{"id": jobID}, &st)
		if st.Status == "done" || st.Status == "error" || time.Now().After(deadline) {
			if st.Status != "done" {
				t.Fatalf("job %s did not finish: %+v", jobID, st)
			}
			return &st
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func initGitFixture(t *testing.T, dir, remoteURL string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"remote", "add", "origin", remoteURL},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# tastastas\n\nlib repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"commit", "-q", "-m", "init"},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

type ingestResp struct {
	JobID              string `json:"job_id"`
	Stage              string `json:"stage"`
	EffectiveProjectID string `json:"effective_project_id"`
}

type jobStatus struct {
	Status string `json:"status"`
	Nodes  int    `json:"nodes_ingested"`
	Error  string `json:"error"`
}

type projectInfo struct {
	ProjectID     string `json:"project_id"`
	ProjectName   string `json:"project_name"`
	RepositoryURL string `json:"repository_url"`
}

func TestE2EIdentityUserStory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e identity test in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	workspaceRoot := t.TempDir()
	t.Setenv("SERVER_WORKSPACE_ROOT", workspaceRoot)

	const baseID = "gitea.example/Org/tastastas"
	repoDir := filepath.Join(workspaceRoot, "org", "tastastas")
	initGitFixture(t, repoDir, "https://gitea.example/Org/tastastas.git")

	bin := buildBinary(t)
	dbPath := filepath.Join(t.TempDir(), "identity.db")
	sess := connectIdentity(t, bin, dbPath)

	// 1. Remote-only ingest: no cwd, no project_id. Identity from remote.
	var ing ingestResp
	callTool(t, sess, "ingest", map[string]any{
		"repository_url": "https://gitea.example/Org/tastastas.git",
	}, &ing)
	if ing.JobID == "" {
		t.Fatalf("ingest: empty job_id: %+v", ing)
	}
	if ing.Stage == "" {
		t.Fatalf("ingest: empty stage: %+v", ing)
	}
	wantEff := baseID + "::stage:" + ing.Stage
	if ing.EffectiveProjectID != wantEff {
		t.Fatalf("effective_project_id = %q, want %q", ing.EffectiveProjectID, wantEff)
	}
	st := waitForDone(t, sess, ing.JobID)
	if st.Nodes == 0 {
		t.Fatalf("expected nodes ingested, got %+v", st)
	}

	// 2. init carries project_name + repository_url.
	var initOut struct {
		Projects []projectInfo `json:"projects"`
	}
	callTool(t, sess, "init", map[string]any{}, &initOut)
	var found *projectInfo
	for i := range initOut.Projects {
		if initOut.Projects[i].ProjectID == baseID {
			found = &initOut.Projects[i]
		}
	}
	if found == nil {
		t.Fatalf("project %q missing from init: %+v", baseID, initOut.Projects)
	}
	if found.ProjectName != "tastastas" {
		t.Fatalf("project_name = %q, want %q", found.ProjectName, "tastastas")
	}
	if found.RepositoryURL != baseID {
		t.Fatalf("repository_url = %q, want %q", found.RepositoryURL, baseID)
	}

	// 3. Fuzzy short-name recall resolves. Unknown short name either returns
	// results directly or fires the "Did you mean" suggestion.
	var recallOut struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
		Warning string `json:"warning"`
	}
	callTool(t, sess, "recall", map[string]any{
		"project_id": "tastastas",
		"stage":      ing.Stage,
		"query":      "README",
		"limit":      5,
	}, &recallOut)
	if len(recallOut.Results) == 0 && !strings.Contains(recallOut.Warning, "Did you mean") {
		t.Fatalf("short-name recall resolved to neither results nor a suggestion: %+v", recallOut)
	}

	// 4. onboard_check gates an already-clean project.
	var checkOut struct {
		Gated      bool   `json:"gated"`
		GateReason string `json:"gate_reason"`
		HasNodes   bool   `json:"has_nodes"`
	}
	callTool(t, sess, "onboard_check", map[string]any{
		"project_id": baseID,
		"stage":      ing.Stage,
	}, &checkOut)
	if !checkOut.HasNodes {
		t.Fatalf("onboard_check: expected nodes present: %+v", checkOut)
	}
	if !checkOut.Gated {
		t.Fatalf(
			"onboard_check: expected gated=true for clean project (model bookkeeping ran via sidecar), got %+v",
			checkOut,
		)
	}
	if checkOut.GateReason == "" {
		t.Fatalf("onboard_check: expected non-empty gate_reason, got %+v", checkOut)
	}

	// 5. Absent repo -> error names clone_repo.
	isErr, text := callRaw(t, sess, "ingest", map[string]any{
		"repository_url": "https://gitea.example/Org/nope.git",
	})
	if !isErr {
		t.Fatalf("ingest absent repo: expected error, got %q", text)
	}
	if !strings.Contains(text, "clone_repo") {
		t.Fatalf("absent-repo error should name clone_repo: %q", text)
	}

	// 6. Ambiguous cwd -> error names repository_url.
	initGitFixture(t, filepath.Join(workspaceRoot, "other", "tastastas"), "https://gitea.example/Other/tastastas.git")
	ambIsErr, ambText := callRaw(t, sess, "ingest", map[string]any{
		"cwd": "/home/some-client/Workspace/Personal/tastastas",
	})
	if !ambIsErr {
		t.Fatalf("ambiguous cwd: expected error, got %q", ambText)
	}
	if !strings.Contains(ambText, "repository_url") {
		t.Fatalf("ambiguous-cwd error should suggest repository_url: %q", ambText)
	}

	// 7. Non-git dir -> basename identity + stage local.
	notesDir := filepath.Join(workspaceRoot, "notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll notes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "notes.md"), []byte("# Notes\n"), 0o644); err != nil {
		t.Fatalf("write notes.md: %v", err)
	}
	var notes ingestResp
	callTool(t, sess, "ingest", map[string]any{"cwd": notesDir}, &notes)
	if notes.Stage != "local" {
		t.Fatalf("non-git ingest: stage = %q, want %q", notes.Stage, "local")
	}
	if !strings.HasPrefix(notes.EffectiveProjectID, "notes::stage:") {
		t.Fatalf("non-git ingest: effective_project_id = %q, want prefix notes::stage:", notes.EffectiveProjectID)
	}
}
