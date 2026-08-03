# Deployment

## Pick your delivery

| # | Method | Requires on server | Best for |
|---|--------|-------------------|----------|
| 1 | `scp` binary (all-in-one) | Nothing | Linux amd64, no Docker |
| 2 | `scp` binary (no sidecar) | Internet / Ollama server | Cross-platform, lighter binary |
| 3 | `docker pull` image | Docker | Any OS, orchestrated |
| 4 | Build directly on server | Go + Node + gcc + (Rust) | Single machine dev+deploy |
| 5 | `docker compose up` on server | Docker + compose | Single machine, no build tools |

---

## Prerequisites

**Build machine only:**
| Tool | Needed for |
|------|-----------|
| Go 1.26+ + gcc | Backend |
| Node.js 22+ | Frontend |
| Rust + cargo | Sidecar ONNX (optional, skip for OpenAI/Ollama) |
| Docker | Container build (optional) |

**Target server: nothing.** No Go, no Node, no Rust, no gcc.

---

## 1. `scp` binary — all-in-one (sidecar + frontend + Go)

```bash
# dev machine
make all
scp tastastas user@host:~/

# target
./tastastas --serve :8080 --graph-addr :9292
```
Zero deps. SPA + ONNX baked in.

## 2. `scp` binary — frontend + Go only (no sidecar)

```bash
# dev machine
make build
scp tastastas user@host:~/

# target — OpenAI
export TASTASTAS_OPENAI_KEY=$KEY
./tastastas --serve :8080 --graph-addr :9292 --embed-backend openai

# target — Ollama
./tastastas --serve :8080 --graph-addr :9292 --embed-backend ollama --ollama-url http://ollama:11434
```

## 3. Docker image — push to registry

```bash
# dev machine
docker build -t tastastas .
docker tag tastastas ghcr.io/mystaline/tastastas:latest
docker push ghcr.io/mystaline/tastastas:latest
```

Stable (main) releases publish `:latest`; pre-release (non-main branch) releases publish `:alpha`. See [RELEASING.md](./RELEASING.md#branch-awareness).

```yaml
# target — docker-compose.yml
services:
  tastastas:
    image: ghcr.io/mystaline/tastastas:latest   # or :alpha for pre-release
    ports:
      - "8080:8080"
      - "9292:9292"
    volumes:
      - tastastas-data:/data
      - ${WORKSPACE_MOUNT:-/home/deploy/workspaces}:/workspaces
    command:
      - "--serve"
      - ":8080"
      - "--graph-addr"
      - ":9292"
      - "--embed-backend"
      - "openai"
      - "--consolidate-interval"
      - "1h"
    environment:
      - TASTASTAS_OPENAI_KEY=${TASTASTAS_OPENAI_KEY:?required}
      - SERVER_WORKSPACE_ROOT=/workspaces

volumes:
  tastastas-data:
```

## 4. Build directly on server

```bash
git clone https://github.com/mystaline/tastastas && cd tastastas
make build   # or make all (with sidecar, needs Rust)
./tastastas --serve :8080 --graph-addr :9292
```

## 5. Docker compose on server

```bash
git clone https://github.com/mystaline/tastastas && cd tastastas
docker compose up -d
```

---

## Quick run cheatsheet

| Goal | Command |
|------|---------|
| stdio MCP (default) | `./tastastas` |
| HTTP + sidecar | `./tastastas --serve :8080 --graph-addr :9292` |
| HTTP + OpenAI | `TASTASTAS_OPENAI_KEY=$KEY ./tastastas --serve :8080 --graph-addr :9292 --embed-backend openai` |
| HTTP + Ollama | `./tastastas --serve :8080 --embed-backend ollama --ollama-url http://ollama:11434` |
| OpenRouter | `TASTASTAS_OPENAI_KEY=$OR_KEY ./tastastas --serve :8080 --graph-addr :9292 --embed-backend openai --openai-base-url https://openrouter.ai/api/v1` |
| With consolidation | `./tastastas --serve :8080 --consolidate-interval 1h` |
| Custom DB path | `./tastastas --db /path/to/db` |
| Auth + HTTP | `./tastastas --serve :8080 --auth-token mytoken` |

---

## Hardware & tuning

### OpenAI / API (cloud)

No tuning needed. `--batch-size` up to 2048 (API limit). Costs vary by model:

| Model | Dim | Cost / M tok | Notes |
|-------|:---:|:-----------:|-------|
| text-embedding-3-small | 1536 | $0.02 | Default. Best quality/$ |
| nvidia/nemotron-3-embed-1b:free | 2048 | **free** | OpenRouter |
| baai/bge-m3 | 1024 | $0.01 | Multilingual |

### Sidecar (local ONNX)

Heavy only during **ingest** (ONNX embedding saturates CPU, RAM spikes). Recall (FTS5 + cosine) is lightweight and unaffected.

| vCPU | RAM | Ingest load | `--sidecar-workers` | `--sidecar-intra-threads` | `--batch-size` | Notes |
|------|-----|-------------|--------------------:|--------------------------:|---------------:|-------|
| 2 | 2GB | Occasional (e.g. cron daily) | 1 | 1 | 8 | **Minimum.** Run ingest alone, avoid concurrent recall. |
| 4 | 4GB | Per push CI / every few hours | 1 | 2 | 16 | Ingest serialized. Recall parallel. |
| 8 | 16GB | Multi-project / hourly | 2 | 4 | 32 | Multiple ingest jobs queue. |
| 16+ | 32GB+ | Full mass re-ingest | 4 | 0 (all) | 32 | Max throughput. Default. |

> **Crash prevention:** on <4 vCPU, always set `--sidecar-workers 1 --sidecar-intra-threads 1 --batch-size 8`. Default (workers=4, threads=all, batch=32) will OOM or saturate CPU on low-spec machines.  
> Ingest and recall run independently. If CPU is fully consumed by ingest, recall may be slow. Schedule ingest during low-usage hours on minimal hardware.

### Ollama (external)

| vCPU | RAM | Ingest load | Model | `--batch-size` | Notes |
|------|-----|-------------|-------|---------------:|-------|
| 4 | 8GB | Light-moderate | `nomic-embed-text` | 16 | Ollama + tastastas ~1.5GB base RAM |
| 8 | 16GB | Moderate-heavy | `nomic-embed-text` | 32 | Standard team setup |
| 16+ | 32GB+ | Heavy | `bge-m3` | 32 | 1024-dim, higher quality |

> Ollama serves its own model in a separate process. On low-RAM machines, choose a smaller model (`nomic-embed-text`) or reduce Ollama's `--num-threads` to avoid swap/OOM. tastastas itself is unaffected — it just sends HTTP requests to Ollama.

---

## Flags

### HTTP server
| Flag | Default | Description |
|------|---------|-------------|
| `--serve` | `""` | HTTP address. Empty = stdio MCP. |
| `--graph-addr` | `""` | Graph port. Separated so unauthenticated access doesn't hit --serve's auth. |
| `--auth-token` | `""` | Bearer token. Empty = no auth. |

### Storage
| Flag | Default | Description |
|------|---------|-------------|
| `--db` | `~/.local/share/tastastas/memory.db` | SQLite path. Also `$TASTASTAS_DB`. |

### Embed — general
| Flag | Default | Description |
|------|---------|-------------|
| `--embed-backend` | `sidecar` | `sidecar`, `openai`, `ollama`, or `none`. |
| `--embed-dim` | `0` | Vector dimension (0 = auto-detect). |
| `--batch-size` | `32` | Max texts per embed batch. |

### Embed — sidecar
| Flag | Default | Description |
|------|---------|-------------|
| `--sidecar-workers` | `0` | Worker count (0 = 4). |
| `--sidecar-intra-threads` | `0` | ONNX intra-op threads (0 = all cores). |

### Embed — OpenAI
| Flag | Default | Description |
|------|---------|-------------|
| `--openai-api-key` | `""` | API key. Also `$TASTASTAS_OPENAI_KEY`. |
| `--openai-model` | `text-embedding-3-small` | Model ID. |
| `--openai-base-url` | `https://api.openai.com/v1` | Base URL. Works with OpenRouter, any OpenAI-compatible provider. |

### Embed — Ollama
| Flag | Default | Description |
|------|---------|-------------|
| `--ollama-url` | `http://localhost:11434` | Server URL. |
| `--ollama-model` | `nomic-embed-text` | Embedding model. |

### Frontend
| Flag | Default | Description |
|------|---------|-------------|
| `--spa-dir` | `""` | Filesystem SPA override. Also `$TASTASTAS_SPA_DIR`. |

### Consolidation
| Flag | Default | Description |
|------|---------|-------------|
| `--consolidate-interval` | `""` | Background cron interval e.g. `1h`, `30m`. Empty = disabled. Detects session co-occurrence → creates `co-accessed` edges. |

### Env variables

Precedence per variable: **explicit flag wins → env var → default**. Flags and env vars are interchangeable for these:

| Variable | Flag | Notes |
|----------|------|-------|
| `TASTASTAS_DB` | `--db` | Env checked first, then XDG default |
| `TASTASTAS_OPENAI_KEY` | `--openai-api-key` | Env used when flag empty |
| `TASTASTAS_SPA_DIR` | `--spa-dir` | Env used when flag empty |
| `TASTASTAS_AUTH_TOKEN` | `--auth-token` | Env used when flag empty |
| `TASTASTAS_EMBED` | `--embed-backend` | Default `sidecar` |
| `TASTASTAS_OPENAI_MODEL` | `--openai-model` | Default `text-embedding-3-small` |
| `TASTASTAS_OPENAI_BASE_URL` | `--openai-base-url` | Default `https://api.openai.com/v1` |
| `TASTASTAS_CONSOLIDATE` | `--consolidate-interval` | Empty = disabled |

Env-only (no flag equivalent — read directly by the app):

| Variable | Notes |
|----------|-------|
| `SERVER_WORKSPACE_ROOT` | Server-visible repository root; Docker default `/workspaces`. Bare binary: unset defaults to `~/tastastas/workspaces` (auto-created at startup); explicitly set but missing on disk is a hard error, not auto-created |

Compose-only interpolation (read by docker-compose, never by the app):

| Variable | Used for |
|----------|----------|
| `WORKSPACE_MOUNT` | Host directory bind-mounted at `/workspaces` |
| `HTTP_PORT` / `GRAPH_PORT` | Published ports |
| `TASTASTAS_IMAGE` | Prod image tag |
| `GOPRIVATE` | Go toolchain private-module list (codeast/gitrepo ingest) |

Rule of thumb: **bare binary** → flags (or the four `TASTASTAS_*` env vars above); **docker-compose** → set variables in `.env`, compose maps them into the `command`; anything else in the compose env block is interpolation only.

---

## Workspace paths

`SERVER_WORKSPACE_ROOT` is the path tastastas can read. It is not a client path. The server never maps or stores client filesystem paths.

### Docker with shared `/workspaces`

```yaml
volumes:
  - ${WORKSPACE_MOUNT:-/home/deploy/workspaces}:/workspaces
environment:
  - SERVER_WORKSPACE_ROOT=/workspaces
```

Send `/workspaces/repo-a` as `cwd`, or pass `repository_url` and let the server resolve it.

### On-prem server

Set root to actual server-visible repository directory:

```bash
SERVER_WORKSPACE_ROOT=/home/user/workspaces
```

If unset, the bare binary defaults to `~/tastastas/workspaces` and creates it at startup (same precedent as `defaultDBPath`/`ensureDBDir`) — `repository_url` and `project_id`-only resolution work out of the box instead of hard-failing. If the env is set explicitly but the path doesn't exist, that's an error (`stat server workspace root ...`), not auto-created — a typo'd or unmounted path must surface loudly.

For client-only paths, include `repository_url` in `onboard` or `ingest`:

```json
{
  "cwd": "/home/mystaline-dev/Workspace/repo-a",
  "repository_url": "https://gitea.example/org/repo-a.git"
}
```

`cwd` is used as-is only when it exists on the server. Remote clients send `repository_url`; the server recursively scans `SERVER_WORKSPACE_ROOT` (no depth limit, so org/group-nested layouts like `/workspaces/Personal/repo-a` are found, not just direct children) for a Git repository whose `origin` remote matches. Matching remote resolves to canonical server path. Duplicate matches return an error. `.git`, `node_modules`, `vendor`, `.venv`/`venv`, `.cache`, `__pycache__`, and hidden directories are never descended into — once a directory's own `.git` is found, its subtree isn't descended into either, which bounds the scan.

If `cwd` and `repository_url` are both empty, the server does **not** fall back to walking `SERVER_WORKSPACE_ROOT` itself (that would silently mix every repo under the mount into one project). It instead requires `project_id` and searches the same recursive index for a repository directory whose *basename* matches — e.g. `project_id: "repo-a"` finds `/workspaces/repo-a` or `/workspaces/Personal/repo-a`, whichever exists. Ambiguous basename matches (same name at two different paths) return an error listing all matches. If none of `cwd`, `repository_url`, or a matching `project_id` basename is found, the call errors instead of guessing.

URL matching normalizes HTTPS, SSH, and scp-style remotes. Repository index refreshes when the discovered repo set or any `.git/config` timestamp changes. Stored `SourcePath` values remain relative.

### Stage and scope gotchas

`onboard`/`ingest` resolve a stage automatically and never error on it: pass `stage` explicitly, or let the server auto-detect the current git branch from `cwd`. If `cwd` isn't a git repo the server can read, git is missing, or the repo has an unborn HEAD (no commits yet), the stage falls back to the literal `"local"` instead of failing — this keeps non-git directories (docs folders, tarball exports, CI checkouts without `.git`) ingestable. The ingested data is stored under an effective ID of `project_id::stage:<stage>` — every read tool (`recall`, `onboard_check`, `clear_project`, `check_recent`, `project_graph`, `abort_ingestion`, `link_projects`) must be called with the **same** `project_id` + `stage` pair, or it will silently return zero results/nodes rather than an error (an empty scope is a valid, distinct state — not a typo signal).

**`local` → real-branch transition.** If a directory was first ingested while non-git (stage `local`) and later becomes a real git repo (or `cwd` starts resolving a branch), new ingests land on the detected branch stage, not `local`. The response (`onboard`/`ingest`/`POST /ingest`) carries a `warning` field naming the still-present `local`-stage data and pointing at `clear_project` with `stage=local` as the remedy. This is never automatic — a docs directory or Obsidian vault can legitimately stay at `local` forever, so ingest never auto-purges it.

A real git branch literally named `local` is indistinguishable from the fallback — acceptable, but worth knowing if debugging an unexpected transition warning.

---

## Target platform

| Delivery | OS / Arch | Runtime deps |
|----------|-----------|-------------|
| Binary with sidecar | Linux amd64 | **None** |
| Binary no sidecar | Any (Go-compiled) | Internet / Ollama |
| Docker image | Any (image is linux/amd64) | Docker |

Sidecar ONNX baked only for `linux/amd64`. Without sidecar (`openai`/`ollama`/`none`), binary runs on any OS/arch Go + CGO SQLite supports.

---

## Frontend override (dev only)

```bash
cd frontend && npm ci && npm run build
./tastastas --spa-dir frontend/dist
# or: TASTASTAS_SPA_DIR=frontend/dist ./tastastas
```
