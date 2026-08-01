FROM node:25-alpine@sha256:bdf2cca6fe3dabd014ea60163eca3f0f7015fbd5c7ee1b0e9ccb4ced6eb02ef4 AS frontend
WORKDIR /build
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /build/dist internal/mcp/frontenddist
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o tastastas ./cmd/tastastas

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates git && \
    adduser -D -h /app appuser
WORKDIR /app
COPY --from=builder /build/tastastas .
# Go toolchain copied from builder — needed at runtime for go/packages.Load
# during code ingestion (primary Go path; tree-sitter fallback covers failures).
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH \
    GOCACHE=/tmp/go-build \
    GOPATH=/tmp/go \
    GOMODCACHE=/tmp/go/pkg/mod \
    GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}
RUN mkdir /data /workspaces && chown appuser:appuser /data /workspaces
# Bake the builder's Go module cache so runtime ingest (go/packages.Load)
# doesn't re-download every container start. /tmp is tmpfs in prod, so the
# entrypoint copies it into GOMODCACHE at start.
COPY --from=builder /go/pkg/mod /go-mod-cache
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh && chown appuser:appuser /app/docker-entrypoint.sh
VOLUME /data /workspaces
USER appuser
EXPOSE 8080 9292
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["--serve", ":8080", "--db", "/data/memory.db", "--graph-addr", ":9292"]
