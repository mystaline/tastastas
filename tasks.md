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
- [x] Re-ran calibration against real data: 22 facts from a personal
      side-project's Claude Code session memory (Regista, no NDA concerns).
      Each fact has 2 independently-authored terse phrasings (MEMORY.md
      index line + frontmatter description) — genuine same-register
      same-fact-reworded pairs, not invented paraphrases.
- [x] Same-register true-pos (n=22): min=0.641 median=0.877 max=0.939.
      Same-register true-neg (n=231): min=0.340 median=0.532 p95=0.647
      max=0.710. Clean separation, much better than v1's noisy
      mismatched-register synthetic run.
- [x] `DefaultThreshold` updated 0.80 → 0.71 (0.8% error rate on this
      sample, zero wrong-merges). Doc comment in `dedupe.go` fully
      documents both v1 and v2 calibration runs.
  See commit `578e2f4`.

## 5. Phase 7 — docs + release
- [x] Full README rewrite: pitch, quickstart (build/run commands verified
      against actual `--help` output), architecture diagram, MCP tool
      table, adapter list, `.memoryrc.yaml` config reference (verified
      against real testdata fixture), HTTP route table (verified against
      `http.go` source), dev/verify commands, roadmap.
      See commit `adc1fea`.
- [x] Tagged `v0.1.0`.

All 5 items closed. v0.1.0 shipped.
