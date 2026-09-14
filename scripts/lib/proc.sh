#!/usr/bin/env bash
# Shutdown semantics, shared by start.sh and stop.sh.
#
# These live in one file because the two scripts have to agree. However the
# stack goes down — Ctrl-C in the terminal that started it, or stop.sh against
# a stack whose terminal is long gone — it must go down the same way. Two
# copies of this would drift, and the way you find out they drifted is a
# detector still holding the GPU an hour later.
#
# Sourced, never run. No `set` here: that belongs to the caller.

# Is this pid running? Not `kill -0`: an exited child we have not reaped is a
# zombie, and `kill -0` reports a zombie as alive — which is exactly the death
# we care about.
alive() {
  case "$(ps -o state= -p "$1" 2>/dev/null)" in
    ''|Z*) return 1 ;;
    *)     return 0 ;;
  esac
}

# Signal a process and anything it spawned. `uv run` execs the interpreter as
# a child, so the detector is a generation deeper than the pid we recorded;
# signalling the pid alone leaves a Python process holding the GPU and polling
# a console that is gone.
kill_tree() {
  local sig="$1" pid="$2"
  pkill -"$sig" -P "$pid" 2>/dev/null || true
  kill -"$sig" "$pid" 2>/dev/null || true
}

# TERM every pid given, wait up to $1 seconds for them to go, then KILL
# whatever ignored it. Bounded rather than a bare `wait`: a child that ignores
# TERM must not leave the operator with a terminal that will not come back.
stop_pids() {
  local grace="$1"; shift
  local pid waited=0 live

  for pid in "$@"; do kill_tree TERM "$pid"; done

  while [[ $waited -lt $grace ]]; do
    live=0
    for pid in "$@"; do alive "$pid" && live=1; done
    [[ $live -eq 0 ]] && return 0
    sleep 1
    waited=$((waited + 1))
  done

  for pid in "$@"; do
    alive "$pid" && kill_tree KILL "$pid"
  done
  return 0
}
