# 0003: Benchmark method

Status: accepted (2026-11-13)

- Both servers run as local OrbStack containers (arm64 Linux VM, 14 host cores) with `--cpus=1 --memory=256m --ulimit nofile=65536`, one at a time, a fresh container per run, on a user network. The Node image is the original 1.5.1 on `node:22-alpine` with pnpm 9 (`bench/node/Dockerfile`; the upstream Dockerfile no longer builds).
- The load generator (`bench/loadgen`, Go) runs in another, unlimited container on the same network and speaks the real protocol: `register-host`, pid to the registrar until `OK`, `connect-relay` per client. Each room is 1 host + 5 clients; every client sends to the host's relay port and the host sends to every client's relay port at the same rate, 60-200 byte payloads (random), so a room at 30 Hz is 300 packets/s through the server.
- Latency is one-way, loadgen -> server -> loadgen, from a timestamp in the payload (same process clock). It includes two Docker bridge hops and loadgen scheduling, so it is an upper bound on what the server adds. Loss counts packets sent inside the window that never arrived (1 s drain).
- CPU is cgroup CPU time over the window from the Docker Engine API (100% = one core); memory is the median of 1 s samples of `usage - inactive_file` (what `docker stats` shows).
- Relay benchmarks set `NORAY_UDP_RELAY_MAX_INDIVIDUAL_TRAFFIC=1Gb`, `..._GLOBAL_TRAFFIC=100Gb`, `..._LIFETIME_TRAFFIC=1Pb` and `NORAY_UDP_RELAY_PORTS=46000-50999` (500 rooms need 3000 relays), the same for both. Idle/startup and the "limits" scenario use the defaults (2049 ports, 128 KB/s per relay).
- Startup is container `StartedAt` to the first accepted TCP connection (a probe polling every 1 ms), so it includes container start overhead.
- Saturation: 50 rooms, rate raised step by step (8 s windows); capacity is the highest step with loss <= 1%.
- Every scenario ran 3 times; tables show medians with min-max. The Mac was not otherwise idle (other containers running), so treat small differences as noise.
