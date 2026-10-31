VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/codewith-aditya/astra-moderation/internal/api.Version=$(VERSION)

.PHONY: all build test race vet fmt run docker clean loc

all: vet test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/astra-moderation ./cmd/server

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

run:
	go run ./cmd/server

docker:
	docker build --build-arg VERSION=$(VERSION) -t astra-moderation:$(VERSION) -t astra-moderation:latest .

clean:
	rm -rf bin dist data

loc:
	@find . -name '*.go' -not -path './vendor/*' | xargs wc -l | tail -1
	@echo "--- tests ---"
	@find . -name '*_test.go' | xargs wc -l | tail -1
