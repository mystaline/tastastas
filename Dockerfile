FROM node:22-alpine AS frontend
WORKDIR /build
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /build/dist internal/mcp/frontenddist
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o tastastas ./cmd/tastastas

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && \
    adduser -D -h /app appuser
WORKDIR /app
COPY --from=builder /build/tastastas .
RUN mkdir /data && chown appuser:appuser /data
VOLUME /data
USER appuser
EXPOSE 8080 9292
ENTRYPOINT ["./tastastas"]
CMD ["--serve", ":8080", "--db", "/data/memory.db", "--graph-addr", ":9292"]
