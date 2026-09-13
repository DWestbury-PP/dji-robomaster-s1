#!/usr/bin/env bash
# Start the whole stack: console, observer, detector.
#
# Three processes, because the bridge is amd64-under-Rosetta and owns the
# control loop while the detector wants native arm64 and Python
# (DECISIONS.md #20). They find each other over HTTP on :8700 — no broker,
# nothing to start first (#14).
#
#   ./scripts/start.sh                      drive one robot
#   ./scripts/start.sh -mock                no robot: synthetic video
#   S1_VEHICLES="Rover:123,Scout:456" \
#     ./scripts/start.sh                    two robots, switchable in the UI
#
# S1_VEHICLES is a comma-separated list of name[:appID]. With more than one
# vehicle each worker gets its own process (the DJI bridge is a process-wide
# singleton — DECISIONS.md #9) and the console becomes a supervisor over them.
#
# Give every vehicle a distinct appID. Without one, each worker connects to
# whichever robot answers its discovery first, so which name lands on which
# robot is a coin toss.
#
# Ctrl-C stops everything. Anything passed here goes to the vehicle worker(s).
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
names=()
logs=()
died=""

# Record a started process so a later death can be reported against its log.
track() { pids+=("$1"); names+=("$2"); logs+=("$3"); }

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
  if [[ -n "$died" ]]; then
    echo "$died stopped, so the rest were shut down. Its last words:"
    echo
    # The bridge aborts through ObjC on exit and dumps every goroutine and CPU
    # register, which buries the one line that says what went wrong. Cut the
    # dump at its first marker. awk, not sed: BSD sed has no \| alternation, so
    # a GNU-style pattern here silently matches nothing and prints the dump.
    awk '/^libc\+\+abi|^SIGABRT|^goroutine |^\[signal /{exit} {print}' \
      "$died_log" 2>/dev/null | grep -v '^$' | tail -8 | sed 's/^/  /'
    echo
    if grep -q "could not reach the robot" "$died_log" 2>/dev/null; then
      echo "  The robot is not answering. Check it is powered on and on the network:"
      echo "      ./bin/s1find"
      echo "  Or work without it:"
      echo "      ./scripts/start.sh -mock"
      echo
    fi
    echo "full logs in $RUNLOG/"
  else
    echo "stopped · logs in $RUNLOG/ · drives in logs/drives/"
  fi
}
trap cleanup INT TERM EXIT

# One vehicle is one process. Several vehicles are several processes plus a
# supervisor, because the DJI bridge handle is process-wide (DECISIONS.md #9).
if [[ -n "${S1_VEHICLES:-}" ]]; then
  IFS=',' read -r -a specs <<< "$S1_VEHICLES"
  port=8801
  worker_addrs=""
  missing_appid=0

  for spec in "${specs[@]}"; do
    spec="$(echo "$spec" | xargs)"
    [[ -z "$spec" ]] && continue
    vname="${spec%%:*}"
    vapp="${spec#*:}"
    [[ "$vapp" == "$spec" ]] && vapp=0
    [[ "$vapp" == "0" ]] && missing_appid=1

    vlog="$RUNLOG/vehicle-${vname}.log"
    printf '%-10s → %s   (127.0.0.1:%s)\n' "$vname" "$vlog" "$port"
    ./bin/s1teleop -addr "127.0.0.1:$port" -name "$vname" -appid "$vapp" "$@" >"$vlog" 2>&1 &
    track $! "$vname" "$vlog"

    worker_addrs="${worker_addrs:+$worker_addrs,}127.0.0.1:$port"
    port=$((port + 1))

    # Discovery binds one UDP port, so two workers searching at once collide.
    # They retry and recover, but staggering keeps startup ordered and quiet.
    sleep 2
  done

  if [[ $missing_appid -eq 1 && ${#specs[@]} -gt 1 ]]; then
    echo
    echo "  warning: a vehicle has no appID, so each worker takes whichever robot"
    echo "           answers first — names may land on the wrong vehicle."
    echo "           Give each one: S1_VEHICLES=\"Rover:123,Scout:456\""
  fi

  echo "supervisor → $RUNLOG/teleop.log"
  ./bin/s1teleop -addr localhost:8700 -workers "$worker_addrs" >"$RUNLOG/teleop.log" 2>&1 &
  track $! "supervisor" "$RUNLOG/teleop.log"
else
  echo "s1teleop   → $RUNLOG/teleop.log"
  ./bin/s1teleop "$@" >"$RUNLOG/teleop.log" 2>&1 &
  track $! "s1teleop" "$RUNLOG/teleop.log"
fi


# The console owns the frames; the other two poll it and will wait, so
# ordering does not matter beyond keeping the startup output readable.
if [[ $have_narrator -eq 1 ]]; then
  echo "s1narrate  → $RUNLOG/narrate.log"
  ./bin/s1narrate -v >"$RUNLOG/narrate.log" 2>&1 &
  track $! "s1narrate" "$RUNLOG/narrate.log"
fi

if [[ $have_detector -eq 1 ]]; then
  echo "detector   → $RUNLOG/detect.log"
  ( cd perception/detector && uv run detect.py -v ) >"$RUNLOG/detect.log" 2>&1 &
  track $! "detector" "$RUNLOG/detect.log"
  # Ours to kill, but not ours to have announced: without this bash prints a
  # "Terminated" job notice into the middle of the failure report.
  disown %% 2>/dev/null || true
fi

echo
echo "console: $CONSOLE     (Ctrl-C stops everything)"
echo

# Exit as soon as any one of them dies, rather than sitting on a half-up
# stack: a console that died silently looks exactly like a robot that is off.
#
# Polled rather than `wait -n`, which macOS's bash 3.2 does not have.
while :; do
  i=0
  while [[ $i -lt ${#pids[@]} ]]; do
    if ! alive "${pids[$i]}"; then
      died="${names[$i]}"
      died_log="${logs[$i]}"
      exit 1
    fi
    i=$((i + 1))
  done
  sleep 1
done
