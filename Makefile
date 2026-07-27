# Build & dev
.PHONY: build run install test clean

version := $(shell git describe --tags --dirty=-dirty 2>/dev/null || echo "dev")
ldflags := -X github.com/mystaline-dev/tastastas/internal/mcp.Version=$(version)

build:
	go build -ldflags="$(ldflags)" -o tastastas ./cmd/tastastas

# Default run: embed dim 768, ollama nomic, XDG data dir
run: build
	./tastastas --serve :8080 --db ~/.local/share/tastastas/memory.db --graph-addr :9292

install:
	go install -ldflags="$(ldflags)" ./cmd/tastastas

test:
	go test ./...

clean:
	rm -f tastastas

# Docker
.PHONY: docker-build docker-up docker-down docker-logs

docker-build:
	docker build -t tastastas .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

# Release — tag & push. Usage: make release [MAJOR=n] [MINOR=n] [PATCH=n]
.PHONY: release

MAJOR ?=
MINOR ?=
PATCH ?=

release:
	@latest=$$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0"); \
	ver=$${latest#v}; \
	cur_major=$$(echo "$$ver" | cut -d. -f1); \
	cur_minor=$$(echo "$$ver" | cut -d. -f2); \
	cur_patch=$$(echo "$$ver" | cut -d. -f3); \
	major=$${MAJOR:-$$cur_major}; \
	minor=$${MINOR:-$$cur_minor}; \
	patch=$${PATCH:-$$((cur_patch + 1))}; \
	tag="v$$major.$$minor.$$patch"; \
	git tag "$$tag" && git push origin "$$tag" && echo "pushed $$tag"
