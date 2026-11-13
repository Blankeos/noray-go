# noray-go

> [!NOTE]
> **This is a Go port of [Noray](https://github.com/foxssake/noray) by [Tamás Gálffy / foxssake](https://github.com/foxssake).**
> The protocol, behaviour, word list and tests all come from the original (MIT). noray-go is a drop-in replacement: same TCP commands, same UDP registrar and relays, same `NORAY_*` config, same Prometheus metrics. It passes the original's own end-to-end tests, and it is smaller and faster (see [Performance](#-performance)) and doesn't crash when a relay goes over its bandwidth limit.

A simple connection orchestrator and relay to bulletproof connectivity for your online multiplayer games, as a 6 MB static binary.

## ✨ Highlights

| | Node original | noray-go |
|---|---|---|
| Docker image | 320 MB | **6.1 MB** |
| Idle memory | 18.7 MiB | **10.3 MiB** |
| 20 000 held hosts | 84.8 MiB | **46.6 MiB** |
| Relay capacity (≤ 1% loss) | about 60k packets/s | **about 280k packets/s** |
| 500 rooms x 6 players @ 30 Hz | 74% packet loss | **0% loss** |
| Relay over its bandwidth limit | process crashes | **drops the packets, keeps running** |

## 🧭 Why orchestration

If you're already familiar with the topic, noray can help with NAT punchthrough.

If you're not familiar with the issue, read Keith Johnston's [article](https://keithjohnston.wordpress.com/2014/02/17/nat-punch-through-for-multiplayer-games/) on the topic. In short:

* Most PCs online are behind a router
* Routers only allow traffic to your PC if it's in response to something you sent
* So if you host an online game, people won't be able to connect to your PC
* NAT punchthrough is both parties sending traffic to each other: the first packets fail, the next ones succeed because each router now sees outgoing traffic

*noray* helps by orchestrating the NAT punchthrough process 🔥

## 🔁 Why relaying

Even NAT punchthrough is not always viable, depending on your players' NAT setup. To make sure players can always connect, *noray* can act as a relay 🔥

*noray* dedicates a specific UDP port to each player, at which others can send data to them. Any data incoming on this dedicated port is transmitted as-is to the appropriate player.

*NOTE:* Relaying only supports UDP traffic.

## 📦 Dependencies

* [Go](https://go.dev/dl/) 1.26 or newer, to build from source
* Or just Docker: the image has no runtime dependencies (`FROM scratch`)

## 🛠️ Installation

```sh
git clone https://github.com/Blankeos/noray-go
cd noray-go
make build          # ./bin/noray
```

## ▶️ Usage

```sh
./bin/noray         # or: go run ./cmd/noray
```

```sh
printf 'register-host\n' | nc -w1 localhost 8890
# set-oid TWmoTe-qqbUShZcU8Qlrx
# set-pid 2x0GPldCX...
```

Upon startup the application allocates all the configured ports and starts listening. Logs are written to stdout as pino-style JSON lines. `SIGTERM` and `SIGINT` shut down gracefully.

Ports (all configurable):

* TCP **8890** for clients to register and request connections
* UDP **8809** for the remote address registrar
* UDP **49152-51200** for relays (make sure Docker maps the same range)
* HTTP **8891** for Prometheus metrics at `/metrics`

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
| `NORAY_UDP_RELAY_WAIT_FOR_PEER` | `false` | relay to an address only once it has sent to the sender's relay port (new, see below) |
| `NORAY_LOGLEVEL` | `info` | `silent trace debug info warn error fatal` |

`NORAY_UDP_RELAY_WAIT_FOR_PEER`: both sides of a relay start sending when Noray answers `connect-relay`. Some home routers block a remote endpoint for good once a packet from it arrives before they have sent to it, so if one side is late by more than the relay's one-way trip (a slow frame, a resent TCP reply), the early side's packets arrive first and that relay never carries anything either way. With this on, a packet is relayed to an address only once that address has sent to the sender's relay port; until then it is dropped (`noray_relay_held_count`). Every router then sees its own packet first. Protocols that resend their first packets (ENet does) lose nothing; a one-shot packet sent before the other side has spoken is lost, which is why it is off by default (the original relays at once).

Sizes are bytes in powers of 1024 (`1Gb` = 1 GiB of bytes, not a gigabit); durations take `us ms s m h hr d w mo yr`. Logs are pino-style JSON lines on stdout.

### 🐳 Usage with Docker

```sh
docker build --load -t noray-go .
docker run --rm -p 8890:8890 -p 8809:8809/udp -p 8891:8891 \
  -p 49152-51200:49152-51200/udp noray-go
```

Multi-stage build, `CGO_ENABLED=0`, final image `FROM scratch`, runs as nobody. `SIGTERM` and `SIGINT` shut down gracefully.

## 📖 Documentation

The full wire behaviour, quirks included, is in [docs/protocol.md](docs/protocol.md). What changed from the original and why: [docs/adr/0001-go-rewrite.md](docs/adr/0001-go-rewrite.md).

### Protocol

Data is transmitted over TCP as newline-separated strings. Each line starts with a command, a space, and the rest of the line is data. Example:

```
connect-relay host-1
```

The protocol has no concept of replies, threads or correspondences. Think of it as a dumbed-down RPC without return values.

### Flows

#### Host registration

Each player first registers as a host (clients too) by sending `register-host` over TCP. *noray* replies with the host's OpenID and PrivateID:

```
set-oid [openid]
set-pid [privateid]
```

These IDs are needed for any later exchange. Don't forget to end your messages with a newline!

#### Remote address registration

To orchestrate connections, *noray* needs each host's external address. Create a UDP socket and send the host's PrivateID to the registrar port. This is idempotent: send multiple packets until you get a reply. On success the reply is `OK`, otherwise an error message.

#### Connecting

Connecting can be attempted via NAT punchthrough or relay. Since the relay ports are limited, prefer punchthrough whenever possible. You need the target host's OpenID (sharing it is up to you), then send one of:

```
connect [openid]
connect-relay [openid]
```

The server replies with the same command: the target address and port for punchthrough (`connect 87.53.78.15:55759`), or the relay port on the *noray* server itself (`connect-relay 49178`). Both parties receive the appropriate command, and should then attempt a UDP handshake with each other.

## 🧪 Tests

| | |
|---|---|
| `make test` | Go unit tests, ported from the original `test/spec` (config parsers, repository, bandwidth limiter, constraints, relay entry, socket pool, relay handler, cleanup, dynamic relaying, registrar) plus trimsock, ids and an in-process end-to-end test |
| `make test-linux` | the same on Linux in Docker (covers the epoll TCP server and the zero-alloc receive path) |
| `make conformance` | the original's e2e tests ported to a black-box Node suite (`conformance/`, builtins only) plus extra compatibility checks; point it anywhere with `NORAY_HOST`, `NORAY_SOCKET_PORT`, `NORAY_UDP_REGISTRAR_PORT`, `NORAY_HTTP_PORT` |
| `make robustness` | inputs that crash the original server; run against a disposable server |

Results (2026-11-13):

| Check | Original (Node) | noray-go |
|---|---|---|
| Conformance suite (5 ported e2e tests + 21 compatibility checks), macOS native and Linux containers | 26/26 | 26/26 |
| Robustness (orphan `x.1` response, 17 KB line, relay over 128 KB/s) | 0/3, each one crashes the process | 3/3 |
| Hooked's Godot client (`tests/net_test.tscn`): hole punch, forced relay, each at 0 ms and at 150 ms ping + 2% loss | 4/4 pass | 4/4 pass |
| `docker stop` | ignores SIGTERM, killed after 10 s | exits 0 in 0.2 s |

## 📊 Performance

Original 1.5.1 on Node 22 vs noray-go, each in a local container limited to **1 CPU and 256 MB**, same config, load from a Go load generator in another container speaking the real protocol (rooms of 1 host + 5 clients, 60-200 byte packets both ways). Medians of 3 runs; method in [docs/adr/0003-benchmark-method.md](docs/adr/0003-benchmark-method.md), every run in [bench/results/](bench/results/summary.md). Relay limits were raised so they never interfere, except in the last row.

| | Node | Go |
|---|---|---|
| Image size | 320 MB | **6.1 MB** |
| Startup (container start to TCP ready, 2049 relay ports bound) | 227 ms | **114 ms** |
| Idle, default config: memory / CPU | 18.7 MiB / 1.3% | **10.3 MiB / 0.0%** |
| 20 000 registered hosts holding TCP: memory | 84.8 MiB | **46.6 MiB** |
| Per-host cost (20 000 hosts vs none) | ~3.1 KiB | **~1.3 KiB** |
| Register 5 000 hosts (64 in parallel) / 20 000 | 0.64 s / 1.76 s | **0.11 s / 0.67 s** |
| 10 rooms x 6 @ 30 Hz (3 k pkt/s): CPU, latency p50 / p99 | 39%, 1.25 / 6.6 ms | **16%, 0.36 / 2.4 ms** |
| 100 rooms x 6 @ 30 Hz (30 k pkt/s): CPU, latency p50 / p99 | 64%, 1.2 / **5.1** ms | **58%**, **0.6** / 5.7 ms |
| 500 rooms x 6 @ 30 Hz (150 k pkt/s offered) | delivers 38 k pkt/s (**74% loss**, p50 6 s) | **150 k pkt/s, 0% loss**, p50 3.0 ms, p99 34 ms, 100% CPU |
| Saturation, 50 rooms (highest rate with <= 1% loss) | 60 k pkt/s | **280 k pkt/s** (320 k at 0.7% median loss, noisy) |
| Default limits, 1000-1200 byte packets @ 30 Hz (host relay > 128 KB/s) | **crashed 3/3** (`Bandwidth limit reached!`) | kept running, dropped 18% (`noray_relay_blocked_count`) |

Where Go is not clearly better: at moderate load (100 rooms, 30 k pkt/s) CPU is about the same (58% vs 64%) and Go's p99 is slightly worse; the per-packet cost there is mostly the kernel and wake-ups, not the server code. With 5000 relay ports bound and nothing else, memory is close (20.7 vs 23.5 MiB): Go keeps a parked goroutine per relay port. Latency includes two Docker bridge hops and the load generator, so treat it as relative. The Mac was not idle during the runs; min-max ranges are in the results file.

Run it yourself (builds images locally, never pushes):

```sh
NORAY_NODE_SRC=/path/to/foxssake/noray make bench   # ~45 min
```

## 📜 License

*noray-go* is licensed under the [MIT license](LICENSE), like the original. Noray is by Tamás Gálffy ([foxssake/noray](https://github.com/foxssake/noray)); the protocol, behaviour, word list and tests this port follows are his. Also see [netfox](https://github.com/foxssake/netfox), the Godot addon collection that uses noray.
