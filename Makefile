BINARY = bin/go-product-search
SERVICE = go-product-search

.PHONY: preflight build test lint vendor clean

# Fleet gate: gofmt → vet → lint → build → test (-race). Mirrors go-search /
# go-wowa preflight; GOWORK=off guards against a stray go.work on the host.
preflight:
	@echo "==> gofmt -l (vendor/ excluded — upstream code, never reformatted)"
	@dirty=$$(gofmt -l . | grep -v '^vendor/' || true); \
	  if [ -n "$$dirty" ]; then \
	    echo "FAIL: gofmt -- the following files are not formatted (run: gofmt -w <file>):"; \
	    echo "$$dirty"; \
	    exit 1; \
	  fi
	@echo "==> go vet ./..."
	GOWORK=off go vet ./...
	@echo "==> golangci-lint run ./..."
	GOWORK=off golangci-lint run ./...
	@echo "==> go build ./..."
	GOWORK=off go build ./...
	@echo "==> go test -race -count=1 ./..."
	GOWORK=off go test -race -count=1 ./...

build:
	GOWORK=off go build -ldflags="-s -w" -o $(BINARY) ./cmd/server

test:
	GOWORK=off go test -race -count=1 ./...

lint:
	GOWORK=off golangci-lint run ./...

vendor:
	GOWORK=off go mod tidy
	GOWORK=off go mod vendor

clean:
	rm -f $(BINARY)
