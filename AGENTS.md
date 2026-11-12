# Agent notes

- Goal: byte-compatible Go drop-in for foxssake/noray 1.5.1. The behaviour spec is `docs/protocol.md`; decisions are ADRs in `docs/adr/`. Change the spec doc when you change observable behaviour.
- Layout: `cmd/noray` (main), `internal/{config,logger,ids,trimsock,repository,hosts,relay,metrics,server}`, `conformance/` (Node black-box suite, builtins only), `bench/` (loadgen + `run.sh` + Node baseline Dockerfile).
- Platform split: `internal/server/tcp_epoll_linux.go` (single epoll loop, Linux) vs `tcp_goroutine.go` (portable, macOS dev); `internal/relay/recv_linux.go` (zero-alloc recvfrom) vs `recv_other.go`. Run `make test-linux` after touching either; macOS tests do not cover the Linux files.
- Tests: `make test` (unit, ported from the original `test/spec`), `make conformance` against a running server (must pass against the original Node server too: it validates the suite), `make robustness` only against a disposable server.
- Hot path: `relay.Handler.Relay` must stay allocation-free (`TestRelayHotPathAllocations`). No logging above trace level per packet.
- Never push images; `docker build --load` only. Benchmark results go to `bench/results/` (summary.md is committed, raw jsonl too).
- Commits: author Blankeos, no AI attribution.
