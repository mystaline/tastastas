# tastastas

**Agentic memory for AI agents.** Typed graph + vector + lexical hybrid store. Single Go binary, SQLite offline.

Store facts, search by relevance × recency × importance, get "this might be stale" alerts when connected docs change.

Data sources: doc repos (PRDs, ERDs, APIs, spec docs), codebases (Go, TypeScript, JavaScript, Python, Rust — full AST + imports), git history, conversation facts, and custom notes — all stored in one graph DB with zero external services.

## Quick Start

See [DEPLOYMENT.md](./DEPLOYMENT.md) for delivery options (binary, Docker, build from source).

## MCP Configuration

Pick the block matching your setup. See [DEPLOYMENT.md](./DEPLOYMENT.md#quick-run-cheatsheet) for run commands.

Only Claude Code and Kilo Code shown below — other agents just adjust to that agent's own MCP config format/file.

**HTTP (Docker Compose):**

Claude Code:
```json
{
  "mcpServers": {
    "tastastas": {
      "type": "http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Kilo Code:
```json
"tastastas": {
  "type": "remote",
  "url": "http://localhost:8080/mcp",
  "enabled": true
}
```

**Stdio (sidecar):**

Claude Code:
```json
{
  "mcpServers": {
    "tastastas": {
      "type": "stdio",
      "command": "/path/to/tastastas",
      "args": [
        "--db", "~/.local/share/tastastas/memory.db",
        "--graph-addr", ":9292",
        "--consolidate-interval", "1h"
      ]
    }
  }
}
```

Kilo Code:
```json
"tastastas": {
  "type": "local",
  "command": [
    "/path/to/tastastas",
    "--db", "~/.local/share/tastastas/memory.db",
    "--graph-addr", ":9292",
    "--consolidate-interval", "1h"
  ],
  "enabled": true
}
```

**Stdio + Ollama:**

Claude Code:
```json
{
  "mcpServers": {
    "tastastas": {
      "type": "stdio",
      "command": "/path/to/tastastas",
      "args": [
        "--db", "~/.local/share/tastastas/memory.db",
        "--embed-backend", "ollama",
        "--graph-addr", ":9292",
        "--consolidate-interval", "1h"
      ]
    }
  }
}
```

Kilo Code:
```json
"tastastas": {
  "type": "local",
  "command": [
    "/path/to/tastastas",
    "--db", "~/.local/share/tastastas/memory.db",
    "--embed-backend", "ollama",
    "--graph-addr", ":9292",
    "--consolidate-interval", "1h"
  ],
  "enabled": true
}
```

**Stdio + OpenAI:**

```bash
# Prefer env var (not in process list)
export TASTASTAS_OPENAI_KEY=sk-...
./tastastas --embed-backend openai --consolidate-interval 1h
```

Claude Code:
```json
{
  "mcpServers": {
    "tastastas": {
      "type": "stdio",
      "command": "/path/to/tastastas",
      "args": [
        "--db", "~/.local/share/tastastas/memory.db",
        "--embed-backend", "openai",
        "--graph-addr", ":9292",
        "--consolidate-interval", "1h"
      ],
      "env": {
        "TASTASTAS_OPENAI_KEY": "sk-..."
      }
    }
  }
}
```

Kilo Code:
```json
"tastastas": {
  "type": "local",
  "command": [
    "/path/to/tastastas",
    "--db", "~/.local/share/tastastas/memory.db",
    "--embed-backend", "openai",
    "--graph-addr", ":9292",
    "--consolidate-interval", "1h"
  ],
  "env": {
    "TASTASTAS_OPENAI_KEY": "sk-..."
  },
  "enabled": true
}
```

### Agent Integration

Add this to your `.cursorrules`, `CLAUDE.md`, `AGENTS.md`, or other agent entrypoint:

```markdown
## Memory (tastastas)
tastastas is an MCP server connected to this project. If you see tastastas in your available MCP tools, always call the `init` tool as the first step of every session to understand available tools and best practices.
```

## Hardware

See [DEPLOYMENT.md](./DEPLOYMENT.md#hardware--tuning) for hardware requirements and sidecar tuning.

## How it works

```
Source → [AutoDetectAdapters] → n + edges + rawCalls → [CrossFileLinker]
  → [EmbedNodes] → n → [InferConventions] → [BuildHierarchy]
  → [DocLinkExtraction] → [Tier2ScoreAndLink] → [CrossProjectLink]
  → edges tagged EXTRACTED / INFERRED / PROPOSED
```

1. **Adapters** walk a directory — docwalk (`.memoryrc.yaml` mapping), codeast (Go AST, tree-sitter for TS/Python/Rust), gitrepo, obsidian, markdown-glob. Each returns typed nodes + structural edges + raw calls (unresolved function references from tree-sitter).
2. **CrossFileLinker** resolves raw calls against a global label index built from all ingested nodes — auto-links cross-module `calls` edges.
3. **EmbedNodes** batch-embeds node content (ollama, openai, or sidecar ONNX).
4. **InferConventions** detects naming patterns from code symbols (`Get*`/`handle*` prefixes).
5. **BuildHierarchy** generates synthetic `directory` nodes connected by `contains` edges — every file becomes reachable from the repo root. Single-child pass-through folders collapse.
6. **DocLinkExtraction** parses .md files for inline/wikilink/reference-style links to existing nodes — creates `references` edges.
7. **Tier2ScoreAndLink** pairs every two nodes: `0.4·cos + 0.2·typeCompat + 0.2·pathProximity + 0.2·identifierOverlap - templateCollisionPenalty`. Above 0.80 = `INFERRED`. Above 0.55 = `PROPOSED`. Skipped below. Also runs cross-project linking — matches code symbols by name across projects.
8. **Consolidation** (background cron) — session co-occurrence analysis creates `co-accessed` `INFERRED` edges via `--consolidate-interval`.
9. **Graph view** at `GET /graph/{project_id}` renders edges tagged EXTRACTED (structural) + INFERRED (auto-linked) as a force-directed D3 visualization. PROPOSED edges hidden — use `query_graph` to inspect.

### Model identity & isolation

Embedding models produce different vector spaces. tastastas stores vectors with composite primary keys (`{model_id}:{node_id}`) so multiple models coexist per project without conflict:

- Switching models adds new vectors — recall only searches vectors matching the current model, not all models
- Per-model dirty/clean status tracks crash residue without affecting other models
- Recall filters by the current session's model_id automatically
- Models can be listed via `init` tool per project
- Re-ingest after crash is idempotent (content-hash skip)

Embeds use API probes to auto-detect dimension — no `--embed-dim` flag needed for most setups.

### Architecture notes

- **Typed edges** — `implements` (API Spec→PRD), `specifies` (ERD→PRD), `tests` (TestCases→PRD), `contains` (directory→file), `calls` (code→code), `references` (type annotations, doc links), `depends-on` (via link tool or package.json), `co-accessed` (session co-occurrence). Each edge carries a `ConfidenceTier`: `EXTRACTED` (structural, 1.0), `INFERRED` (Tier 2 similarity > 0.80, session co-occurrence), or `PROPOSED` (review queue, 0.55–0.80).
- **Template collision penalty** (`-0.20`) — same-filename docs in different directories don't auto-link anymore (previously files with same name in different features scored false 1.0 on identifier overlap, clearing the auto-link gate).
- **4-tier linking** — `EXTRACTED` (structural, 1.0) → `INFERRED` (Tier 2 ~0.80, co-occurrence, conventions) → `PROPOSED` (review queue, 0.55–0.80) → Explicit (manually linked via `link` tool).
- **External content FTS5** — SQLite's built-in full-text search, no Elasticsearch.
- **vec0 extension** — embedded vector search, no vector DB separate.
- **Store interface** — abstracted. Swap sqlite→libsql (Turso) without changing callers.
- **Recency decay** — `2^(-age / halfLife)` applied at recall time.
- **RRF fusion** — uses Reciprocal Rank Fusion to merge FTS5 + vector + graph scores without distribution assumptions.

## Tools (MCP)

| Group | Tool | What it does | Embedder needed? | LLM needed? |
|-------|------|-------------|-----------------|------------|
| **Session start** | `init` | Capability overview — call first every session | No | No |
| | | | | |
| **Store / Delete** | `remember` | Store/update a fact | No | No |
| | `forget` | Delete node by ID | No | No |
| | | | | |
| **Content retrieval** | `recall` | Search content by relevance × recency × importance | No* | No |
| | `recall_chunks` | Paginated chunk retrieval by parent + index | No | No |
| | | | | |
| **Graph** | `link` | Connect two nodes with typed edge | No | No |
| | | | | |
| **Ingestion / Impact** | `ingest` | Walk doc/code tree → searchable memory | No* | No |
| | `onboard` | Full pipeline: detect → ingest → embed → link | No* | No |
| | `onboard_check` | Check graph state for a project (read-only stats) | No | No |
| | `check_impact` | "What's stale because this changed?" | No | No |
| | | | | |
| **Graph / Discovery** | `query_graph` | Query edges from/to a specific node | No | No |
| | `project_graph` | Return all edges — whole project graph shape | No | No |
| | `find_path` | BFS shortest path between two nodes | No | No |
| | `link_projects` | Link current project's code symbols to other known projects | No | No |
| | | | | |
| **Maintenance** | `clear_project` | Delete a project's data (nodes, edges, vectors) | No | No |
| | `list_projects` | List all known projects with node/edge counts | No | No |
| | `check_recent` | List nodes updated within N days | No | No |
| | | | | |
| **LLM extraction** | `extract_and_remember` | Pull facts from raw conversation text | **Yes** | **Yes** |
| | | | | |
| **Async** | `job_status` | Poll async job (onboard, ingest, extract) | No | No |

`*` = degrades gracefully to FTS5-only (lexical). Vector/semantic features skip without embedder. ONNX sidecar is zero-dependency and baked into the binary — run `scripts/build-sidecar.sh` once, no Ollama needed.

## API endpoints (`--serve` mode)

### `GET /health` — Readiness probe

```
→ 200  {"status":"ok","version":"v0.1.0"}
```

Version reflects the binary build tag (via `-ldflags`). Exempt from auth. Used by Docker health checks and load balancers.

### `POST /ingest` — Ingest repo path into memory

Async. Returns immediately with a job ID. Poll `GET /ingest/jobs/{id}` for completion.
Runs full pipeline: auto-detect adapters → chunk → embed → hierarchy → Tier2 link.

```
Request:
{ "root": "/path/to/repo", "project_id": "my-project" }

→ 202  {"job_id":"<ulid>","status":"running"}
→ 400  {"error":"root is required"}          — missing root
→ 400  {"error":"invalid JSON"}              — malformed body
```

Idempotent: unchanged files skip re-chunk + re-embed (content-hash skip).

### `GET /ingest/jobs/{id}` — Poll ingest job

```
→ 200  {"id":"<ulid>","status":"running|done|error","phase":"detecting|chunking|embedding|persisting",
         "nodes":5,"edges":3,"chunks":42,"error":"","started_at":"...","ended_at":"..."}
→ 404  {"error":"job not found"}
```

### `GET /graph/{project}` — Graph visualization

```
?max_edges=2000  (default 2000)

Accept: application/json
→ 200  {"project_id":"my-project","total_edges":36000,"returned":500,
         "nodes":[{"id":"...","title":"...","type":"...","group":"...","weight":2},...],
         "edges":[{"source":"...","target":"...","edge_type":"contains","confidence":1.0,"confidence_tier":"EXTRACTED"},...]}

Accept: text/html (default)
→ 200  D3 force-directed graph HTML page
```

### `POST /mcp` — MCP Streamable HTTP (all 20 tools)

See [Tools (MCP)](#tools-mcp) section below. Request/response follows the [MCP Streamable HTTP spec](https://spec.modelcontextprotocol.io/).

## Ingesting docs

Three adapters. Point one at a folder:

- **`docwalk`** — structured doc trees (PRD/APISpec/ERD) via `.memoryrc.yaml` glob→type mapping. Auto cross-links same-feature docs.
- **`gitrepo`** — walks a tree for files matching a glob pattern (default: `MEMORY.md`, configurable).
- **`obsidian`** — vault frontmatter + `[[wikilinks]]` become typed nodes/edges.

```bash
# From curl — httpie or HTTP mode
curl -X POST localhost:8080/ingest \
  -d '{"cwd": "/path/to/docs", "project_id": "my-project"}'

# From MCP client
ingest project_id=my-project cwd=/path/to/docs
```

`.memoryrc.yaml` example (optional, only needed for typed cross-linking):

```yaml
project_id: my-project
mappings:
  - path_glob: "**/PRD/**/00-index.md"
    type: prd
    group_by: "PRD/(?P<feature>[^/]+)/"
```

Without `.memoryrc.yaml` everything ingests as `generic-doc` — still searchable, just untyped.

### Auto-update on git push (CI/CD)

Tastastas `POST /ingest` is designed for CI/CD pipelines. After every git push, re-ingest the repo:

```yaml
# .github/workflows/tastastas-sync.yml (or any CI)
on:
  push:
    branches: [main]
jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Update tastastas memory
        run: |
          curl -X POST http://your-server:8080/ingest \
            -d '{"root": "$PWD", "project_id": "my-project"}'
```

Ingest is idempotent — unchanged files keep their existing chunks and embeddings (content-hash skip). Only changed files are re-processed.

## Technology Stack

| Language | Role | What it does |
|----------|------|-------------|
| **Go** | Core application | MCP server, ingest pipeline, graph database, retrieval, chunking, SQLite store. All production code in `internal/`. |
| **Rust** | Embedding sidecar | `tastastas-embed` — bge-small-en-v1.5 ONNX model compiled into standalone binary. Embedded into Go binary via `//go:embed`. JSON-RPC over stdin/stdout. |
| **Python** | Data science / calibration | `prototype/scoring.py` — extraction prompt calibration, dedup threshold derivation, embedding quality benchmarks. Not in production binary. |

### Expansion Paths

**Data engineers (Python):**
- Fine-tune embedding models on domain-specific data → export ONNX → replace `sidecar/assets/model.onnx`
- Calibrate RRF (Reciprocal Rank Fusion) weights via `prototype/scoring.py` benchmarks
- Evaluate chunking strategies against recall quality metrics

**Backend engineers (Go):**
- Add new ingest adapters in `internal/ingest/` (Confluence, Notion, Google Docs)
- Swap SQLite → PostgreSQL/turso via `store.Store` interface
- Add authentication providers beyond bearer token

**ML engineers (Rust):**
- Swap bge-small-en-v1.5 with larger models (bge-large, multilingual)
- Add GPU-accelerated ONNX Runtime execution
- Implement cross-encoder reranking in sidecar

## Development

```
go build ./...        — build all
go test ./...         — run all tests
go vet ./...          — static analysis
```

See `docs/architecture.md` for full architecture, `docs/demo-walkthrough.md` for end-to-end verification against the example-vault vault (800+ nodes, 36k edges).

## How it's different from plain RAG

Flat chunk+cosine can't tell two mentions of the same fact apart, doesn't decay, and has no graph — can't answer "what connects to X" or "what's stale because Y changed." tastastas has typed nodes/edges, recency decay, importance gating, graph neighbor pull-in, and change propagation.

Uses RRF fusion (Reciprocal Rank Fusion) instead of linear `α·BM25 + (1-α)·cosine` — removes dependence on embedder score distributions. Same approach documented by:

- [Azure AI Search hybrid retrieval ranking](https://learn.microsoft.com/en-us/azure/search/hybrid-search-ranking)
- [Elasticsearch RRF hybrid search](https://www.elastic.co/guide/en/elasticsearch/reference/current/rrf.html)

Retrieval pipeline:

| Method | Role | Reference |
|--------|------|-----------|
| FTS5 BM25 | Lexical keyword search (always runs) | [SQLite FTS5](https://www.sqlite.org/fts5.html) |
| Cosine similarity | Semantic vector distance | Standard linear algebra |
| RRF | Fuses lexical + vector + graph ranks | [Azure AI Search](https://learn.microsoft.com/en-us/azure/search/hybrid-search-ranking), [Elasticsearch](https://www.elastic.co/guide/en/elasticsearch/reference/current/rrf.html) |
| Recency decay | `2^(-age / halfLife)` time decay | Standard exponential decay |
| Importance weighting | Per-node significance multiplier | Application-defined (stored per node) |
| Graph neighbor pull-in | BFS from top hits, confidence-boosted | Custom (graph traversal + heuristic boost) |
| Cross-source linking | Pairwise cosine between chunks from different adapters | Custom (implicit semantic edge detection) |

## Why SQLite FTS5 (and not PostgreSQL FTS / Elasticsearch)

tastastas uses SQLite FTS5 because it fits the use case:

| Scenario | SQLite FTS5 fine? | If not, use |
|----------|-------------------|-------------|
| Single-team vault, <100k nodes, Go binary | ✅ Yes — zero-config, portable, same DB for everything | — |
| Need fuzzy search, synonyms, language-specific stemming | ❌ No — FTS5 has Porter stemmer only | PostgreSQL FTS or Elasticsearch |
| Multi-node HA, billions of docs | ❌ No — single file | Elasticsearch / Solr |
| Offline / air-gapped / no Docker | ✅ Yes — one binary, no services | — |
| Per-field BM25 weights, custom scoring | ❌ No — built-in BM25 only | PostgreSQL FTS (`ts_rank`) or Elasticsearch (function score) |
| Full highlighting with fragments | ⚠️ Partial — `snippet()` only, no fragment builder | Elasticsearch highlight API |
| Your data fits on one machine | ✅ Yes — simpler, faster to iterate | — |

The `store.Store` interface is already abstracted. Swap SQLite for libsql (Turso) when you need distributed reads — same code path, no caller changes.

PostgreSQL (`pgvector` + `tsvector`) is on the roadmap — the interface is ready, just needs the adapter implementation.

## tastastas vs Graphify

Not competing. [Graphify](https://github.com/Graphify-Labs/graphify) (96.5k ★, YC S26) is excellent at what it does. tastastas is just a simpler thing: a centralized memory server your AI talks to. Usage is dead simple — tell your agent "ingest this folder" and "recall what you know about X." Minimal config: one MCP entry, optional one `.memoryrc.yaml` if you want typed docs. No project setup, no per-repo install, no files to commit.

| Angle | tastastas | Graphify |
|-------|-----------|----------|
| **What it is** | Agentic memory server (Go, SQLite, running daemon) | Codebase knowledge graph CLI (Python, static files) |
| **Consumer** | AI agent querying a structured knowledge graph | AI agent reading a static graph.json + report |
| **Storage** | Persistent SQLite DB — add/update/delete facts anytime | Static `graphify-out/` files, committed to git |
| **Retrieval** | Hybrid: FTS5 + vector (vec0) + graph + recency + importance + RRF | Pure graph traversal — no embeddings, no vectors |
| **Code analysis** | Go (full) + TS/JS/Python/Rust (tree-sitter) | 40+ languages via tree-sitter AST |
| **Non-code content** | Docs, PRDs, ERDs, specs, conventions, code, git history, conversation facts, custom notes | Docs, PDFs, images, video/audio, office files |
| **Recency / Staleness** | Built-in: `2^(-age / halfLife)` decay + stale propagation on change | Snapshot at build time. `--update` / `watch` / git hooks for differential re-sync. No query-time decay or propagation |
| **Live updates** | CRUD at any time — `remember`, `link`, `forget`, `ingest` all work while server is running | Rebuild the graph. Auto-rebuild on git commit via hook |
| **Edge types** | 4 tiers: `EXTRACTED` (1.0) → `INFERRED` (~0.80, co-occurrence) → `PROPOSED` (review, 0.55–0.80) → explicit (manual link) | `EXTRACTED` / `INFERRED` / `AMBIGUOUS` — each edge tagged with confidence tier |
| **Embeddings** | Yes — vec0 extension, ONNX sidecar or Ollama | None — pure graph, no vector store |
| **Output** | MCP tool responses + live D3 graph page at `GET /graph/{project}` | `graph.html`, `GRAPH_REPORT.md`, `graph.json` (git-committable) |
| **MCP** | Native MCP server (stdio or HTTP) | `python -m graphify.serve graph.json` (separate process) |

**tl;dr** — Graphify is better if you need a one-shot codebase map for your AI assistant, supports many languages, and want the output committed to git. tastastas is better if you need a persistent memory server with hybrid search that grows over time — ingest docs, code, conversations, and facts into one DB, query it with recall × recency × importance, and get change-propagation alerts.

## License

MIT — see `LICENSE`.
