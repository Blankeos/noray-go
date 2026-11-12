#!/usr/bin/env bash
# Benchmarks the Go server against the original Node server in local Docker
# containers with identical limits and config. Images are built locally and
# never pushed.
#
#   NORAY_NODE_SRC=/path/to/foxssake/noray bench/run.sh [all|idle|hosts|relay|sweep|limits]
#
# Results: bench/results/<scenario>.jsonl, summary: bench/results/summary.md
set -euo pipefail

cd "$(dirname "$0")/.."
NODE_SRC=${NORAY_NODE_SRC:?set NORAY_NODE_SRC to the original noray checkout}
WHAT=${1:-all}
REPEATS=${REPEATS:-3}
IMPLS=${IMPLS:-"node go"}
NET=noray-bench
OUT=${OUT:-bench/results}
mkdir -p "$OUT"

LIMITS=(--cpus=1 --memory=256m --memory-swap=256m --ulimit nofile=65536:65536)
# Benchmark config (both implementations): a bigger relay range (500 rooms x
# 6 players need 3000 relays) and bandwidth limits high enough to never
# interfere. The idle and "limits" scenarios use the defaults instead.
BENCH_ENV=(
  -e NORAY_UDP_RELAY_PORTS=46000-50999
  -e NORAY_UDP_RELAY_MAX_INDIVIDUAL_TRAFFIC=1Gb
  -e NORAY_UDP_RELAY_MAX_GLOBAL_TRAFFIC=100Gb
  -e NORAY_UDP_RELAY_MAX_LIFETIME_TRAFFIC=1Pb
)

build() {
  docker build --load -q -t noray-go:bench . >/dev/null
  docker build --load -q -f bench/node/Dockerfile -t noray-node:bench "$NODE_SRC" >/dev/null
  docker build --load -q -f bench/loadgen/Dockerfile -t noray-loadgen:bench . >/dev/null
  docker network inspect $NET >/dev/null 2>&1 || docker network create $NET >/dev/null
}

srv_rm() { docker rm -f noray-srv >/dev/null 2>&1 || true; }

srv_start() { # impl, extra docker args...
  local impl=$1; shift
  srv_rm
  docker run -d --name noray-srv --network $NET "${LIMITS[@]}" "$@" noray-$impl:bench >/dev/null
  wait_ready
}

wait_ready() {
  loadgen probe -server noray-srv -timeout 60s >/dev/null
}

loadgen() {
  docker run --rm --network $NET --ulimit nofile=1048576:1048576 \
    -v /var/run/docker.sock:/var/run/docker.sock noray-loadgen:bench "$@"
}

idle() {
  for i in $(seq "$REPEATS"); do
    for impl in $IMPLS; do
      echo "== idle/startup $impl #$i" >&2
      srv_rm
      docker create --name noray-srv --network $NET "${LIMITS[@]}" noray-$impl:bench >/dev/null
      # probe starts first and polls every 1 ms until the TCP port accepts
      docker run -d --name noray-probe --network $NET -v /var/run/docker.sock:/var/run/docker.sock \
        noray-loadgen:bench probe -server noray-srv -container noray-srv -label $impl -scenario startup >/dev/null
      sleep 1
      docker start noray-srv >/dev/null
      docker wait noray-probe >/dev/null
      docker logs noray-probe >>"$OUT/startup.jsonl"
      docker rm -f noray-probe >/dev/null
      loadgen idle -container noray-srv -label $impl -scenario idle-default-config >>"$OUT/idle.jsonl"
    done
  done
}

hosts() {
  for n in ${HOSTS_N:-100 1000 5000 20000}; do
    for i in $(seq "$REPEATS"); do
      for impl in $IMPLS; do
        echo "== hosts $n $impl #$i" >&2
        srv_start $impl "${BENCH_ENV[@]}"
        loadgen hosts -server noray-srv -container noray-srv -n $n -label $impl -scenario hosts-$n >>"$OUT/hosts.jsonl"
      done
    done
  done
  for i in $(seq "$REPEATS"); do
    for impl in $IMPLS; do
      echo "== hosts baseline $impl #$i" >&2
      srv_start $impl "${BENCH_ENV[@]}"
      loadgen hosts -server noray-srv -container noray-srv -n 0 -label $impl -scenario hosts-0 >>"$OUT/hosts.jsonl"
    done
  done
}

relay() {
  for rooms in ${ROOMS:-10 100 500}; do
    for i in $(seq "$REPEATS"); do
      for impl in $IMPLS; do
        echo "== relay $rooms rooms $impl #$i" >&2
        srv_start $impl "${BENCH_ENV[@]}"
        loadgen relay -server noray-srv -container noray-srv -rooms $rooms -players 6 -rate 30 \
          -label $impl -scenario relay-${rooms}x6-30hz >>"$OUT/relay.jsonl"
      done
    done
  done
}

sweep() {
  for i in $(seq "$REPEATS"); do
    for impl in $IMPLS; do
      echo "== sweep $impl #$i" >&2
      srv_start $impl "${BENCH_ENV[@]}"
      loadgen relay -server noray-srv -container noray-srv -rooms 50 -players 6 \
        -sweep ${SWEEP_HZ:-20,40,60,80,100,120,140,160,200,240,320,400,480,560,640,800} -max-loss 0.01 \
        -warmup 2s -window 8s -label $impl -scenario sweep-50x6 >>"$OUT/sweep.jsonl"
    done
  done
}

limits() {
  for i in $(seq "$REPEATS"); do
    for impl in $IMPLS; do
      echo "== default limits $impl #$i" >&2
      srv_start $impl
      loadgen relay -server noray-srv -container noray-srv -rooms 10 -players 6 -rate 30 -window 10s \
        -metrics-port 8891 -label $impl -scenario limits-default-10x6-30hz-60-200B >>"$OUT/limits.jsonl"
      srv_start $impl
      loadgen relay -server noray-srv -container noray-srv -rooms 10 -players 6 -rate 30 -window 10s \
        -min-size 1000 -max-size 1200 -metrics-port 8891 \
        -label $impl -scenario limits-default-10x6-30hz-1000-1200B >>"$OUT/limits.jsonl" || true
      docker logs noray-srv 2>&1 | grep -m1 -E 'Bandwidth limit|AssertionError' >&2 || true
    done
  done
}

images() {
  for impl in node go; do
    printf '{"mode":"image","impl":"%s","scenario":"image","size_bytes":%s}\n' $impl \
      "$(docker image inspect noray-$impl:bench --format '{{.Size}}')"
  done >"$OUT/images.jsonl"
}

build
case $WHAT in
  all) images; idle; hosts; relay; sweep; limits ;;
  *) $WHAT ;;
esac
srv_rm
docker run --rm -v "$PWD/$OUT":/r noray-loadgen:bench report $(cd $OUT && ls *.jsonl | sed 's#^#/r/#') >"$OUT/summary.md"
cat "$OUT/summary.md"
