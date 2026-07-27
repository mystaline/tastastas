# tastastas

**Agentic memory for AI agents.** Typed graph + vector + lexical hybrid store. Single Go binary, SQLite offline.

Store facts, search by relevance × recency × importance, get "this might be stale" alerts when connected docs change.

Data sources: doc repos (PRDs, ERDs, APIs, spec docs), codebases (Go symbols, call graphs), git history, conversation facts, and custom notes — all stored in one graph DB with zero external services.

## Quick start

```bash
go build -o tastastas ./cmd/tastastas

# stdio mode — for MCP clients (Claude Desktop, Claude Code, etc.)
./tastastas --db ~/.local/share/tastastas/memory.db --embed-dim 768

# HTTP mode — curl, webhooks, shared team instance
./tastastas --db ~/.local/share/tastastas/memory.db --embed-dim 768 --serve :8080
```

```bash
# Ollama (default)
./tastastas --db ~/.local/share/tastastas/memory.db --embed-dim 768 --embed-backend ollama --serve :8080

# Sidecar — zero external deps, baked ONNX embedder
./scripts/build-sidecar.sh
./tastastas --db ~/.local/share/tastastas/memory.db --embed-dim 384 --embed-backend sidecar --serve :8080

# Lexical + graph only — no embedding needed
./tastastas --db ~/.local/share/tastastas/memory.db --embed-backend none --serve :8080
```

Embedding dim auto-detects when unset (384 sidecar, 768 ollama).

## Flags

| Flag | Default | What it does |
|------|---------|-------------|
| `--serve` | *(unset)* | HTTP address (`:8080`). Unset = stdio MCP mode. |
| `--db` | `~/.local/share/tastastas/memory.db` | SQLite DB path (honors `$TASTASTAS_DB`, `$XDG_DATA_HOME`) |
| `--embed-dim` | `0` = auto-detect | Vector dimension: 384 for sidecar, 768 for ollama, 0 = pick from backend |
| `--embed-backend` | `sidecar` | `sidecar` (baked ONNX, zero deps), `ollama`, or `none` |
| `--ollama-url` | `http://localhost:11434` | Ollama URL (`embed-backend=ollama`) |
| `--ollama-model` | `nomic-embed-text` | Ollama embedding model (`embed-backend=ollama`) |
| `--sidecar-workers` | `0` = 4 | Sidecar worker count (`embed-backend=sidecar`) |
| `--graph-addr` | *(unset)* | Graph visualization page on this address (`:9292`). Works alongside stdio MCP mode — no `--serve` needed. |
| `--auth-token` | *(unset)* | Bearer token for HTTP server mode (empty = no auth) |

## How it works

```
Source → [AutoDetectAdapters] → n → [EmbedNodes] → n → [InferConventions]
  → n + conv → [BuildHierarchy] → n + dir + contains → [Tier2ScoreAndLink]
  → auto-linked + proposed edges
```

1. **Adapters** walk a directory — docwalk (`.memoryrc.yaml` mapping), codeast (Go AST), gitrepo, obsidian, markdown-glob. Each returns typed nodes + structural edges.
2. **EmbedNodes** batch-embeds node content (ollama or sidecar ONNX).
3. **InferConventions** detects naming patterns from code symbols (`Get*`/`handle*` prefixes).
4. **BuildHierarchy** generates synthetic `directory` nodes connected by `contains` edges — every file becomes reachable from the repo root. Single-child pass-through folders collapse.
5. **Tier2ScoreAndLink** pairs every two nodes: `0.4·cos + 0.2·typeCompat + 0.2·pathProximity + 0.2·identifierOverlap - templateCollisionPenalty`. Above 0.80 = `auto-linked`. Above 0.55 = `proposed` (review queue). Below = skipped.
6. **Graph view** at `GET /graph/{project_id}` renders structural edges + auto-linked edges as a force-directed D3 visualization. Proposed edges hidden — use `query_graph` to inspect.

### Architecture notes

- **Typed edges** — `implements` (API Spec→PRD), `specifies` (ERD→PRD), `tests` (TestCases→PRD), `contains` (directory→file), `depends-on` (via link tool or manual), `auto-linked` (Tier 2 similarity > 0.80), `proposed` (review queue, 0.55–0.80).
- **Template collision penalty** (`-0.20`) — same-filename docs in different directories don't auto-link anymore (previously files with same name in different features scored false 1.0 on identifier overlap, clearing the auto-link gate).
- **4-tier linking** — Structural (confidence 1.0) → Tier 2 inferred (~0.80) → Proposed (review queue) → Explicit (manually linked).
- **External content FTS5** — SQLite's built-in full-text search, no Elasticsearch.
- **vec0 extension** — embedded vector search, no vector DB separate.
- **Store interface** — abstracted. Swap sqlite→libsql (Turso) without changing callers.
- **Recency decay** — `2^(-age / halfLife)` applied at recall time.
- **RRF fusion** — uses Reciprocal Rank Fusion to merge FTS5 + vector + graph scores without distribution assumptions.

## Tools (MCP)

| Group | Tool | What it does | Embedder needed? | LLM needed? |
|-------|------|-------------|-----------------|------------|
| **Store / Delete** | `remember` | Store/update a fact | No | No |
| | `forget` | Delete node by ID | No | No |
| | | | | |
| **Content retrieval** | `recall` | Search content by relevance × recency × importance | No* | No |
| | `recall_chunks` | Paginated chunk retrieval by parent + index | No | No |
| | | | | |
| **Graph** | `link` | Connect two nodes with typed edge | No | No |
| | `query_graph` | Query edges from/to a specific node | No | No |
| | `project_graph` | Return all edges — whole project graph shape | No | No |
| | | | | |
| **Ingestion / Impact** | `ingest` | Walk doc/code tree → searchable memory | No* | No |
| | `onboard` | Full pipeline: detect → ingest → embed → link | No* | No |
| | `onboard_check` | Check graph state for a project (read-only stats) | No | No |
| | `check_impact` | "What's stale because this changed?" | No | No |
| | | | | |
| **LLM extraction** | `extract_and_remember` | Pull facts from raw conversation text | **Yes** | **Yes** |
| | | | | |
| **Async** | `job_status` | Poll async job (onboard, ingest, extract) | No | No |

`*` = degrades gracefully to FTS5-only (lexical). Vector/semantic features skip without embedder. ONNX sidecar is zero-dependency and baked into the binary — run `scripts/build-sidecar.sh` once, no Ollama needed.

For MCP clients, add to config:

**Claude Code:**
```json
{
  "mcpServers": {
    "tastastas": {
      "type": "stdio",
      "command": "/path/to/tastastas",
      "args": [
        "--db", "~/.local/share/tastastas/memory.db",
        "--embed-dim", "768",
        "--embed-backend", "ollama",
        "--graph-addr", ":9292"
      ],
      "env": {}
    }
  }
}
```

**Kilo Code:**
```json
"tastastas": {
  "type": "local",
  "command": [
    "/path/to/tastastas",
    "--db", "~/.local/share/tastastas/memory.db",
    "--embed-dim", "768",
    "--embed-backend", "ollama",
    "--graph-addr", ":9292"
  ]
}
```

DB path is customizable — `~/.local/share/tastastas/memory.db` follows the [XDG Base Directory](https://specifications.freedesktop.org/basedir-spec/latest/) convention: consistent across working directories, survives project cleanup, easy to find.

Then just talk naturally: "ingest my docs from ~/Workspace/project" or "recall what you know about coupon redemption."

Then just talk naturally: "ingest my docs from ~/Workspace/project" or "recall what you know about coupon redemption."

## API endpoints (`--serve` mode)

```
POST /mcp                — MCP protocol (Streamable HTTP)
GET  /graph/{project_id}  — D3 force-directed graph visualization
POST /ingest              — Ingest from path (auto-detect adapters)
GET  /health              — Health check
```

## Ingesting docs

Three adapters. Point one at a folder:

- **`docwalk`** — structured doc trees (PRD/APISpec/ERD) via `.memoryrc.yaml` glob→type mapping. Auto cross-links same-feature docs.
- **`gitrepo`** — walks a tree for `MEMORY.md`-style files.
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
| **Code analysis** | Go AST only (codeast adapter) | 40+ languages via tree-sitter AST |
| **Non-code content** | Docs, PRDs, ERDs, specs, conventions, code, git history, conversation facts, custom notes | Docs, PDFs, images, video/audio, office files |
| **Recency / Staleness** | Built-in: `2^(-age / halfLife)` decay + stale propagation on change | Snapshot at build time. `--update` / `watch` / git hooks for differential re-sync. No query-time decay or propagation |
| **Live updates** | CRUD at any time — `remember`, `link`, `forget`, `ingest` all work while server is running | Rebuild the graph. Auto-rebuild on git commit via hook |
| **Edge types** | 4 tiers: structural (1.0) → inferred (~0.80) → proposed (review) → explicit | `EXTRACTED` / `INFERRED` / `AMBIGUOUS` — each edge tagged with confidence tier |
| **Embeddings** | Yes — vec0 extension, ONNX sidecar or Ollama | None — pure graph, no vector store |
| **Output** | MCP tool responses + live D3 graph page at `GET /graph/{project}` | `graph.html`, `GRAPH_REPORT.md`, `graph.json` (git-committable) |
| **MCP** | Native MCP server (stdio or HTTP) | `python -m graphify.serve graph.json` (separate process) |

**tl;dr** — Graphify is better if you need a one-shot codebase map for your AI assistant, supports many languages, and want the output committed to git. tastastas is better if you need a persistent memory server with hybrid search that grows over time — ingest docs, code, conversations, and facts into one DB, query it with recall × recency × importance, and get change-propagation alerts.

## License

MIT — see `LICENSE`.
