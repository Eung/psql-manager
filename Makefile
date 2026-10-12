GO      ?= go
BIN     := psql-manager
PKG     := ./cmd/psql-manager
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build check fmt-check vet test cover integration cross clean

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o dist/$(BIN) $(PKG)

check: fmt-check vet test

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt 필요:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

integration:
	$(GO) test -tags integration ./...

cross:
	@for os in linux darwin; do \
		for arch in amd64 arm64; do \
			echo "build $$os/$$arch"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" \
				-o dist/$(BIN)-$$os-$$arch $(PKG) || exit 1; \
		done; \
	done

clean:
	rm -rf dist coverage.out
