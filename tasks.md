# tasks.md — remaining work

Tracked as of 2026-07-20. See `~/Workspace/.hermes/plans/2026-07-19_agent-memory-v1-plan.md`
Standing Rule 7 for the strict Definition of Done these are checked against.

## 1. Tier-3 E2E tests
No test spawns the compiled `tastastas` binary and talks to it over the real
transport. Current tests call Go functions directly (Tier 1/2), which cannot
catch wire-format bugs (this is exactly how the `check_impact` shape bug
slipped through earlier). Need:
- [x] stdio: `os/exec` spawn binary, speak real MCP JSON-RPC framing over
      stdin/stdout, assert on wire responses — `internal/e2e/stdio_test.go`,
      full remember→recall→link→check_impact→forget sequence. Caught a real
      bug on first run: `forget`'s not-found check used bare `==` against a
      wrapped error, always false — fixed with `errors.Is`. (commit `38dc5be`)
- [x] HTTP: real `net/http` client against a real listening socket, hit
      `/mcp`, `/ingest/{adapter}`, health/webhook endpoints —
      `internal/e2e/http_test.go`, two tests: (1) health + webhook
      REST wire shapes, (2) full tool sequence over StreamableClientTransport.
- Floor: one test per transport. Target: one full multi-tool-call sequence
  per transport (remember→recall→link→check_impact→forget). Both met.

## 2. `-race` coverage
- [x] Added `internal/e2e/concurrent_test.go`: real concurrent load (20
      webhook-ingest + 20 health-check requests interleaved) against a real
      running HTTP server, run under `go test -race`.
- [x] Full suite (11 packages) passes clean under `go test ./... -race -count=1`,
      run 3x back to back, zero races detected. `internal/store/sqlite`
      already caps `SetMaxOpenConns(1)` (avoids SQLITE_BUSY), which serializes
      DB access at the connection-pool layer — this test confirms nothing
      above that layer (HTTP handlers, MCP tool closures) introduces a race.
  See commit `34bc19d`.

## 3. `golangci-lint`
- [x] `.golangci.yml` added, enabled: errorlint, gocritic, unused, govet,
      ineffassign, misspell, unconvert. errcheck suppressed for test files only.
- [x] Run once, fix what it flags — caught: bare `err != context.Canceled`
      (errors.Is fix), exitAfterDefer (db.Close before log.Fatalf), empty
      error branch (ulid.go), unused func (docwalk_test.go). All fixed.
- [x] Wire into standard verify command (golangci-lint run ./... passes clean)
  See commit `1e1932f`.

## 4. Dedup threshold recalibration
Current `dedupe.DefaultThreshold = 0.80` derived from a small sample (13
facts, 78 pairs, 4 hand-identified known-dup pairs) built from repo test
fixtures, not real conversation data — flagged as a v1 placeholder in
`internal/dedupe/dedupe.go`'s doc comment.
- [ ] Re-run `prototype/scoring.py` against a larger, more diverse
      real-conversation corpus once available
- [ ] Update `DefaultThreshold` + doc comment with the new derived value

## 5. Phase 7 — docs + release
Blocked on 1-4 being closed out per current sequencing (do the fill-in pass
first, then finalize).
- [ ] Full README rewrite: pitch, quickstart, architecture diagram, config
      reference, roadmap
- [ ] Tag `v0.1.0`
