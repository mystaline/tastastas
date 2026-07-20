# Unified Embeddings Plan — Conversations + Docs + Code in One Vector Space

**Goal**: `recall(query)` searches conversations, docs, and code chunks in a single semantic space, while keeping typed nodes + graph + change-impact intact.

---

## Phase A — Chunking + Embedding at Ingest (Week 1)

### A1. Chunker package (`internal/chunker/`)
| Task | Spec | Acceptance |
|---|---|---|
| Heading-based markdown chunker | Parse by ATX headings (`#` `##` `###`), each chunk = heading + content until next same/higher level. Max 1200 chars, overlap 150. Preserve heading path in chunk metadata (`heading_path: ["Auth", "JWT Validation"]`). | `chunker.ChunkMarkdown("# A\n...\n## B\n...") → []Chunk{heading_path:["A"], ...}, {heading_path:["A","B"], ...}` |
| Tree-sitter code chunker | Reuse rag-processor's approach: chunk by function/method/class declaration. Language detected by file extension. Each chunk = signature + body. Max 1500 chars. | `chunker.ChunkGo("func Foo() {...}\nfunc Bar() {...}") → []Chunk{name:"Foo", ...}, {name:"Bar", ...}` |
| Chunk type enum | `ChunkType { ConversationFact, MarkdownSection, CodeFunction, CodeMethod, CodeStruct, ObsidianSection }` | Exhaustive, used for filter at query time |

### A2. Modify ingest adapters
| Adapter | Changes |
|---|---|
| `docwalk` | After creating parent `Node`, run chunker on each matched file. For each chunk: embed → store as `ChunkRow{ParentNodeID, ChunkIndex, HeadingPath, Content, Embedding}`. Parent node gets `HasChunks=true`. |
| `gitrepo` | Same — chunk each `MEMORY.md` (or configured glob) by headings. |
| `obsidian` | Chunk by frontmatter + headings. Wikilinks preserved in chunk metadata. |

### A3. Storage schema
```sql
-- New virtual table (sqlite-vec)
CREATE VIRTUAL TABLE chunk_vectors USING vec0(
  chunk_id TEXT PRIMARY KEY,
  parent_node_id TEXT,
  chunk_index INT,
  chunk_type TEXT,
  heading_path TEXT,     -- JSON array ["Auth","JWT"]
  content TEXT,
  embedding float[384]   -- or 768, matches --embed-dim
);

-- Parent nodes table gets:
ALTER TABLE nodes ADD COLUMN has_chunks BOOLEAN DEFAULT 0;
```

### A4. Embedding pipeline at ingest
- Reuse existing `embed.Embedder` (Ollama or baked sidecar)
- Batch chunks in groups of 32 (configurable) to amortize HTTP overhead
- Progress callback: `onProgress("embed", current, total, "embedding chunks")`
- Fail-fast on embed error; partial success returns count + error

---

## Phase B — Unified Query Layer (Week 1-2)

### B1. Extended recall
```go
func (r *Retriever) Recall(ctx, projectID, query string, limit int, opts RecallOpts) ([]UnifiedResult, error)
type RecallOpts struct {
  IncludeChunks    bool     // default true
  ChunkTypes       []ChunkType // filter
  LexicalWeight    float64   // default 0.4
  SemanticWeight   float64   // default 0.6
  RecencyWeight    float64   // default 0.3 (multiplier)
  ImportanceWeight float64   // default 0.5 (multiplier)
}
```

### B2. Score fusion
```
final_score = 
  LexicalWeight * BM25_normalized +
  SemanticWeight * cosine_similarity +
  RecencyWeight * recency_decay(age) * ImportanceWeight * node_importance
```
- Lexical: FTS5 BM25 from `nodes_fts` (existing)
- Semantic: cosine from `chunk_vectors` + `node_vectors` (unified)
- Both normalized to [0,1] per query

### B3. Result shape
```go
type UnifiedResult struct {
  Node        store.Node    // parent node (for graph pull-in)
  Chunk       *ChunkRow     // nil for pure node hits
  Score       float64
  MatchType   string        // "lexical" | "semantic" | "hybrid"
}
```

### B4. Graph pull-in unchanged
- Uses `UnifiedResult.Node.ID` as seed for `NeighborsWithConfidence`
- Edge confidence boost still applies

---

## Phase C — Cross-Source Semantic Links (Week 2)

### C1. Implicit links via vector proximity
- No new edge type needed
- At query time: if a conversation fact chunk and a code chunk land in top-5 with cosine > 0.85, surface as "Related:" in result metadata
- UI/API hint: `{"implicit_link": true, "related_chunk_id": "...", "cosine": 0.91}`

### C2. Explicit cross-source edges (optional, later)
- `extract_and_remember` could auto-create `mentions` edges to nearest code/doc chunks (cosine > 0.9)
- Controlled by flag `--auto-link-threshold=0.9`

---

## Migration & Compatibility

| Concern | Handling |
|---|---|
| Existing DBs without chunks | `sqlite.Open` detects missing `chunk_vectors` table → creates it. `has_chunks=0` for old nodes. |
| `--embed-dim` change | Reject at startup if `node_vectors` dim ≠ flag. User must rebuild or migrate. |
| Zero-embedding mode (`dim=0`) | Chunking still runs, `chunk_vectors` table created but empty. `SemanticWeight` auto-set to 0. |
| Ingest idempotency | `docwalk` uses content hash per chunk → re-ingest updates changed chunks only. |

---

## CLI / Config Additions

```bash
# New flags
--embed-ingested              # enable chunking+embedding at ingest (default: false)
--chunk-size 1000             # max chars per chunk
--chunk-overlap 150           # overlap between chunks
--semantic-weight 0.6         # recall fusion weight
--lexical-weight 0.4
```

---

## Acceptance Criteria (Definition of Done)

1. **Ingest**: `curl /ingest/docwalk` on a 50-file repo → `nodes_ingested: N, chunks_created: M, chunks_embedded: M` in < 30s
2. **Recall**: `recall("JWT validation")` returns code chunks (`ParseBearerToken`, `VerifySignature`) + conversation facts + doc sections in single ranked list
3. **Graph**: `check_impact` on a parent node still marks downstream nodes stale — chunk layer invisible to impact engine
4. **Zero-embedding mode**: `--embed-dim 0` → ingest works, recall uses only lexical + graph, no embedder called
5. **Performance**: 10k chunks → recall p95 < 200ms (FTS5 + vec0 both indexed)
6. **Tests**: 
   - `TestDocwalkChunksAndEmbeds`
   - `TestRecallHybridScoring`
   - `TestCheckImpactUnaffectedByChunks`

---

## Effort Estimate

| Phase | Days | Risk |
|---|---|---|
| A1-A4 Chunking + Ingest | 3 | Low (reusing rag-processor patterns) |
| B1-B4 Query Fusion | 2 | Medium (score tuning) |
| C1-C2 Cross-source | 1 | Low (metadata only) |
| Tests + Polish | 2 | - |
| **Total** | **~8 days** | |

---

## Dependencies

- No new external deps (tree-sitter via CGO already in rag-processor; we'll use heading-regex for MD to avoid CGO)
- Baked ONNX sidecar (from earlier decision) provides local embedder
- Ollama fallback unchanged

---

## Out of Scope (Defer)

- Auto-linking conversation facts → code chunks (`mentions` edges)
- Cross-project vector search (different `project_id` namespaces)
- Incremental re-embedding on partial doc change (full re-ingest for v1)
- Web UI for chunk visualization