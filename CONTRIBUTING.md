# Contributing to tastastas

## What this project is

**tastastas = agentic memory server.** One binary (Go + SQLite). AI agents (Claude Code, Kilo, any MCP client) talk to it over MCP and ask it to:

- **ingest** a folder (code, docs, PRDs, git history) → typed knowledge graph
- **remember** facts from conversation
- **recall** stuff later — by relevance × recency × importance, hybrid search (text + vectors + graph)
- **link** things and see "what's stale because X changed"

No external services. No Elasticsearch. No vector DB. One binary, one SQLite file.

Read [README.md](./README.md) first. Then [docs/architecture.md](./docs/architecture.md) for the pipeline. Both shorter than they look.

## Repo map (read this, it's 90% of orientation)

| Path | What lives there |
|---|---|
| `cmd/tastastas/` | Entry point, CLI flags |
| `internal/mcp/` | MCP server + all tools (`ingest`, `onboard`, `recall`, `link`, …) |
| `internal/onboard/` | The ingest pipeline orchestrator (adapters → chunk → embed → link → hierarchy → conventions) |
| `internal/ingest/` | Adapters: `codeast` (code), `docwalk` (structured docs), `markdownglob`, `gitrepo`, `obsidian` |
| `internal/ingest/codeast/treesitter/` | Tree-sitter extractors (TS/JS, Python, Rust, Go) |
| `internal/store/` | SQLite store (abstracted via `store.Store` interface) |
| `internal/chunker/` | Chunking (incl. code-aware chunking via tree-sitter) |
| `internal/embed/` | Embedding backends: sidecar (ONNX), OpenAI, Ollama |
| `frontend/` | D3 graph visualization (served at `/graph/{project}`) |
| `sidecar/` | Rust ONNX embedder (`tastastas-embed`, baked into binary) |
| `docs/` | Architecture, demo walkthrough, MCP client config |

## Commands (dev machine)

```bash
go build ./...       # compile
go test ./...        # run all tests
go vet ./...         # static analysis
gofmt -l .           # formatting check (must be clean on your files)

# full local run (frontend + sidecar + binary)
make build && make run

# no sidecar / no embeddings needed? just the server:
go run ./cmd/tastastas --serve :8080 --db ~/.local/share/tastastas/memory.db
```

Minimum for a PR: your code `go test ./...` passes and `gofmt -l` is clean on the files you touched.

**Flags vs env:** most flags are also settable via env (`TASTASTAS_DB`, `TASTASTAS_OPENAI_KEY`, `TASTASTAS_SPA_DIR`, `TASTASTAS_AUTH_TOKEN`); explicit flag wins. `SERVER_WORKSPACE_ROOT` is env-only. See [DEPLOYMENT.md#env-variables](./DEPLOYMENT.md#env-variables).

## House rules

- **Conventional commits.** `feat(scope): ...`, `fix(scope): ...`, `chore: ...`. Short subject, imperative ("add", "fix"), no "added". Bullets with `- label:` when a body is needed.
- **Branch names** already use `feat/…` or `fix/…` prefixes. Keep that pattern.
- **No new dependencies for what a few lines can do.** Stdlib first. If a dep is genuinely needed, say why in the PR.
- **Tests go with the change.** Non-trivial logic ships with a test (table-driven is the house style, see `internal/onboard/*_test.go`).
- **Determinism matters.** Never rely on Go map iteration order in anything that produces node IDs or edges — sort keys first (`internal/onboard/orchestrator.go` has a `sortedKeys` helper to copy).
- **Don't break the Docker story.** See below — the container is `read_only` + tmpfs, code ingestion needs Go, and repo paths resolve via `SERVER_WORKSPACE_ROOT`/`repository_url`. Read the section before touching Docker/build files.

## Common tasks → where to look

| You want to… | Go to |
|---|---|
| Add a new MCP tool | `internal/mcp/server.go` (tools registered in `registerTools`) |
| Add a new ingest source (Notion, Confluence, …) | new adapter under `internal/ingest/`, wire into `internal/onboard/orchestrator.go` |
| Support a new code language | new extractor in `internal/ingest/codeast/treesitter/` (copy `rs.go` or `go.go` — they're small) |
| Change graph linking | `internal/onboard/linker.go` |
| Change chunking | `internal/chunker/` |
| Touch storage | `internal/store/` (keep both sqlite + libsql in sync) |

## Docker: the 3 things that will bite you

1. **Workspace paths** — `SERVER_WORKSPACE_ROOT` is the directory visible to tastastas. Docker defaults to `/workspaces`. The server never maps client host paths (multi-user, different local dirs); remote clients pass `repository_url` so the server resolves the canonical repo under `SERVER_WORKSPACE_ROOT`. Details: [DEPLOYMENT.md#workspace-paths](./DEPLOYMENT.md#workspace-paths).
2. **`read_only: true` + `tmpfs: /tmp`** — only `/tmp` is writable. The Go module cache is baked into the image and copied to `/tmp` at start by `docker-entrypoint.sh`. Don't "fix" this by making the FS writable or removing tmpfs — it's a deliberate security posture (see discussion in commit history).
3. **Go ingestion needs `go` + modules.** `packages.Load` (type-checked calls, import edges) needs the Go toolchain (baked in) and module resolution. Private modules (e.g. `gitea.tagsamurai.local`) need `GOPRIVATE` + git credentials; without them it falls back to tree-sitter (name-matched, fewer edges, still useful). Both paths are intentional.

## Releases

See [RELEASING.md](./RELEASING.md). Short version:

- **Don't run `make release`.** Releases are triggered from CI via `gh workflow run tag-release.yml`.
- `main` → stable tag `vX.Y.Z` + `:latest` image.
- Any other branch → `-alpha` tag + `:alpha` image.

## First PR ideas (small, high value)

- Add a tree-sitter language extractor (Java, C#, PHP — pick one, ~100 lines)
- Improve cross-file call resolution in `internal/onboard/linker.go` (Go `Receiver` field is captured but unused — good starting point)
- Fix the stats discrepancy: `onboard_check` edge count vs `clear_project` delete count differ by a few edges
- Add an ingest adapter for a doc source you actually use

## The short version

Fork it. `go test ./...`. Make a small change. Conventional commit. PR with a one-paragraph "what and why". That's it — the codebase is ~8 packages, most of the interesting logic is in two files (`internal/onboard/orchestrator.go`, `internal/mcp/server.go`).
