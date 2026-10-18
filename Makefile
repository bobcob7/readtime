GOBIN := $(CURDIR)/bin

.PHONY: tools generate fmt test build

tools:
	GOBIN=$(GOBIN) go install github.com/matryer/moq mvdan.cc/gofumpt

generate: tools
	PATH="$(GOBIN):$$PATH" go generate ./...

fmt: tools
	$(GOBIN)/gofumpt -w .

test:
	go test -race ./...

build:
	go build -o $(GOBIN)/readtime ./cmd/readtime
