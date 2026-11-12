# 0001: Rewrite Noray in Go as a drop-in replacement

Status: accepted (2026-11-13)

## Why

The original Node server (foxssake/noray 1.5.1) works, but:

- **It crashes on ordinary overload.** Relay constraints `assert` inside the UDP `message` listener; a relay that goes over 128 KB/s throws and the uncaught exception kills the process, ending every session on the server. Hooked hit it with a 22 KB burst (Hooked ADR 0008). A response line with an unknown id (`x.1`), a 16 KiB+ line, or an exhausted port pool during dynamic relaying also crash it.
- Lookups are linear scans: every relayed packet scans the relay table twice, every registrar request and `connect` scans the host list.
- Its upstream Dockerfile no longer builds, and the image is ~320 MB.

Go gives a single static binary (6 MB `scratch` image), real parallelism, and predictable memory.

## Compatibility strategy

- **The wire is the contract.** `docs/protocol.md` records what the original does, read from its source and its trimsock dependency, including the odd parts clients may depend on: errors reply with the bare command name (`connect\n`) or `!<id>\n` because the original's serializer drops the error text; `\r` is not stripped; the registrar keeps the first address; free ports are a stack; relay ports are sorted as strings; byte sizes are bytes with 1024 steps.
- **Same config, same names.** All `NORAY_*` variables, defaults, `.env` loading and parsers (JS `parseInt`/`parseFloat` semantics), pino-shaped JSON logs, and every `noray_*` metric with the same name, type and help text.
- **Two test layers.** The original's spec tests are ported to Go unit tests per area. The original's e2e tests are ported into `conformance/`, a black-box Node suite that targets any `host:port`, plus extra compatibility checks. The suite must pass against the original first (that validates the harness), then against Go. The real client (Hooked's Godot `NorayTransport`, `tests/net_test.tscn`) is run against both, on the punch and relay paths, with and without simulated 150 ms ping and 2% loss.

## What differs (on purpose)

- A blocked packet (any relay constraint) is dropped and counted in `noray_relay_blocked_count{reason}` instead of crashing. Blocked packets do not refresh relay timestamps.
- Malformed TCP input never crashes: per-connection parser, overflow resets that connection's buffer, orphan responses are ignored.
- Dynamic relaying takes one port per relay (the original leaked one more per dynamic relay) and drops the packet when the pool is empty.
- OID collisions are retried instead of failing `register-host`.
- `SIGTERM` shuts down gracefully (the original ignored it as PID 1, so `docker stop` waited 10 s); shutdown also closes client connections.
- Invalid config (bad unit, no relay ports, inverted range) is a clean startup error or ignored range instead of a crash.
- Extra metrics: `noray_relay_blocked_count`, `noray_relay_send_error_count`, `go_*` instead of `nodejs_*`.
- `/metrics` sends `Content-Type` and `Content-Length` (the original sent neither, chunked).

## Known gaps / risks

- Only the trimsock features Noray's handlers can observe are implemented. Requests are answered like the original (plain commands, or `!<id>` on error); the server never sends requests itself.
- IPv4 only, like the original (`udp4` sockets).
- Log lines match pino's shape, not every byte (child loggers write `name` once; error objects are `{type,message}` without stacks).
- Relay datagrams are received into a 64 KiB pooled buffer; larger UDP payloads cannot exist anyway.
- On Linux the TCP server is a single epoll loop (ADR 0002): command handling is serialized like Node's event loop, which is plenty for registrations, but one slow handler delays all of them.
- The Godot client test was run against the native macOS build (goroutine TCP path). The Linux epoll path is covered by the conformance suite and unit tests in Docker, not by the Godot client (OrbStack NATs the Mac as `192.168.x.0`, which breaks punch-through and relay for any server in a container, the Node one included).
