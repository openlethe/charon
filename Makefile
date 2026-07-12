.PHONY: all build test vet fmt lint clean docker

BINARY := charon
LDFLAGS := -s -w

all: fmt vet test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/charon

test:
	go test -race -v ./...

vet:
	go vet ./...

fmt:
	gofmt -w -s .

lint: fmt vet

run:
	go run ./cmd/charon serve

clean:
	rm -rf bin/ data/

docker:
	docker build -t charon:latest .

.DEFAULT_GOAL := all
