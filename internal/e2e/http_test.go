// TestE2EHTTPToolSequence starts the real HTTP server (ServeHTTP, the same
// function cmd/tastastas invokes with --serve) on a real listening socket
// and talks to it with a real net/http client + the SDK's real
// StreamableClientTransport — no in-process handler calls. Covers the
// three HTTP surfaces: /health, /ingest (async, returns job_id), /ingest/jobs/{id}
// endpoints, and /mcp (MCP-over-HTTP) with the same tool sequence as the
// stdio E2E test.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/mystaline-dev/tastastas/internal/embed"
	mcpserver "github.com/mystaline-dev/tastastas/internal/mcp"
	sqlitestore "github.com/mystaline-dev/tastastas/internal/store/sqlite"
)

// startHTTPServer boots the real ServeHTTP on a free localhost port (picked
// randomly and retried on bind failure — ServeHTTP owns ListenAndServe
// internally so we can't grab an OS-assigned port directly without touching
// production code) and waits for /health to respond before returning.
func startHTTPServer(t *testing.T) (addr string) {
	t.Helper()
	return startHTTPServerWithEmbedder(t, nil, 8)
}

// startHTTPServerWithEmbedder is startHTTPServer with an injected embedder
// and explicit vector dimension — used by tests that need real chunk
// embedding (e.g. the sidecar-backed /ingest/{adapter} test).
func startHTTPServerWithEmbedder(t *testing.T, embedder embed.EmbedderBackend, dim int) (addr string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "e2e-http.db")
	db, err := sqlitestore.Open(context.Background(), dbPath, dim)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		port := 20000 + rand.Intn(20000)
		addr = fmt.Sprintf("127.0.0.1:%d", port)
		errCh := make(chan error, 1)
		go func() { errCh <- mcpserver.ServeHTTP(ctx, db, embedder, addr, "", 32, "", "") }()

		// Poll /health with backoff; a bind failure surfaces almost
		// immediately on errCh, a success means /health starts responding
		// within a few hundred ms.
		ok := false
		for i := 0; i < 30; i++ {
			select {
			case err := <-errCh:
				lastErr = err
			default:
			}
			if lastErr != nil {
				break
			}
			resp, err := http.Get("http://" + addr + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					ok = true
				}
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if ok {
			return addr
		}
		lastErr = nil // port collision or slow start, retry with a new port
	}
	t.Fatalf("could not start HTTP server after 10 attempts: %v", lastErr)
	return ""
}

// TestE2EHTTPIngestChunksAndEmbeds proves POST /ingest/{adapter} chunks and
// embeds ingested nodes exactly like the MCP "ingest" tool does (this was a
// real gap: the REST path used to only upsert nodes, never call the
// embedder). Uses the real ONNX sidecar so this is genuine embedding, not a
// mock — skips gracefully if no baked sidecar binary exists for this
// platform.
func TestE2EHTTPIngestChunksAndEmbeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E HTTP test in -short mode")
	}
	sc, err := embed.NewSidecar()
	if err != nil {
		t.Skipf("sidecar unavailable on this platform: %v", err)
	}
	defer sc.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte(
		"# Auth Notes\n\nJWT validation happens in the middleware layer.\n\n"+
			"## Token Parsing\n\nBearer tokens are parsed from the Authorization header.\n",
	), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	addr := startHTTPServerWithEmbedder(t, sc, 384)
	base := "http://" + addr

	body, _ := json.Marshal(map[string]any{
		"root":       root,
		"project_id": "e2e-ingest-chunks",
	})
	resp, err := http.Post(base+"/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /ingest: status %d: %s", resp.StatusCode, b)
	}
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("decode /ingest accepted response: %v", err)
	}

	// Ingest runs async now (internal/mcp/jobs.go) — poll job status instead
	// of expecting the counts inline. Real-world ingests over big directories
	// can take minutes; the REST contract reflects that.
	var out struct {
		Status        string `json:"status"`
		NodesIngested int    `json:"nodes_ingested"`
		EdgesCreated  int    `json:"edges_created"`
		ChunksCreated int    `json:"chunks_created"`
		Error         string `json:"error"`
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		jresp, err := http.Get(base + "/ingest/jobs/" + accepted.JobID)
		if err != nil {
			t.Fatalf("GET /ingest/jobs/%s: %v", accepted.JobID, err)
		}
		if err := json.NewDecoder(jresp.Body).Decode(&out); err != nil {
			jresp.Body.Close()
			t.Fatalf("decode job status: %v", err)
		}
		jresp.Body.Close()
		if out.Status == "done" || out.Status == "error" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if out.Status != "done" {
		t.Fatalf("job did not complete in time: %+v", out)
	}
	if out.NodesIngested == 0 {
		t.Fatalf("expected >=1 node ingested, got %+v", out)
	}
	if out.ChunksCreated == 0 {
		t.Fatalf("expected chunks_created > 0 — REST ingest path did not chunk/embed, got %+v", out)
	}
}

func TestE2EHTTPHealthAndIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E HTTP test in -short mode")
	}
	addr := startHTTPServer(t)
	base := "http://" + addr

	// GET /health — real wire shape check.
	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("/health: expected status=ok, got %+v", health)
	}

	// POST /ingest — REST ingest shape check. Ingest is async now
	// (internal/mcp/jobs.go): it returns 202 + job_id immediately, and the
	// caller polls GET /ingest/jobs/{id}. Use testdata directory root so
	// ingest walks our existing test fixtures.
	ingestBody, _ := json.Marshal(map[string]string{
		"root":       "testdata",
		"project_id": "e2e-http",
	})
	resp2, err := http.Post(base+"/ingest", "application/json", bytes.NewReader(ingestBody))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("POST /ingest: status %d: %s", resp2.StatusCode, b)
	}
	var ingestOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&ingestOut); err != nil {
		t.Fatalf("decode /ingest accepted: %v", err)
	}
	if ingestOut.JobID == "" {
		t.Fatalf("/ingest: expected job_id, got %+v", ingestOut)
	}
}

// TestE2EHTTPMCPToolSequence drives the same remember->recall->link->check_impact->forget sequence as the stdio E2E test, but over real
// MCP-over-HTTP (Streamable HTTP transport) against a real listening
// socket — the third and last transport surface this repo exposes.
func TestE2EHTTPMCPToolSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E HTTP test in -short mode")
	}
	addr := startHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transport := &mcp.StreamableClientTransport{Endpoint: "http://" + addr + "/mcp"}
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-http-test", Version: "0.0.0"}, nil)
	sess, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect over StreamableClientTransport: %v", err)
	}
	defer sess.Close()

	callHTTPTool := func(name string, args any, out any) {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool(%s): transport error: %v", name, err)
		}
		if res.IsError {
			b, _ := json.Marshal(res.Content)
			t.Fatalf("CallTool(%s): tool error: %s", name, b)
		}
		if out == nil {
			return
		}
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("CallTool(%s): unmarshal StructuredContent into %T: %v (raw: %s)", name, out, err, b)
		}
	}

	var rememberOut struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	callHTTPTool("remember", map[string]any{
		"id":         "e2e-http/fact/prd-001",
		"project_id": "e2e-http",
		"node_type":  "fact",
		"title":      "HTTP E2E PRD",
		"content":    "HTTP E2E test PRD content.",
	}, &rememberOut)
	if rememberOut.ID != "e2e-http/fact/prd-001" {
		t.Fatalf("remember: unexpected id, got %+v", rememberOut)
	}

	callHTTPTool("remember", map[string]any{
		"id":         "e2e-http/fact/api-001",
		"project_id": "e2e-http",
		"node_type":  "fact",
		"title":      "HTTP E2E API spec",
		"content":    "HTTP E2E test API spec content.",
	}, nil)

	var recallOut struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	callHTTPTool("recall", map[string]any{
		"project_id": "e2e-http",
		"query":      "HTTP E2E",
	}, &recallOut)
	if len(recallOut.Results) == 0 {
		t.Fatalf("recall: expected >=1 result, got 0")
	}

	// link: prd -> api via an impact-bearing edge type (see stdio_test.go
	// for why direction/type matter: MarkStaleDownstream walks outgoing
	// edges of impactTypes = implements/tests/specifies/depends-on).
	var linkOut struct {
		Status string `json:"status"`
	}
	callHTTPTool("link", map[string]any{
		"from_id":    "e2e-http/fact/prd-001",
		"to_id":      "e2e-http/fact/api-001",
		"edge_type":  "implements",
		"confidence": 1.0,
	}, &linkOut)
	if linkOut.Status == "" {
		t.Fatalf("link: expected non-empty status, got %+v", linkOut)
	}

	var impactOut struct {
		StaleNodes []struct {
			ID string `json:"id"`
		} `json:"stale_nodes"`
	}
	callHTTPTool("check_impact", map[string]any{"id": "e2e-http/fact/prd-001"}, &impactOut)
	found := false
	for _, n := range impactOut.StaleNodes {
		if n.ID == "e2e-http/fact/api-001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("check_impact: expected e2e-http/fact/api-001 stale, got %+v", impactOut.StaleNodes)
	}

	var forgetOut struct {
		Status string `json:"status"`
	}
	callHTTPTool("forget", map[string]any{"id": "e2e-http/fact/prd-001"}, &forgetOut)
	if forgetOut.Status != "deleted" {
		t.Fatalf("forget: expected status=deleted, got %+v", forgetOut)
	}
	callHTTPTool("forget", map[string]any{"id": "e2e-http/fact/prd-001"}, &forgetOut)
	if forgetOut.Status != "not_found" {
		t.Fatalf("forget: expected status=not_found on repeat, got %+v", forgetOut)
	}
}
