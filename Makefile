# Build & dev
.PHONY: all sidecar tastastas build run install test clean frontend-build

version := $(shell git describe --tags --dirty=-dirty 2>/dev/null || echo "dev")
ldflags := -X github.com/mystaline-dev/tastastas/internal/mcp.Version=$(version)

SIDECAR_SRC := sidecar/src/main.rs sidecar/Cargo.toml
SIDECAR_BIN := sidecar/target/release/tastastas-embed
EMBED_BIN  := internal/embed/bin/linux_amd64/tastastas-embed

all: sidecar tastastas

frontend-build:
	cd frontend && npm ci && npm run build

sidecar: $(EMBED_BIN)

$(EMBED_BIN): $(SIDECAR_SRC)
	cargo build --release --manifest-path sidecar/Cargo.toml
	cp $(SIDECAR_BIN) $@

tastastas: $(EMBED_BIN)
	cp -r frontend/dist/. internal/mcp/frontenddist/ 2>/dev/null; \
	go build -ldflags="$(ldflags)" -o tastastas ./cmd/tastastas

build: frontend-build tastastas

run: build
	./tastastas --serve :8080 --db ~/.local/share/tastastas/memory.db --graph-addr :9292

install: frontend-build
	cp -r frontend/dist/. internal/mcp/frontenddist/
	go install -ldflags="$(ldflags)" ./cmd/tastastas

test:
	go test ./...

clean:
	rm -f tastastas
	cargo clean --manifest-path sidecar/Cargo.toml

# Docker
.PHONY: docker-build docker-up docker-down docker-logs release

docker-build:
	docker build -t tastastas .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

.PHONY: release

release:
	@echo "Use CI: gh workflow run tag-release.yml --field bump=patch"
	@echo "  or:   gh workflow run tag-release.yml --field version=v1.2.3"
