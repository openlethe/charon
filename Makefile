.PHONY: all build build-all test vet fmt format-check lint clean docker release

BINARY := charon
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(BUILD_DATE)

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/charon

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

build-all: clean
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		GOOS=$$(echo $$platform | cut -d/ -f1); \
		GOARCH=$$(echo $$platform | cut -d/ -f2); \
		OUTPUT=dist/$(BINARY)-$$GOOS-$$GOARCH; \
		if [ "$$GOOS" = "windows" ]; then OUTPUT=$$OUTPUT.exe; fi; \
		echo "Building $$platform -> $$OUTPUT"; \
		GOOS=$$GOOS GOARCH=$$GOARCH CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $$OUTPUT ./cmd/charon; \
	done
	@echo "Done. Binaries in dist/"

release: build-all
	@cd dist && shasum -a 256 charon-* > checksums.txt
	@printf 'version=%s\ncommit=%s\nbuild_date=%s\n' "$(VERSION)" "$(COMMIT)" "$(BUILD_DATE)" > dist/build-info.txt
	@echo "Release artifacts ready in dist/"

test:
	go test -race -v ./...

vet:
	go vet ./...

fmt:
	gofmt -w -s .

format-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

lint: format-check vet

run:
	go run ./cmd/charon serve

clean:
	rm -rf dist/
	rm -f $(BINARY)

docker:
	docker build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		-t $(BINARY):latest .

.DEFAULT_GOAL := build
