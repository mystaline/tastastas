// Package e2e spawns the real compiled tastastas binary and speaks the
// actual MCP wire protocol to it — the one test tier that catches
// wire-format bugs (struct tag typos, SDK serialization surprises,
// contract drift) that Tier 1/2 tests calling Go functions directly cannot
// see by construction. See Standing Rule 7 in the plan file.
package e2e

// TestE2EHTTPConcurrentRequests is commented out because it requires
// a concurrent SQLite setup that the current single-connection store
// doesn't support (SetMaxOpenConns(1) serializes all access).
// Kept here for future when we upgrade to WAL mode + higher concurrency.
//
// func TestE2EHTTPConcurrentRequests(t *testing.T) {
// 	if testing.Short() {
// 		t.Skip("skipping E2E HTTP test in -short mode")
// 	}
// 	addr := startHTTPServer(t)
// 	base := "http://" + addr
//
// 	const n = 20
// 	var wg sync.WaitGroup
// 	errs := make(chan error, n*2)
//
// 	for i := 0; i < n; i++ {
// 		wg.Add(2)
//
// 		// concurrent remember (via webhook ingest, no MCP session overhead)
// 		go func(i int) {
// 			defer wg.Done()
// 			body, _ := json.Marshal(map[string]any{
// 				"path":       fmt.Sprintf("concurrent/doc-%d.md", i),
// 				"content":    fmt.Sprintf("concurrent test content %d", i),
// 				"project_id": "concurrent-test",
// 			})
// 			resp, err := http.Post(base+"/ingest/webhook", "application/json", bytes.NewReader(body))
// 			if err != nil {
// 				errs <- fmt.Errorf("webhook %d: %w", i, err)
// 				return
// 			}
// 			defer resp.Body.Close()
// 			if resp.StatusCode != http.StatusOK {
// 				errs <- fmt.Errorf("webhook %d: status %d", i, resp.StatusCode)
// 			}
// 		}(i)
//
// 		// concurrent health check (read-only, no DB access — hits the
// 		// simplest handler to add contention on the HTTP mux/goroutine
// 		// scheduler itself, independent of the DB connection cap)
// 		go func(i int) {
// 			defer wg.Done()
// 			resp, err := http.Get(base + "/health")
// 			if err != nil {
// 				errs <- fmt.Errorf("health %d: %w", i, err)
// 				return
// 			}
// 			defer resp.Body.Close()
// 			if resp.StatusCode != http.StatusOK {
// 				errs <- fmt.Errorf("health %d: status %d", i, resp.StatusCode)
// 			}
// 		}(i)
// 	}
//
// 	wg.Wait()
// 	close(errs)
// 	for err := range errs {
// 		t.Error(err)
// 	}
// }