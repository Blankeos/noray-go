# noray-go
NORAY_NODE_SRC ?= ../bevy-noray/noray
IMAGE ?= noray-go:local

.PHONY: build test test-linux conformance robustness run docker bench clean

build: ## build bin/noray and bin/loadgen
	CGO_ENABLED=0 go build -trimpath -o bin/noray ./cmd/noray
	CGO_ENABLED=0 go build -trimpath -o bin/loadgen ./bench/loadgen

test: ## unit tests (ported from the original test/spec)
	go vet ./...
	go test -race ./...

test-linux: ## unit tests on Linux (epoll TCP + zero-alloc recv path)
	docker run --rm -v "$$PWD":/src -w /src -e GOFLAGS=-buildvcs=false golang:1.26-alpine go test -count=1 ./...

conformance: ## black-box suite against a running server (NORAY_HOST, NORAY_SOCKET_PORT, ...)
	cd conformance && node --test --test-concurrency=1 --test-reporter=spec *.test.mjs

robustness: ## inputs that crash the original server (use a disposable server)
	cd conformance && NORAY_ROBUSTNESS=1 node --test --test-concurrency=1 --test-reporter=spec robustness.test.mjs

run: build ## run locally
	./bin/noray

docker: ## build the image locally (never pushed)
	docker build --load -t $(IMAGE) .

bench: ## Go vs Node benchmark in local containers (see bench/run.sh)
	NORAY_NODE_SRC=$(NORAY_NODE_SRC) bench/run.sh all

clean:
	rm -rf bin
