.PHONY: build install test clean docker-build docker-up docker-down docker-logs

build:
	go build -o tastastas ./cmd/tastastas

install:
	go install ./cmd/tastastas

test:
	go test ./...

clean:
	rm -f tastastas

docker-build:
	docker build -t tastastas .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f
