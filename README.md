# tastastas

Agentic memory engine: a typed graph+vector+lexical hybrid store for AI
agents, with automatic conversation-derived memory, pluggable document
ingestion (git repos, Obsidian vaults, structured doc trees like
PRD/API-spec/ERD/test-case sets), and cross-doc/cross-service
**change-impact detection** — when a doc or service changes, tastastas tells
you what else might now be stale.

Single Go binary. SQLite by default. Runs fully offline. No accounts, no
tenancy, no server required unless you want one.

## Why not just RAG

Flat chunk+cosine retrieval has no entity identity (same fact re-embedded and
duplicated), no decay, and no graph structure — it can't answer "what connects
to X" or "what's now stale because Y changed." tastastas keeps typed nodes and
typed edges (`implements`, `tests`, `specifies`, `depends-on`, ...) so recall
is `relevance * recency * importance`, not just top-k similarity, and doc/graph
changes propagate a "possibly stale" signal to connected nodes automatically.

## Quickstart

Requires a local [Ollama](https://ollama.com) for extraction/embedding tools
(`remember`/`recall`/`link`/`forget`/`ingest`/`check_impact` work with zero
LLM dependency; only `extract_and_remember` needs Ollama).

```bash
go build -o tastastas ./cmd/tastastas

# stdio mode (default) — for embedding into an MCP client (Claude Desktop,
# Claude Code, any MCP-compatible agent). Point your MCP client config at
# the compiled binary; it speaks MCP over stdin/stdout.
./tastastas --db memory.db --embed-dim 768

# HTTP mode — shared team instance + webhook ingestion.
./tastastas --db memory.db --embed-dim 768 --serve :8080
```

`--embed-dim` must match your embedder's output dimension (768 for
`nomic-embed-text`, 384 for many `sentence-transformers` models). Vectors of
the wrong dimension are rejected at insert time.

### Point it at a doc tree

```bash
# via HTTP
curl -X POST localhost:8080/ingest/docwalk \
  -d '{"root": "/path/to/docs", "config_path": ".memoryrc.yaml", "project_id": "my-project"}'

# or via the "ingest" MCP tool from your agent client, same fields
```

### Recall

```bash
curl -X POST localhost:8080/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":{"project_id":"my-project","query":"coupon redemption"}}}'
```

Or just ask your MCP-connected agent to "recall what you know about coupon
redemption" — it calls the tool for you.

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
N-hop graph neighbors with an edge-confidence boost — so recalling one fact
also surfaces what it's connected to.

**Change impact**: `check_impact` walks outgoing edges of impact-bearing
types (`implements`, `tests`, `specifies`, `depends-on`) from a changed node
and marks reachable nodes `status: stale` — the automatic "what might now be
wrong" signal.

**Dedup**: `extract_and_remember` embeds each extracted fact, cosine-searches
existing nodes of the same kind in the project, and merges (reuses the
existing ID) above a calibrated similarity threshold instead of inserting a
duplicate. See `internal/dedupe/dedupe.go`'s package doc for the empirical
calibration this threshold is derived from.

## MCP tools

| Tool | Purpose | Ollama required? |
|---|---|---|
| `remember` | Store/update a fact or entity directly (explicit content, no LLM) | No |
| `recall` | Lexical search with relevance × recency × importance scoring + graph pull-in | No |
| `forget` | Delete a node by ID | No |
| `link` | Create a typed, directed edge between two nodes | No |
| `ingest` | Ingest documents from a filesystem root via a named adapter | No |
| `check_impact` | Mark downstream nodes stale after a change | No |
| `extract_and_remember` | Extract facts from raw conversation text, dedupe, store | **Yes** |

## Ingestion adapters

- **`docwalk`** — structured doc trees matching a configurable glob→type
  mapping (e.g. `{Module}/PRD/{feature}/*.md`, `{Module}/APISpec/*.md`, ...).
  Config: `.memoryrc.yaml` (see `internal/ingest/docwalk/testdata/acme-style/.memoryrc.yaml`
  for a worked example). Cross-links same-feature docs automatically via a
  named regex capture group.
- **`gitrepo`** — walks a directory tree for `MEMORY.md` files (or a
  configurable glob), skipping `.git`/`node_modules`.
- **`obsidian`** — Obsidian vault: YAML frontmatter + `[[wikilink]]` edge
  extraction.

## `.memoryrc.yaml` config reference (docwalk adapter)

```yaml
project_id: my-project
mappings:
  - path_glob: "*/PRD/**/*.md"        # glob relative to ingest root
    type: prd                          # node_type assigned to matches
    group_by: "^[^/]+/PRD/(?P<feature>[^/]+)/"  # optional: named regex
                                        # group used to cross-link docs
                                        # sharing the same feature slug
  - path_glob: "*/APISpec/*.md"
    type: api-spec
    group_by: "^[^/]+/APISpec/(?P<feature>[^.]+)\\.md$"
```

Every mapping's matched files become nodes of `type`; files sharing the same
`group_by` capture across different `type`s get cross-linked with an
impact-bearing edge automatically (e.g. a PRD and its API spec for the same
feature).

## HTTP server mode

```
POST /mcp                     — MCP protocol over Streamable HTTP
POST /ingest/{adapter}        — REST ingest: {"root", "config_path", "project_id"}
POST /ingest/webhook          — generic doc-push: {"path", "content", "project_id", ...}
GET  /health                  — {"status": "ok", "version": "0.1.0"}
```

## Development

```bash
go vet ./... && go build ./... && go test ./... -count=1   # unit + integration
go test ./... -race -count=1                                # race detector
golangci-lint run ./...                                     # lint (errorlint,
                                                              # gocritic, etc.)
go test ./internal/e2e/... -v -count=1                       # E2E: spawns the
                                                              # real binary,
                                                              # speaks real
                                                              # MCP wire
                                                              # protocol
                                                              # (stdio + HTTP)
```

Requires local Ollama for the `extract_and_remember` integration test and the
E2E suite's tool-sequence tests (`qwen3.5:2b-q4_K_M` or similar for
extraction, `nomic-embed-text` for embeddings). Other tests need no external
services.

## Roadmap

- **pgvector backend** — optional Postgres store for teams already running
  Postgres, as an alternative to SQLite. Not required for OSS/solo use.
- **Kuzu backend** — optional embedded graph DB for larger graphs where
  SQLite's edge-walk queries become the bottleneck. Not required for OSS use.
- **Auth (opt-in)** — HTTP server mode currently has no auth layer; fine for
  a trusted local network or single-tenant internal deployment, not yet
  suitable for exposing publicly. Explicitly deferred: not required for the
  primary solo/offline and trusted-team use cases this v0.1.0 targets.
- **Larger dedup calibration corpus** — current threshold (`internal/dedupe`)
  is derived from a real but small sample (22 facts, one project, one
  author); re-calibrate against a larger, more diverse real-conversation
  corpus as usage grows.

## Status

v0.1.0 — functional: all 7 MCP tools, 3 ingestion adapters, stdio + HTTP
transport, retrieval scoring, change-impact detection, extract+dedupe
pipeline. See `tasks.md` for what's still open beyond this release.

## License

MIT — see `LICENSE`.
