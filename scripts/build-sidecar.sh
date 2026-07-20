#!/usr/bin/env bash
# scripts/build-sidecar.sh — downloads bge-small-en-v1.5 (ONNX) + tokenizer,
# builds the tastastas-embed Rust sidecar for the current platform, and
# copies the binary into internal/embed/bin/<os>_<arch>/ where go:embed
# picks it up at build time.
#
# Usage: ./scripts/build-sidecar.sh
# Requires: cargo/rustc, curl, ~1GB free disk, network access to huggingface.co
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="$ROOT/sidecar/assets"
OS="$(go env GOOS)"
ARCH="$(go env GOARCH)"
OUT_DIR="$ROOT/internal/embed/bin/${OS}_${ARCH}"
BIN_NAME="tastastas-embed"
[ "$OS" = "windows" ] && BIN_NAME="${BIN_NAME}.exe"

mkdir -p "$ASSETS"

if [ ! -f "$ASSETS/model.onnx" ]; then
    echo "Downloading bge-small-en-v1.5 ONNX model (~130MB)..."
    curl -sL -o "$ASSETS/model.onnx" \
        "https://huggingface.co/BAAI/bge-small-en-v1.5/resolve/main/onnx/model.onnx"
fi

if [ ! -f "$ASSETS/tokenizer.json" ]; then
    echo "Downloading tokenizer..."
    curl -sL -o "$ASSETS/tokenizer.json" \
        "https://huggingface.co/BAAI/bge-small-en-v1.5/resolve/main/tokenizer.json"
fi

echo "Building sidecar (release, this takes a minute)..."
(cd "$ROOT/sidecar" && cargo build --release)

mkdir -p "$OUT_DIR"
cp "$ROOT/sidecar/target/release/$BIN_NAME" "$OUT_DIR/$BIN_NAME"

echo "Sidecar built: $OUT_DIR/$BIN_NAME"
echo "Run 'go build ./...' to embed it into the tastastas binary."
