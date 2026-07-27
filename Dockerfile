FROM golang:1.26-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN mkdir -p internal/embed/bin/linux_amd64

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
CMD ["--serve", ":8080", "--db", "/data/memory.db"]
