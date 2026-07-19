# tastastas

Agentic memory engine: a typed graph+vector+lexical hybrid store for AI agents,
with automatic conversation-derived memory, pluggable document ingestion (git
repos, Obsidian vaults, structured doc trees like PRD/API-spec/ERD/test-case
sets), and cross-doc/cross-service **change-impact detection** — when a doc or
service changes, tastastas tells you what else might now be stale.

Single Go binary. SQLite by default. Runs fully offline. No accounts, no
tenancy, no server required unless you want one.

## Why not just RAG

Flat chunk+cosine retrieval has no entity identity (same fact re-embedded and
duplicated), no decay, and no graph structure — it can't answer "what connects
to X" or "what's now stale because Y changed." tastastas keeps typed nodes and
typed edges (`implements`, `tests`, `specifies`, `depends-on`, ...) so recall
is relevance × recency × importance, not just top-k similarity, and doc/graph
changes propagate a "possibly stale" signal to connected nodes automatically.

## Use cases

1. **Bravo, offline** — local agent memory for a single developer, no network
   required (SQLite + Ollama).
2. **Team, shared** — same binary in `--serve` mode, ingest webhook lets an
   internal doc-center/Jira app push doc changes in; agents recall
   cross-service context via MCP.
3. **Self-hosted OSS** — any team runs their own instance. No cross-install
   auth, no multi-tenant SaaS layer — each install is one trust boundary.

## Status

Early scaffold — see `.hermes/plans/` (or your own planning docs) for the
implementation roadmap. Not yet functional.

## License

MIT — see `LICENSE`.
