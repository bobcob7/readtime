.PHONY: generate fmt test build

generate:
	go generate ./...

fmt:
	go tool gofumpt -w .

test:
	go test -race ./...

build:
	go build -o bin/readtime ./cmd/readtime
