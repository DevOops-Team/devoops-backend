export GOMODCACHE := $(CURDIR)/.cache/mod
export GOCACHE := $(CURDIR)/.cache/go
.PHONY: build vet
build:
	go build -o bin/vdi-api ./cmd/server
vet:
	go vet ./...
