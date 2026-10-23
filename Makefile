.DEFAULT_GOAL := help
BINARY  := barge
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build test test-integration cover lint fmt vet vuln tidy-check cross ci

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

build: ## build ./bin/barge
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/barge

test: ## unit tests with the race detector
	go test -race -shuffle=on ./...

test-integration: ## tests against real docker/podman (needs a reachable daemon)
	go test -race -tags=integration ./...

cover: ## coverage (fails under 80%; test-support packages excluded)
	go test -race -coverprofile=coverage.out $$(go list ./internal/... | grep -v 'test$$')
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); if ($$3+0 < 80) {print "coverage below 80%"; exit 1}}'

fmt: ## format
	gofmt -w .

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (v2)
	golangci-lint run

vuln: ## known-vulnerability scan
	govulncheck ./...

tidy-check: ## fail if go.mod/go.sum are not tidy
	go mod tidy -diff

cross: ## prove it builds for every supported OS/arch
	@for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do \
		GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 go build -o /dev/null ./... && echo "ok $$t" || exit 1; done

ci: vet test cover tidy-check cross ## what CI runs (plus lint, vuln)

.PHONY: snapshot
snapshot: ## local release dry run into ./dist (no signing or SBOM)
	goreleaser release --snapshot --clean --skip=sign,sbom
