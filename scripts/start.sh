#!/usr/bin/env bash
# Start the whole stack: console, observer, detector.
#
# Three processes, because the bridge is amd64-under-Rosetta and owns the
# control loop while the detector wants native arm64 and Python
# (DECISIONS.md #20). They find each other over HTTP on :8700 — no broker,
# nothing to start first (#14).
#
#   ./scripts/start.sh           drive the robot
#   ./scripts/start.sh -mock     no robot: synthetic video, sink that discards
#
# Ctrl-C stops all three. Anything passed here goes to s1teleop.
set -euo pipefail

cd "$(dirname "$0")/.."

# The banner must name the port actually in use: -addr is passed straight
# through to s1teleop, and a URL that lies is worse than no URL.
addr="localhost:8700"
prev=""
for a in "$@"; do
  [[ "$prev" == "-addr" || "$prev" == "--addr" ]] && addr="$a"
  case "$a" in -addr=*|--addr=*) addr="${a#*=}" ;; esac
  prev="$a"
done
CONSOLE="http://${addr}"
RUNLOG="logs/run"
mkdir -p "$RUNLOG"

[[ -x bin/s1teleop ]] || { echo "bin/s1teleop missing — run ./scripts/build.sh first" >&2; exit 1; }

# A console is already up. Worth its own message: the second instance fails to
# bind and dies, but the browser still answers on :8700 from the first one, so
# the stack looks healthy while half of it is not the half you just started.
if [[ "$*" != *-addr* ]] && lsof -ti :8700 -sTCP:LISTEN >/dev/null 2>&1; then
  echo "port 8700 is already listening — a console is running." >&2
  echo "stop it first:  kill \$(lsof -ti :8700 -sTCP:LISTEN)" >&2
  exit 1
fi

# Preflight. Warn, never block: the console alone is worth running, and a
# missing model should not stop you driving.
have_detector=1
have_narrator=1
command -v uv >/dev/null || { echo "note: uv not found — skipping the detector (boxes)"; have_detector=0; }
curl -sf --max-time 2 http://localhost:11434/api/tags >/dev/null 2>&1 \
  || { echo "note: Ollama not answering on :11434 — skipping the observer (captions)"; have_narrator=0; }

# Liveness. Not `kill -0`: an exited child we have not reaped is a zombie, and
# `kill -0` reports a zombie as alive — which is exactly the death we care about.
alive() {
  case "$(ps -o state= -p "$1" 2>/dev/null)" in
    ''|Z*) return 1 ;;
    *)     return 0 ;;
  esac
}

pids=()

# Kill a child and anything it spawned. `uv run` execs the interpreter as a
# grandchild, so killing the child alone leaves a detector holding the GPU and
# polling a console that is gone.
kill_tree() {
  local pid=$1
  pkill -TERM -P "$pid" 2>/dev/null || true
  kill -TERM "$pid" 2>/dev/null || true
}

cleanup() {
  trap - INT TERM EXIT
  local pid
  for pid in ${pids[@]+"${pids[@]}"}; do
    kill_tree "$pid"
  done
  # Bounded, not a bare `wait`: a child that ignores TERM must not leave the
  # operator with a terminal that will not come back.
  local waited=0
  while [[ $waited -lt 5 ]]; do
    local live=0
    for pid in ${pids[@]+"${pids[@]}"}; do
      alive "$pid" && live=1
    done
    [[ $live -eq 0 ]] && break
    sleep 1
    waited=$((waited + 1))
  done
  for pid in ${pids[@]+"${pids[@]}"}; do
    alive "$pid" && { pkill -KILL -P "$pid" 2>/dev/null; kill -KILL "$pid" 2>/dev/null; } || true
  done
  echo
  echo "stopped · logs in $RUNLOG/ · drives in logs/drives/"
}
trap cleanup INT TERM EXIT

echo "s1teleop   → $RUNLOG/teleop.log"
./bin/s1teleop "$@" >"$RUNLOG/teleop.log" 2>&1 &
pids+=($!)

# The console owns the frames; the other two poll it and will wait, so
# ordering does not matter beyond keeping the startup output readable.
if [[ $have_narrator -eq 1 ]]; then
  echo "s1narrate  → $RUNLOG/narrate.log"
  ./bin/s1narrate -v >"$RUNLOG/narrate.log" 2>&1 &
  pids+=($!)
fi

if [[ $have_detector -eq 1 ]]; then
  echo "detector   → $RUNLOG/detect.log"
  ( cd perception/detector && uv run detect.py -v ) >"$RUNLOG/detect.log" 2>&1 &
  pids+=($!)
fi

echo
echo "console: $CONSOLE     (Ctrl-C stops everything)"
echo

# Exit as soon as any one of them dies, rather than sitting on a half-up
# stack: a console that died silently looks exactly like a robot that is off.
#
# Polled rather than `wait -n`, which macOS's bash 3.2 does not have.
while :; do
  for pid in "${pids[@]}"; do
    alive "$pid" || exit 1
  done
  sleep 1
done
