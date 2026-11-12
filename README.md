# noray-go

A Go drop-in replacement for [Noray](https://github.com/foxssake/noray), the NAT punch-through orchestrator and UDP relay for online games (used by netfox and Godot games). Same TCP commands, same UDP registrar and relays, same `NORAY_*` config, same Prometheus metrics, in a 6 MB static binary that doesn't crash when a relay goes over its bandwidth limit.

## ✨ What it does

- **TCP 8890**: `register-host` -> `set-oid <oid>` + `set-pid <pid>`; `connect <oid>` swaps both parties' public UDP addresses (hole punching); `connect-relay <oid>` returns relay ports when punching fails.
- **UDP 8809**: send your pid, get `OK`; Noray now knows your public address.
- **UDP 49152-51200**: one relay port per party, with per-relay and global bandwidth limits, lifetime limits, idle cleanup and dynamic relaying.
- **HTTP 8891** `/metrics`: the original's `noray_*` metrics.

The full wire behaviour, quirks included, is in [docs/protocol.md](docs/protocol.md). What changed and why: [docs/adr/0001-go-rewrite.md](docs/adr/0001-go-rewrite.md).

## 🚀 Quick start

```sh
make build && ./bin/noray          # or: go run ./cmd/noray
```

```sh
printf 'register-host\n' | nc -w1 localhost 8890
# set-oid TWmoTe-qqbUShZcU8Qlrx
# set-pid 2x0GPldCX...
```

## ⚙️ Configuration

Environment variables (or a `.env` in the working directory), identical to the original:

| Variable | Default | |
|---|---|---|
| `NORAY_OID_LENGTH` / `NORAY_OID_CHARSET` | `21` / nanoid URL alphabet | open id (share code) |
| `NORAY_ENABLE_WORDS_OID` / `NORAY_WORDS_OID_LENGTH` | `false` / `3` | word codes like `FalconTimberYolk` |
| `NORAY_PID_LENGTH` / `NORAY_PID_CHARSET` | `128` / URL alphabet | private id |
| `NORAY_SOCKET_HOST` / `NORAY_SOCKET_PORT` | `0.0.0.0` / `8890` | TCP commands |
| `NORAY_HTTP_HOST` / `NORAY_HTTP_PORT` | `0.0.0.0` / `8891` | metrics |
| `NORAY_UDP_REGISTRAR_PORT` | `8809` | UDP registrar |
| `NORAY_UDP_RELAY_PORTS` | `49152-51200` | relay ports: `2048`, `2048-2051`, `2048+3`, comma separated |
| `NORAY_UDP_RELAY_TIMEOUT` | `30s` | free relays idle this long |
| `NORAY_UDP_RELAY_CLEANUP_INTERVAL` | `30s` | how often to check |
| `NORAY_UDP_RELAY_MAX_INDIVIDUAL_TRAFFIC` | `128kb` | bytes/s per relay (`kb` = 1024 bytes) |
| `NORAY_UDP_RELAY_MAX_GLOBAL_TRAFFIC` | `1gb` | bytes/s for all relays |
| `NORAY_UDP_RELAY_TRAFFIC_INTERVAL` | `100ms` | bandwidth limiter time slice |
| `NORAY_UDP_RELAY_MAX_LIFETIME_DURATION` | `4hr` | block relays older than this |
| `NORAY_UDP_RELAY_MAX_LIFETIME_TRAFFIC` | `4gb` | block relays after this many bytes |
| `NORAY_LOGLEVEL` | `info` | `silent trace debug info warn error fatal` |

Sizes are bytes in powers of 1024 (`1Gb` = 1 GiB of bytes, not a gigabit); durations take `us ms s m h hr d w mo yr`. Logs are pino-style JSON lines on stdout.

## 🐳 Docker

```sh
docker build --load -t noray-go .
docker run --rm -p 8890:8890 -p 8809:8809/udp -p 8891:8891 \
  -p 49152-51200:49152-51200/udp noray-go
```

Multi-stage build, `CGO_ENABLED=0`, final image `FROM scratch`, runs as nobody. `SIGTERM` and `SIGINT` shut down gracefully.

## 🧪 Tests

| | |
|---|---|
| `make test` | Go unit tests, ported from the original `test/spec` (config parsers, repository, bandwidth limiter, constraints, relay entry, socket pool, relay handler, cleanup, dynamic relaying, registrar) plus trimsock, ids and an in-process end-to-end test |
| `make test-linux` | the same on Linux in Docker (covers the epoll TCP server and the zero-alloc receive path) |
| `make conformance` | the original's e2e tests ported to a black-box Node suite (`conformance/`, builtins only) plus extra compatibility checks; point it anywhere with `NORAY_HOST`, `NORAY_SOCKET_PORT`, `NORAY_UDP_REGISTRAR_PORT`, `NORAY_HTTP_PORT` |
| `make robustness` | inputs that crash the original server; run against a disposable server |

Results (2026-11-13):

RESULTS_TESTS

## 📊 Benchmarks

RESULTS_BENCH

## 📜 License and credit

MIT, like the original. Noray is by Tamás Gálffy ([foxssake/noray](https://github.com/foxssake/noray)); the protocol, behaviour, word list and tests this port follows are his. See [LICENSE](LICENSE).
