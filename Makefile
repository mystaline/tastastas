.PHONY: build install test clean

build:
	go build -o tastastas ./cmd/tastastas

install:
	go install ./cmd/tastastas

test:
	go test ./...

clean:
	rm -f tastastas
