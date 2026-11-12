# Noray protocol and behaviour

What the original Node server ([foxssake/noray](https://github.com/foxssake/noray) 1.5.1, read from `src/`, `test/` and its `@foxssake/trimsock-*` 0.17 dependency) does on the wire, written so that a replacement can be checked against it. Everything here is implemented by noray-go; deliberate differences are marked **Go:**.

## Ports

| Port (default) | Proto | Bound to | What |
|---|---|---|---|
| 8890 `NORAY_SOCKET_PORT` | TCP | `NORAY_SOCKET_HOST` (0.0.0.0) | line commands |
| 8809 `NORAY_UDP_REGISTRAR_PORT` | UDP | always 0.0.0.0 | remote address registrar |
| 49152-51200 `NORAY_UDP_RELAY_PORTS` | UDP | always 0.0.0.0 | one port per relay |
| 8891 `NORAY_HTTP_PORT` | TCP/HTTP | `NORAY_HTTP_HOST` (0.0.0.0) | Prometheus metrics |

All UDP sockets are IPv4 (`udp4`), so addresses on the wire are always `a.b.c.d:port`.

## Concepts

- **Host**: every party (game host *and* client) sends `register-host` and gets an **OID** (open id: the share code others connect to) and a **PID** (private id: only sent to the registrar). A client is just a "host" nobody connects to.
- **Remote address** (`rinfo`): the public UDP address the registrar saw the PID come from. Clients then reuse that same local UDP port for the game so the NAT mapping stays valid.
- **Relay**: an entry `{ address: remote ip:port, port: local relay port }`. At most one relay per remote address.

## TCP line commands

Framing is [trimsock](https://github.com/foxssake/trimsock): `<name>[ <data>]\n`, UTF-8. Parsing details that matter:

- The command name ends at the first space; exactly one space is consumed, the rest of the line is the data (`connect  x` has data ` x`). Data may contain `"quoted chunks"` (quotes removed) and the escapes `\n`, `\r`, `\"`. A newline inside quotes does not end the line.
- `\r` is **not** stripped: `register-host\r\n` is the unknown command `register-host\r` (ignored), and `connect <oid>\r\n` looks up the oid `<oid>\r`. Clients must send bare `\n`.
- A line starting with `\r` is a raw command header `\r<name> <size>\n` followed by `size` bytes and a byte that is skipped.
- Conventions on the name: `name?id` = request, `name.id` = success response, `name!id` = error response, `name|id` = stream chunk (or stream end if there is no data). Requests and stream chunks are dispatched to the handler for `name`.
- Unknown commands, and empty lines, get no reply.
- Replies are serialized as `name p1 p2\n` (a param containing a space would be quoted).

| Client sends | Server replies | Notes |
|---|---|---|
| `register-host` | `set-oid <oid>` then `set-pid <pid>` (to the sender) | Always succeeds. A connection may register several times; each registration is a separate host, all removed when the TCP connection closes. |
| `connect <oid>` | to the sender: `connect <host ip:port>`; to the host's TCP connection: `connect <sender ip:port>` | NAT punch-through: both learn the other's registered public UDP address. Fails if the oid is unknown, the host has no registered address, the sender has not registered (by *this* TCP connection, first host it registered), or the sender has no registered address. |
| `connect-relay <oid>` | to the sender: `connect-relay <host relay port>`; to the host: `connect-relay <sender relay port>` | Ensures a relay for the host's and the sender's registered addresses (reusing existing ones) and returns the ports. The client then sends to `noray:<host relay port>` and the host to `noray:<client relay port>`. Fails if the oid or sender is unknown, if either has no registered address, or if the port pool is empty. The host's relay is created before the sender's address is checked. |

### Errors

On any handler error the original runs `exchange.failOrSend({ name, data: '' + error })`, but the serializer ignores `data`, so the error **text never reaches the client**:

- simple command: the reply is the bare command name, e.g. `connect\n` or `connect-relay\n`;
- request form (`connect?abc nope`): the reply is `!abc\n`.

Clients (like the Godot transport in Hooked) therefore treat "reply without a valid address/port" as failure.

### Quirks of the original reactor (and what Go does)

- One trimsock reader is shared by **all** TCP connections, so a partial line from one socket can be completed by bytes from another. **Go:** one parser per connection.
- More than 16384 buffered bytes throws `BufferOverflowError` inside the socket `data` handler, which crashes the process. **Go:** that connection's buffer is discarded, a warning is logged, the server keeps running.
- A response or stream-end command (`x.1`, `x!1`, `x|1`) with no matching exchange throws inside an un-awaited async function: an unhandled rejection that crashes Node ≥ 15. **Go:** ignored.
- A raw command whose size is not a number makes the reader wait forever (until the overflow crash). **Go:** same wait, but recovered by the overflow reset.

## UDP remote registrar (8809)

Each datagram is taken as a PID (UTF-8). Replies go to the sender:

- known PID, no address yet: store the sender's address as the host's remote address, reply `OK` (`noray_remote_registrar_success`);
- known PID, address already stored: reply `OK` and **keep the first address** (`noray_remote_registrar_repeat`); a client whose NAT mapping changed must register a new host;
- unknown PID: reply `Unknown host pid!` (`noray_remote_registrar_fail`).

The registrar does not create relays (the original's comment says it does; the code does not). Clients just resend every ~100 ms until they get an answer.

## UDP relays

### Port pool

`NORAY_UDP_RELAY_PORTS` is a comma separated list of literal ports (`2048`), absolute ranges (`2048-2051`) and offset ranges (`2048+3` = 2048..2051), freely combined. Parts are trimmed; `0` literals and parts with a non-number are dropped. The result is de-duplicated and sorted like JavaScript's default `Array.sort()`, i.e. **as strings** (`999, 1000` -> `[1000, 999]`). Each port is bound at startup; ports that fail to bind are logged at `warn` and skipped. Free ports form a stack: the last bound (or last freed) port is handed out first, so with the default range the first relay gets 51200. An empty list crashed the original at startup. **Go:** a startup error. An inverted range (`1026-1024`) threw `RangeError` at startup. **Go:** ignored.

### Forwarding

A datagram that arrives on relay port `P` from address `S` is forwarded if `S` has a relay (`src`) **and** `P` belongs to a relay (`dst`). It leaves through `src.port` towards `dst.address`, unchanged. So the host sees each client as `noray:<client relay port>` and the client sees the host as `noray:<host relay port>`. On success `src.lastReceived` and `dst.lastSent` are set to now.

Datagrams on a port that has never been assigned are discarded silently (no listener). Otherwise an unknown sender or target counts in `noray_relay_drop_count`.

### Dynamic relaying

If the sender is unknown but the target port belongs to a relay, a relay is created for the sender and the datagram is forwarded through it. This is how a peer that never called `connect-relay` (or whose relay expired) still reaches a host. The original takes one port from the pool and then `createRelay` takes another one, **leaking a port per dynamic relay**; when the pool runs out the rejection is unhandled and crashes the process. **Go:** one port per dynamic relay; if none is free the packet is dropped and a warning logged.

### Constraints

Checked in this order before every forward; the first failure blocks the packet:

1. **Per-relay bandwidth** `NORAY_UDP_RELAY_MAX_INDIVIDUAL_TRAFFIC` (default `128kb` = 131072 **bytes**/s), keyed by the sending relay.
2. **Global bandwidth** `NORAY_UDP_RELAY_MAX_GLOBAL_TRAFFIC` (default `1gb` = 1073741824 bytes/s).
3. **Lifetime** `NORAY_UDP_RELAY_MAX_LIFETIME_DURATION` (default `4hr`): blocked once `now - created >= limit`.
4. **Lifetime traffic** `NORAY_UDP_RELAY_MAX_LIFETIME_TRAFFIC` (default `4gb`): the relay's byte counter grows first (also for the packet that gets blocked), then blocks when `counter >= limit`.

The bandwidth limiter works in slices of `NORAY_UDP_RELAY_TRAFFIC_INTERVAL` (default `100ms`): each slice may carry `limit * interval` bytes (13107.2 bytes per 100 ms at 128kb/s). When more than one interval has passed since the slice started, a new slice starts and the packet passes unconditionally with the counter set to its size (so one oversized packet always passes at a slice start). Otherwise a packet that would exceed the slice is rejected and not counted. Counting stops at the first failing constraint (a packet blocked by the per-relay limit is not counted globally).

In the original, a failing constraint **throws inside the UDP `message` handler and kills the whole server** (`AssertionError: Bandwidth limit reached!`); every connected game loses its session. Hooked hit this with a 22 KB reliable burst (ADR 0008). **Go:** the packet is dropped and counted in `noray_relay_blocked_count{reason}`; the timestamps are not refreshed, so a relay that is only blocked expires normally.

### Cleanup

Every `NORAY_UDP_RELAY_CLEANUP_INTERVAL` (30s) relays whose `max(lastSent, lastReceived) <= now - NORAY_UDP_RELAY_TIMEOUT` (30s) are freed and their port returned (`noray_relay_expired`). Relays are not freed when their host disconnects from TCP; they only expire. A host disconnecting is removed from the host table immediately.

## Ids

- OID: nanoid `customAlphabet(NORAY_OID_CHARSET, NORAY_OID_LENGTH)`, default 21 characters of nanoid's URL alphabet `useandom-26T198340PX75pxJACKVERYMINDBUSHWOLF_GQZbfghjklqvwyzrict`. With `NORAY_ENABLE_WORDS_OID=true`, `NORAY_WORDS_OID_LENGTH` (3) random words from the 2638-word list concatenated (`FalconTimberYolk`), chosen with `crypto.randomInt`.
- PID: nanoid with `NORAY_PID_CHARSET`, `NORAY_PID_LENGTH` (128).
- An OID collision made the original's `register-host` fail (and leaked one `noray_active_hosts`). **Go:** regenerates (relevant with short word OIDs).

## Configuration

Read from the environment; a `.env` file in the working directory is loaded first without overriding set variables (dotenv 16 rules: `KEY=value`, `export`, quotes, `#` comments, `\n` in double quotes). Parsers:

- `integer`: JavaScript `parseInt` (leading digits, `0x` hex, junk after the number ignored: `21 ` is 21); `number`: `parseFloat`; `boolean`: only the exact string `true`.
- sizes (`byteSize`): `<number><unit>`, unit case-insensitive from `b kb mb gb tb pb eb zb yb`, powers of **1024**, and always **bytes** (`1Gb` is 1073741824 bytes, not a gigabit). No unit = bytes.
- durations: `<number><unit>` in seconds, units `us ms s m h hr d w mo yr` (`m` = minute, `mo` = 30 days, `yr` = 365 days), case-insensitive, no unit = seconds. `5min` is an error.
- invalid sizes/durations crashed the original at startup. **Go:** exits with an error.

| Variable | Default | Meaning |
|---|---|---|
| `NORAY_OID_LENGTH` | 21 | OID length |
| `NORAY_OID_CHARSET` | URL alphabet | OID characters |
| `NORAY_ENABLE_WORDS_OID` | false | word OIDs |
| `NORAY_WORDS_OID_LENGTH` | 3 | words per OID |
| `NORAY_PID_LENGTH` | 128 | PID length |
| `NORAY_PID_CHARSET` | URL alphabet | PID characters |
| `NORAY_SOCKET_HOST` / `_PORT` | 0.0.0.0 / 8890 | TCP commands |
| `NORAY_HTTP_HOST` / `_PORT` | 0.0.0.0 / 8891 | metrics |
| `NORAY_UDP_REGISTRAR_PORT` | 8809 | registrar |
| `NORAY_UDP_RELAY_PORTS` | 49152-51200 | relay port pool |
| `NORAY_UDP_RELAY_TIMEOUT` | 30s | idle relay expiry |
| `NORAY_UDP_RELAY_CLEANUP_INTERVAL` | 30s | expiry check period |
| `NORAY_UDP_RELAY_MAX_INDIVIDUAL_TRAFFIC` | 128kb | bytes/s per relay |
| `NORAY_UDP_RELAY_MAX_GLOBAL_TRAFFIC` | 1gb | bytes/s total |
| `NORAY_UDP_RELAY_TRAFFIC_INTERVAL` | 100ms | limiter slice |
| `NORAY_UDP_RELAY_MAX_LIFETIME_DURATION` | 4hr | relay max age |
| `NORAY_UDP_RELAY_MAX_LIFETIME_TRAFFIC` | 4gb | relay max bytes |
| `NORAY_LOGLEVEL` | info | silent, trace, debug, info, warn, error, fatal (anything else = info) |

**Go only:** `NORAY_GO_TCP_MODE=goroutine` switches the Linux epoll TCP server to the portable goroutine-per-connection one.

## Logging

[pino](https://getpino.io) JSON lines on stdout: `{"level":30,"time":<epoch ms>,"pid":..,"hostname":"..","name":"<module>",...fields,"msg":".."}` with levels trace 10, debug 20, info 30, warn 40, error 50, fatal 60. At `info` every registration and disconnect is logged (`Registered host from address ...`, `Host disconnected, removing from repository`) and the full config at startup (`Loaded application config`). The original's child loggers repeat the `name` key (`"name":"noray",...,"name":"cmd:connect"`); Go writes the child name once (what JSON parsers keep anyway).

## Metrics (HTTP 8891)

`GET /metrics` (exactly that path, no query string; any method) returns the Prometheus text format; every other path is `404` with an empty body. prom-client 14 does not add `_total` to counter names.

| Metric | Type | Meaning |
|---|---|---|
| `noray_active_hosts` | gauge | registered hosts |
| `noray_relay_count` | gauge | relays in the table |
| `noray_relay_expired` | counter | relays freed by cleanup |
| `noray_relay_drop_count` | counter | datagrams with unknown sender or target (also counted when dynamic relaying then forwards them) |
| `noray_relay_duration` | histogram (s) | time to relay one datagram |
| `noray_relay_size` | histogram | datagram size. Uses prom-client's default buckets (0.005..10), so nearly everything lands in `+Inf`; kept for compatibility |
| `noray_remote_registrar_success` / `_fail` / `_repeat` | counter | registrar outcomes |
| `process_*` | | prom-client's process metrics (CPU seconds, RSS, fds, start time) |

**Go:** `nodejs_*` metrics are replaced by a few `go_*` ones, and two counters are added: `noray_relay_blocked_count{reason="individual_bandwidth|global_bandwidth|lifetime_duration|lifetime_traffic"}` and `noray_relay_send_error_count`.

## Shutdown

The original handles `SIGINT` only: it stops accepting TCP, stops the cleanup job, closes the registrar and every relay socket and the HTTP server; already open TCP connections are not closed. `SIGTERM` (what `docker stop` sends) is not handled, and Node as PID 1 ignores it, so `docker stop` waits 10 s and kills it. **Go:** `SIGINT` and `SIGTERM` both shut down gracefully, closing client connections too, and exit 0.

## What a real client uses

The Godot transport in Hooked (`game/scripts/net/noray_transport.gd`) uses exactly: `register-host` -> `set-oid`/`set-pid`; the pid datagram to 8809 until a reply, requiring `OK`; `connect <oid>` -> `connect <ip:port>` (also strips a `::ffff:` prefix and quotes); `connect-relay <oid>` -> `connect-relay <port>` (a non-numeric or missing port = failure); on the host side it reacts to unsolicited `connect <ip:port>` and `connect-relay <port>` lines by sending punch packets from its game socket. It sends `\n`-terminated lines and splits replies on the first space, which the server's output always matches.
