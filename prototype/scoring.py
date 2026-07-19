#!/usr/bin/env python3
"""Prototype: extraction prompt calibration + dedup threshold derivation.

Run against real conversation transcripts (from session_search exports or
manual paste). DO NOT fabricate fake transcripts and call them "real".

Usage:
    python prototype/scoring.py --snippets snippets.jsonl --ollama-url http://localhost:11434 --model llama3.2

snippets.jsonl format — one JSON object per line:
    {"text": "raw conversation text (last N turns)..."}

Requirements (outside venv for now — pip install in prototype/.venv):
    pip install requests sentence-transformers numpy matplotlib
"""

import argparse
import json
import math
import sys
import time
from itertools import combinations
from pathlib import Path

try:
    import requests
except ImportError:
    sys.exit("Missing 'requests'. Install: pip install requests")


# ---------------------------------------------------------------------------
# Extraction prompt (mirrored from internal/extract/extract.go)
# ---------------------------------------------------------------------------

SYSTEM_PROMPT = """You extract discrete, atomic facts from a conversation snippet for long-term memory storage. Output ONLY a JSON array, no prose. Each item:
{
  "kind": "fact" | "entity",
  "title": "short label, <= 8 words",
  "content": "one self-contained sentence, no pronouns referring outside itself",
  "importance": 0.0-1.0  (0.9+ = identity/durable preference/hard constraint,
                          0.5 = useful context, 0.2 = trivial/likely stale soon)
}
Skip anything that is: a question, a task-in-progress, small talk, or already-obvious-from-context filler. Prefer 0-3 high quality facts over many weak ones."""


# ---------------------------------------------------------------------------
# Ollama extraction
# ---------------------------------------------------------------------------

def extract_facts(snippet: str, ollama_url: str, model: str) -> list[dict]:
    """Call Ollama chat API to extract facts from a conversation snippet."""
    resp = requests.post(
        f"{ollama_url}/api/chat",
        json={
            "model": model,
            "messages": [
                {"role": "system", "content": SYSTEM_PROMPT},
                {"role": "user", "content": snippet},
            ],
            "stream": False,
        },
        timeout=120,
    )
    resp.raise_for_status()
    raw = resp.json()["message"]["content"]

    # Parse JSON array from LLM output (strip markdown fences if present)
    raw = raw.strip()
    if raw.startswith("```"):
        lines = raw.split("\n")
        start = end = 0
        for i, l in enumerate(lines):
            if l.strip().startswith("```"):
                if start == 0:
                    start = i + 1
                else:
                    end = i
        if start:
            raw = "\n".join(lines[start:end])

    start = raw.find("[")
    end = raw.rfind("]")
    if start < 0 or end < 0:
        return []
    return json.loads(raw[start : end + 1])


# ---------------------------------------------------------------------------
# Embedding (local sentence-transformers)
# ---------------------------------------------------------------------------

_model_cache = None

def get_embedder(model_name: str = "all-MiniLM-L6-v2"):
    global _model_cache
    if _model_cache is None:
        try:
            from sentence_transformers import SentenceTransformer
        except ImportError:
            sys.exit("Missing 'sentence-transformers'. Install: pip install sentence-transformers")
        _model_cache = SentenceTransformer(model_name)
    return _model_cache


def embed_texts(texts: list[str], model_name: str = "all-MiniLM-L6-v2") -> list[list[float]]:
    embedder = get_embedder(model_name)
    vecs = embedder.encode(texts, normalize_embeddings=True)
    return [v.tolist() for v in vecs]


# ---------------------------------------------------------------------------
# Cosine similarity (standalone, no numpy dependency for small-scale runs)
# ---------------------------------------------------------------------------

def cosine(a: list[float], b: list[float]) -> float:
    if len(a) != len(b) or not a:
        return 0.0
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if na == 0 or nb == 0:
        return 0.0
    return dot / (na * nb)


# ---------------------------------------------------------------------------
# Threshold calibration
# ---------------------------------------------------------------------------

def calibrate(facts_by_snippet: list[list[dict]], model_name: str = "all-MiniLM-L6-v2"):
    """Compute intra-snippet and inter-snippet similarities to derive threshold."""
    # Flatten all fact contents
    all_contents = []
    snippet_bounds = []  # (start, end) index ranges per snippet
    for snippet_facts in facts_by_snippet:
        start = len(all_contents)
        for f in snippet_facts:
            all_contents.append(f["content"])
        snippet_bounds.append((start, len(all_contents)))

    if len(all_contents) < 2:
        print("Not enough facts to calibrate (need at least 2).")
        return

    print(f"Embedding {len(all_contents)} facts...")
    embeddings = embed_texts(all_contents, model_name)

    # Intra-snippet: facts from SAME snippet (should be distinct — low similarity)
    intra_sims = []
    for start, end in snippet_bounds:
        for i in range(start, end):
            for j in range(i + 1, end):
                s = cosine(embeddings[i], embeddings[j])
                intra_sims.append(s)

    # Inter-snippet: facts from DIFFERENT snippets (may be duplicates — high similarity)
    inter_sims = []
    for (s1_start, s1_end), (s2_start, s2_end) in combinations(snippet_bounds, 2):
        for i in range(s1_start, s1_end):
            for j in range(s2_start, s2_end):
                s = cosine(embeddings[i], embeddings[j])
                inter_sims.append(s)

    print(f"\nIntra-snippet pairs (should be LOW sim, different facts): {len(intra_sims)}")
    if intra_sims:
        avg_intra = sum(intra_sims) / len(intra_sims)
        print(f"  mean: {avg_intra:.4f}, min: {min(intra_sims):.4f}, max: {max(intra_sims):.4f}")

    print(f"Inter-snippet pairs (may be HIGH sim, potential dupes): {len(inter_sims)}")
    if inter_sims:
        avg_inter = sum(inter_sims) / len(inter_sims)
        print(f"  mean: {avg_inter:.4f}, min: {min(inter_sims):.4f}, max: {max(inter_sims):.4f}")

    # Histogram buckets
    print("\nSimilarity distribution:")
    buckets = [0.0] * 10
    for s in intra_sims + inter_sims:
        idx = min(int(s * 10), 9)
        buckets[idx] += 1
    for i, count in enumerate(buckets):
        lo = i * 0.1
        hi = lo + 0.1
        bar = "#" * int(count)
        print(f"  [{lo:.1f}-{hi:.1f}): {count:4d} {bar}")

    # Suggest threshold at 90th percentile of intra-snippet similarity
    if intra_sims:
        sorted_intra = sorted(intra_sims)
        p90_idx = int(len(sorted_intra) * 0.9)
        suggested = sorted_intra[p90_idx]
        # Add small margin
        threshold = round(suggested + 0.02, 3)
        print(f"\nSuggested dedup threshold: {threshold}")
        print(f"  (90th percentile of intra-snippet similarity: {suggested:.4f} + 0.02 margin)")
        print(f"  This means: facts with cosine >= {threshold} are treated as duplicates.")
    else:
        print("\nNot enough intra-snippet pairs to suggest threshold.")

    # Dump all pairwise similarities for manual inspection
    out_path = Path("prototype/calibration_output.jsonl")
    with open(out_path, "w") as f:
        for i, ci in enumerate(all_contents):
            for j, cj in enumerate(all_contents):
                if i >= j:
                    continue
                same_snippet = any(
                    s <= i < e and s <= j < e for s, e in snippet_bounds
                )
                f.write(json.dumps({
                    "i": i, "j": j,
                    "content_i": ci, "content_j": cj,
                    "similarity": round(cosine(embeddings[i], embeddings[j]), 4),
                    "same_snippet": same_snippet,
                }) + "\n")
    print(f"\nPairwise similarities written to {out_path}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Extraction + dedup calibration")
    parser.add_argument("--snippets", required=True, help="Path to JSONL file with conversation snippets")
    parser.add_argument("--ollama-url", default="http://localhost:11434", help="Ollama API URL")
    parser.add_argument("--model", default="llama3.2", help="Ollama model name")
    parser.add_argument("--embed-model", default="all-MiniLM-L6-v2", help="Sentence-transformers model")
    parser.add_argument("--skip-extraction", action="store_true", help="Skip Ollama extraction, use cached facts")
    parser.add_argument("--facts-cache", default="prototype/extracted_facts.jsonl", help="Cache file for extracted facts")
    args = parser.parse_args()

    snippets_path = Path(args.snippets)
    if not snippets_path.exists():
        sys.exit(f"Snippets file not found: {snippets_path}")

    snippets = [json.loads(line)["text"] for line in snippets_path.read_text().splitlines() if line.strip()]
    print(f"Loaded {len(snippets)} snippets")

    # Extract facts (or load from cache)
    cache_path = Path(args.facts_cache)
    if args.skip_extraction and cache_path.exists():
        print(f"Loading cached facts from {cache_path}")
        facts_by_snippet = []
        for line in cache_path.read_text().splitlines():
            if line.strip():
                facts_by_snippet.append(json.loads(line))
    else:
        print(f"Extracting facts via Ollama ({args.model})...")
        facts_by_snippet = []
        for i, snippet in enumerate(snippets):
            print(f"  [{i+1}/{len(snippets)}] extracting...")
            try:
                facts = extract_facts(snippet, args.ollama_url, args.model)
                facts_by_snippet.append(facts)
                print(f"    -> {len(facts)} facts")
            except Exception as e:
                print(f"    -> ERROR: {e}")
                facts_by_snippet.append([])
            # Brief pause to avoid hammering Ollama
            if i < len(snippets) - 1:
                time.sleep(0.5)

        # Cache extracted facts
        cache_path.parent.mkdir(parents=True, exist_ok=True)
        with open(cache_path, "w") as f:
            for facts in facts_by_snippet:
                f.write(json.dumps(facts) + "\n")
        print(f"Cached extracted facts to {cache_path}")

    # Filter empty
    non_empty = [f for f in facts_by_snippet if f]
    if not non_empty:
        print("No facts extracted from any snippet. Check Ollama connection and model.")
        sys.exit(1)

    print(f"\n{sum(len(f) for f in non_empty)} total facts from {len(non_empty)} snippets")

    # Calibrate
    calibrate(non_empty, args.embed_model)


if __name__ == "__main__":
    main()
