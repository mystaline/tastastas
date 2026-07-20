# tastastas

**Agentic memory for AI agents.** Typed graph + vector + lexical hybrid
store, single Go binary, SQLite, runs fully offline.

> Store facts, recall them by relevance/recency/importance, and get
> automatic "this might be stale now" alerts when connected docs change.

## Why not just RAG

Flat chunk+cosine retrieval can't tell two mentions of the same fact apart,
doesn't decay, and has no graph — it can't answer "what connects to X" or
"what's stale because Y changed." tastastas keeps typed nodes/edges so
recall = `relevance * recency * importance`, and changes propagate a
"possibly stale" flag to connected docs automatically.

## Install & run

```bash
go build -o tastastas ./cmd/tastastas

# stdio — for MCP clients (Claude Desktop, Claude Code, any MCP agent)
./tastastas --db memory.db --embed-dim 768

# HTTP — shared team instance + webhook ingestion
./tastastas --db memory.db --embed-dim 768 --serve :8080
```

`--embed-dim` must match your embedder (768 = `nomic-embed-text`, 384 =
`bge-small-en-v1.5` / many `sentence-transformers`). Wrong-dim vectors are
rejected at insert.

Everything works with zero external dependencies except
`extract_and_remember` and doc/code chunk search, which need an embedder
(see [Flags](#flags) below — pick `sidecar` for zero-setup, or `ollama` if
you already run it).

## Flags

| Flag | Default | Does |
|---|---|---|
| `--serve` | *(unset)* | Run as HTTP server on this address (e.g. `:8080`). Unset = stdio MCP mode. |
| `--db` | `memory.db` | Path to the SQLite database file. |
| `--embed-dim` | `384` | Vector dimension. Must match whatever embedder you use — 384 for the baked sidecar (`bge-small-en-v1.5`) or many `sentence-transformers`, 768 for `nomic-embed-text`. Wrong-dim vectors are rejected at insert. |
| `--embed-backend` | `ollama` | Which embedder to use: `sidecar` (baked ONNX binary, zero external deps, always 384-dim — run `scripts/build-sidecar.sh` once first), `ollama` (HTTP call to a local Ollama), or `none` (lexical-only, no embedding at all — `extract_and_remember` and semantic recall degrade gracefully). |
| `--ollama-url` | `http://localhost:11434` | Ollama base URL. Only used when `--embed-backend=ollama`. |
| `--ollama-model` | `nomic-embed-text` | Ollama embedding model name. Only used when `--embed-backend=ollama`. Must match `--embed-dim` (768 for `nomic-embed-text`). |

```bash
# Zero external deps — bakes bge-small-en-v1.5 into the binary once
./scripts/build-sidecar.sh
./tastastas --db memory.db --embed-dim 384 --embed-backend sidecar --serve :8080

# Or point at an existing Ollama instance instead
./tastastas --db memory.db --embed-dim 768 --embed-backend ollama --serve :8080

# Or skip embedding entirely — lexical (FTS5) + graph only
./tastastas --db memory.db --embed-backend none --serve :8080
```

## First run (5 minutes, from fresh clone)

### What you actually need

| Thing | Why | Mandatory? |
|---|---|---|
| Go 1.23+ | builds the binary | Yes |
| [Ollama](https://ollama.com) | only for `extract_and_remember` tool; the other 6 tools (remember, recall, forget, link, ingest, check_impact) work without it | **No** — skip if you don't need LLM fact extraction |
| 768-dim embedding model in Ollama (e.g. `nomic-embed-text`) | only for `extract_and_remember`'s dedupe step; `recall` uses FTS5 (lexical), not vectors | **No** — skip same as above |

### Steps with explanations

**1. Build the binary** (mandatory)

```bash
git clone <your-fork-or-this-repo>
cd tastastas
go build -o tastastas ./cmd/tastastas
```

*This compiles everything into a single `tastastas` binary. Nothing else to install.*

**2. Start the server** (mandatory — the whole thing runs as a server)

Pick one mode:

```bash
# HTTP mode (easiest for trying things — you can curl it from another terminal)
./tastastas --db memory.db --embed-dim 768 --serve :8080
# Keep this terminal open. The server logs here.

# OR: stdio mode (for connecting your MCP client — Claude Desktop, Claude Code, etc.)
./tastastas --db memory.db --embed-dim 768
# No port. This talks MCP over stdin/stdout — point your client config at this binary.
```

*Why two modes? HTTP mode lets you hit it with curl to try things. Stdio mode is how an AI agent connects to it — no port, no network, just pipes. You don't need both; pick one for now.*

**3. Check it's alive** (skippable — just confirmation)

```bash
curl -s localhost:8080/health
# → {"status":"ok","version":"0.1.0"}
```

*If you see this, the server is running. (Stdio mode has no health endpoint — it's alive if your MCP client connected.)*

**4. Give it some content** (mandatory if you want something to search)

```bash
curl -s -X POST localhost:8080/ingest/docwalk \
  -d '{"root": "/path/to/your/docs", "project_id": "my-project"}'
# → {"nodes_ingested":118,"edges_created":0}
```

*This walks a folder, reads every markdown file, and stores each one as a searchable node. `project_id` is just a namespace — think "which project does this content belong to." No config file needed; without one, everything becomes a generic doc (still fully searchable).*

*If your docs are structured (PRD/API spec/ERD folders with a naming convention), add a `.memoryrc.yaml` config to get typed nodes + automatic cross-linking. See [Ingesting docs](#ingesting-docs) below.*

**5. Search it** (the payoff)

```bash
# First, initialize an MCP session — this is how the MCP protocol works.
# You get back a session ID you reuse for all follow-up calls.
curl -s -i -X POST localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1.0"}}}'
# → look for "Mcp-Session-Id: <some-long-string>" in the response headers
#   copy that string

SESSION="<paste-it-here>"

# Now ask a question
curl -s -X POST localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"project_id":"my-project","query":"your search terms"}}}'
# → returns scored results (title, content, node_type, score)
```

*Why the session dance? This is MCP (Model Context Protocol) — the standard way AI agents talk to tools. Every call after `initialize` reuses the same session ID. If you connect tastastas to an MCP client (next section), the client handles all of this for you — you just talk to your agent normally.*

### Skip the HTTP dance entirely — connect an MCP client

If you have Claude Desktop or Claude Code, add this to your MCP config:

```json
{
  "mcpServers": {
    "tastastas": {
      "command": "/full/path/to/tastastas",
      "args": ["--db", "memory.db", "--embed-dim", "768"]
    }
  }
}
```

Restart your client. Now just talk to your agent normally:

> "remember I prefer dark mode"

> "recall what you know about coupon redemption"

> "ingest my project docs from ~/Workspace/my-project"

No curl, no session IDs, no JSON-RPC — the client handles it.

## The 7 tools

| Tool | Does | Needs Ollama? |
|---|---|---|
| `remember` | Store/update a fact directly | No |
| `recall` | Search by relevance × recency × importance | No |
| `forget` | Delete by ID | No |
| `link` | Connect two nodes with a typed edge | No |
| `ingest` | Pull in a doc tree (docwalk / gitrepo / obsidian) | No |
| `check_impact` | "What's now stale because this changed?" | No |
| `extract_and_remember` | Pull facts out of raw conversation text | **Yes** |

Ask your MCP-connected agent things like "remember I prefer dark mode" or
"recall what you know about coupon redemption" — it calls the tools for you.
Or hit them directly:

```bash
curl -X POST localhost:8080/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"recall","arguments":{"project_id":"my-project","query":"coupon redemption"}}}'
```

## Ingesting docs

Three adapters — point one at a folder and it becomes searchable memory:

- **`docwalk`** — structured doc trees (PRD/APISpec/ERD/...) via a
  `.memoryrc.yaml` glob→type config. Auto cross-links same-feature docs.
- **`gitrepo`** — walks a tree for `MEMORY.md`-style files.
- **`obsidian`** — vault frontmatter + `[[wikilinks]]` become typed nodes/edges.

```bash
curl -X POST localhost:8080/ingest/docwalk \
  -d '{"root": "/path/to/docs", "config_path": ".memoryrc.yaml", "project_id": "my-project"}'
```

`.memoryrc.yaml` example:

```yaml
project_id: my-project
mappings:
  - path_glob: "*/PRD/**/*.md"
    type: prd
    group_by: "^[^/]+/PRD/(?P<feature>[^/]+)/"   # cross-links same-feature docs
  - path_glob: "*/APISpec/*.md"
    type: api-spec
    group_by: "^[^/]+/APISpec/(?P<feature>[^.]+)\\.md$"
```

## HTTP routes (`--serve` mode)

```
POST /mcp                — MCP protocol (Streamable HTTP)
POST /ingest/{adapter}    — {"root", "config_path", "project_id"}
POST /ingest/webhook      — {"path", "content", "project_id", ...}
GET  /health              — {"status": "ok", "version": "0.1.0"}
```

## Status

v0.1.0 — functional. See `DEVELOPMENT.md` for architecture, dev/verify
commands, and roadmap. `tasks.md` tracks what's open past this release.

## License

MIT — see `LICENSE`.
