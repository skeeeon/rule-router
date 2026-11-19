#!/usr/bin/env bash
# Macro benchmark: the real rule-router binary against a real NATS server.
#
#   bench/run.sh                         100k messages, as fast as possible
#   bench/run.sh -n 200000 -rate 20000   fixed offered rate (use this for latency)
#   bench/run.sh -pad 2048               bigger messages
#
# Turn knobs with env vars:
#   RR_NATS_CONSUMERS_WORKERCOUNT=8 bench/run.sh    any key in bench/config.yaml
#   RULES=path/to/rules bench/run.sh                 a different rule set
#   NATS_URL=nats://host:4222 bench/run.sh           an existing server (must have JetStream)
#
# Without NATS_URL it starts a throwaway server on port 4333: `nats-server`
# if it is on PATH, otherwise the nats Docker image.
set -euo pipefail
cd "$(dirname "$0")/.."

RULES=${RULES:-bench/rules}
WORK=$(mktemp -d)
NATS_PID=""
ROUTER_PID=""
DOCKER_NAME=""

cleanup() {
  [ -n "$ROUTER_PID" ] && kill "$ROUTER_PID" 2>/dev/null && wait "$ROUTER_PID" 2>/dev/null
  [ -n "$NATS_PID" ] && kill "$NATS_PID" 2>/dev/null && wait "$NATS_PID" 2>/dev/null
  [ -n "$DOCKER_NAME" ] && docker rm -f "$DOCKER_NAME" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "building..."
go build -o "$WORK/rule-router" ./cmd/rule-router
go build -o "$WORK/bench" ./bench

if [ -z "${NATS_URL:-}" ]; then
  NATS_URL=nats://127.0.0.1:4333
  if command -v nats-server >/dev/null; then
    nats-server -js -p 4333 -sd "$WORK/js" >"$WORK/nats.log" 2>&1 &
    NATS_PID=$!
  else
    DOCKER_NAME=rr-bench-nats
    docker run -d --rm --name "$DOCKER_NAME" -p 4333:4222 nats:2.11-alpine -js >/dev/null
  fi
fi

# Streams must exist before the router starts: it validates its subjects
# against them at startup. Retrying also waits out NATS starting up.
for i in $(seq 20); do
  "$WORK/bench" -url "$NATS_URL" setup 2>/dev/null && break
  [ "$i" = 20 ] && { "$WORK/bench" -url "$NATS_URL" setup; exit 1; }
  sleep 0.5
done

RR_NATS_URLS="$NATS_URL" "$WORK/rule-router" --config bench/config.yaml --rules "$RULES" \
  >"$WORK/router.log" 2>&1 &
ROUTER_PID=$!

if ! "$WORK/bench" -url "$NATS_URL" "$@" run; then
  echo "--- last 20 lines of router log ---"
  tail -n 20 "$WORK/router.log"
  exit 1
fi
