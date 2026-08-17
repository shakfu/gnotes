# gnotes

BIN     := gnotes
CMD     := ./cmd/gnotes
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build
build: ## compile the binary into ./gnotes
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(CMD)

.PHONY: install
install: ## install into $GOPATH/bin
	go install -ldflags "$(LDFLAGS)" $(CMD)

.PHONY: build-slim
build-slim: ## compile without the browser view, saving about 3 MB
	go build -tags noweb -ldflags "$(LDFLAGS)" -o $(BIN) $(CMD)

.PHONY: test
test: ## run every test with the race detector
	go test -race ./...

.PHONY: test-short
test-short: ## run tests without the race detector, for a quick loop
	go test ./...

.PHONY: cover
cover: ## report per-package coverage
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -20

.PHONY: cover-html
cover-html: cover ## open the coverage report in a browser
	go tool cover -html=coverage.out

.PHONY: bench
bench: ## run the benchmarks
	go test -run XXX -bench . -benchmem ./...

.PHONY: lint
lint: ## vet and check formatting
	go vet ./...
	go vet -tags noweb ./...
	@unformatted=$$(gofmt -l . | grep -v '^vendor/' || true); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: tidy
tidy: ## prune and verify go.mod
	go mod tidy
	go mod verify

.PHONY: check
check: lint test ## everything CI runs

.PHONY: clean
clean: ## remove build and coverage artifacts
	rm -f $(BIN) coverage.out

.PHONY: help
help: ## list the targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
