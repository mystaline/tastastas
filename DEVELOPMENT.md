# Development

## Architecture

```
                    ┌─────────────────────────┐
  MCP client  ──────▶  stdio or /mcp (HTTP)    │
 (agent/IDE)         │  7 tools: remember,      │
                    │  recall, forget, link,   │
                    │  ingest, check_impact,   │
                    │  extract_and_remember    │
                    └────────────┬─────────────┘
                                 │
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
  ┌─────▼─────┐          ┌───────▼──────┐         ┌───────▼───────┐
  │  extract   │          │   retrieve   │         │  dedupe/embed  │
  │ (Ollama    │          │ (relevance × │         │ (cosine sim on │
  │  LLM call) │          │  recency ×   │         │  nomic-embed-  │
  │            │          │  importance, │         │  text vectors) │
  │            │          │  N-hop graph │         │                │
  │            │          │  pull-in)    │         │                │
  └─────┬──────┘          └───────┬──────┘         └───────┬────────┘
        │                         │                        │
        └────────────────┬────────┴────────────┬───────────┘
                          │                     │
                   ┌──────▼─────────────────────▼──────┐
                   │   store (SQLite: FTS5 + sqlite-vec) │
                   │   nodes (typed) + edges (typed)     │
                   └──────┬───────────────────────────────┘
                          │
        ┌──────────────────┼──────────────────┐
        │                  │                  │
  ┌─────▼─────┐    ┌───────▼──────┐    ┌──────▼───────┐
  │  docwalk   │    │   gitrepo    │    │   obsidian    │
  │ (structured │    │ (MEMORY.md   │    │ (frontmatter  │
  │  doc trees, │    │  files)      │    │  + wikilinks) │
  │  PRD/API/   │    │              │    │               │
  │  ERD/...)   │    │              │    │               │
  └─────────────┘    └──────────────┘    └───────────────┘
```

**Store**: typed nodes (fact, entity, prd, api-spec, erd, test-case,
generic-doc, ...) and typed directed edges (`implements`, `tests`,
`specifies`, `depends-on`, `related`, ...). Backed by SQLite: FTS5 for
lexical search, `sqlite-vec` for cosine similarity, both pure-Go
(`modernc.org/sqlite`, no cgo).

**Retrieve**: `recall` scores candidates by
`relevance * recency_decay(2^(-age/halfLife)) * importance`, then pulls in
N-hop graph neighbors with an edge-confidence boost (via
`store.NeighborsWithConfidence`, which tracks the actual edge that
discovered each neighbor during the BFS walk — not index alignment between
two unrelated slices) — so recalling one fact also surfaces what it's
connected to.

**Change impact**: `check_impact` walks outgoing edges of impact-bearing
types (`implements`, `tests`, `specifies`, `depends-on`) from a changed node
and marks reachable nodes `status: stale` — the automatic "what might now be
wrong" signal.

**Dedup**: `extract_and_remember` embeds each extracted fact, cosine-searches
existing nodes of the same kind in the project, and merges (reuses the
existing ID) above a calibrated similarity threshold instead of inserting a
duplicate. See `internal/dedupe/dedupe.go`'s package doc for the empirical
calibration this threshold is derived from (currently 0.71, calibrated
against real same-register fact pairs).

## Dev commands

```bash
go vet ./... && go build ./... && go test ./... -count=1   # unit + integration
go test ./... -race -count=1                                # race detector
golangci-lint run ./...                                     # lint (errorlint, gocritic, etc.)
go test ./internal/e2e/... -v -count=1                       # E2E: spawns the real
                                                              # binary, speaks real
                                                              # MCP wire protocol
                                                              # (stdio + HTTP)
```

Requires local Ollama for the `extract_and_remember` integration test and
the E2E suite's tool-sequence tests (`qwen3.5:2b-q4_K_M` or similar for
extraction, `nomic-embed-text` for embeddings). Other tests need no external
services.

## Testing tiers

1. **Unit** — pure functions (dedupe scoring, ULID generation, fact parsing).
2. **Integration** — real SQLite (`:memory:`), no process boundary.
3. **E2E** (`internal/e2e/`) — spawns the actual compiled binary, speaks
   real MCP JSON-RPC over stdio and HTTP. Catches wire-format bugs unit/
   integration tests structurally cannot (e.g. a field-name mismatch
   between what the server sends and what a real client expects). Also
   includes wire-contract tests (struct-tag introspection vs the plan's
   documented contract) and a real-concurrent-load test for `-race`.
4. **Non-functional** — `-race` clean, `golangci-lint` clean (errorlint
   catches bare `err == sentinel` on wrapped errors — a real bug class
   this caught twice during development).

## Known gaps / roadmap

- **pgvector backend** — optional Postgres store for teams already running
  Postgres, as an alternative to SQLite. Not required for OSS/solo use.
- **Kuzu backend** — optional embedded graph DB for larger graphs where
  SQLite's edge-walk queries become the bottleneck. Not required for OSS use.
- **Auth (opt-in)** — HTTP server mode currently has no auth layer; fine for
  a trusted local network or single-tenant internal deployment, not yet
  suitable for exposing publicly. Explicitly deferred: not required for the
  primary solo/offline and trusted-team use cases v0.1.0 targets.
- **Larger dedup calibration corpus** — current threshold is derived from a
  real but small sample (22 facts, one project, one author); re-calibrate
  against a larger, more diverse real-conversation corpus as usage grows.

See `tasks.md` for the full task-level history of what's been verified and
what's still open.
