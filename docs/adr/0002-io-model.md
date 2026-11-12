# 0002: I/O model: epoll loop for TCP, parked goroutine per relay port

Status: accepted (2026-11-13)

## TCP commands

First version: a goroutine per connection. At 20 000 idle hosts it used ~160 MiB against Node's ~84 MiB: each goroutine's stack had grown to 4-8 KiB while handling `register-host` and never shrank.

Now, on Linux, one goroutine runs an epoll loop over the listener and every connection (level-triggered, non-blocking `accept4`/`read`/`write`, output buffered and `EPOLLOUT` armed only on `EAGAIN`, closes deferred to the end of each event batch so descriptor numbers cannot be reused mid-batch). All command handlers run on that goroutine, so writes to other connections (`connect` notifies the host) need no locks, and an idle connection costs only its fd and ~1 KiB of heap (session, host entry, map slots). Elsewhere (macOS) and with `NORAY_GO_TCP_MODE=goroutine`, the portable goroutine-per-connection server is used. Its read buffer is borrowed from a pool only while data is drained (`RawConn.Read`), not held while idle.

## UDP relays

Every relay port has a goroutine parked in the runtime netpoller (`RawConn.Read`), so ports are served in parallel on multi-core hosts. A receive buffer (64 KiB) is taken from a `sync.Pool` only while a socket is drained, so 2049 idle ports cost goroutine stacks only (~10 MiB total at the default range, measured), not buffers. On Linux `recvfrom` is a raw syscall into a per-socket sockaddr (no allocation); sending uses `WriteToUDPAddrPort` (no allocation). The relay table is `map[netip.AddrPort]*Entry` plus a 65536-slot port array behind an `RWMutex`; constraints keep their state on the entry (limiter pointer, traffic counter). `TestRelayHotPathAllocations` keeps `Relay` at zero allocations per packet.

Not done: `recvmmsg`/`sendmmsg` batching or a single epoll loop for UDP. The benchmark saturates around 240-320 k packets/s on one core; batching would help mostly beyond that.
