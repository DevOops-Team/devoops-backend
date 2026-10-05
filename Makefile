export GOMODCACHE := $(CURDIR)/.cache/mod
export GOCACHE := $(CURDIR)/.cache/go
.PHONY: test test-integration build vet
test:
	go test -race ./...
test-integration:
	./scripts/test-integration.sh
build:
	go build -o bin/vdi-api ./cmd/server
vet:
	go vet ./...
