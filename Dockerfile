FROM node:22-alpine@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS frontend
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

FROM alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
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
