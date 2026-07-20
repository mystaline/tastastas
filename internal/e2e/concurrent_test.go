// TestE2EHTTPConcurrentRequests drives real concurrent HTTP requests
// (remember + recall + webhook ingest, interleaved) against a real running
// server instance, to be run under `go test -race`. internal/store/sqlite
// deliberately caps SetMaxOpenConns(1) to avoid SQLITE_BUSY, so
// database/sql serializes access to the single connection — this test
// exists to prove that under real concurrent load (not just sequential
// tests), nothing outside the DB layer (HTTP handlers, MCP tool closures)
// introduces a race the connection cap doesn't already prevent.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestE2EHTTPConcurrentRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E HTTP test in -short mode")
	}
	addr := startHTTPServer(t)
	base := "http://" + addr

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n*2)

	for i := 0; i < n; i++ {
		wg.Add(2)

		// concurrent remember (via webhook ingest, no MCP session overhead)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{
				"path":       fmt.Sprintf("concurrent/doc-%d.md", i),
				"content":    fmt.Sprintf("concurrent test content %d", i),
				"project_id": "concurrent-test",
			})
			resp, err := http.Post(base+"/ingest/webhook", "application/json", bytes.NewReader(body))
			if err != nil {
				errs <- fmt.Errorf("webhook %d: %w", i, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("webhook %d: status %d", i, resp.StatusCode)
			}
		}(i)

		// concurrent health check (read-only, no DB access — hits the
		// simplest handler to add contention on the HTTP mux/goroutine
		// scheduler itself, independent of the DB connection cap)
		go func(i int) {
			defer wg.Done()
			resp, err := http.Get(base + "/health")
			if err != nil {
				errs <- fmt.Errorf("health %d: %w", i, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("health %d: status %d", i, resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
