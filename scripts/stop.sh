#!/usr/bin/env bash
# Stop everything this checkout started: the console, the vehicle workers, the
# observers, the detectors, and any probe still holding a port.
#
#   ./scripts/stop.sh          stop it all, and say what went
#   ./scripts/stop.sh -n       say what would go, touch nothing
#
# Ctrl-C in the start.sh terminal already does this, and does it properly.
# This is for when there is no such terminal: a stack started from a window
# you have since closed, a `kill` that took the console and orphaned the six
# processes around it, or yesterday's run still politely polling a robot that
# has been switched off since.
#
# Scoped to this checkout. A process counts as ours only if it is running from
# this directory, so a second clone of the repo — or another session's work on
# the same Mac — is never touched.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
source scripts/lib/proc.sh

dry=0
for a in "$@"; do
  case "$a" in
    -n|--dry-run) dry=1 ;;
    -h|--help)    sed -n '2,17p' "$0" | sed 's/^#\{1,\} \{0,1\}//'; exit 0 ;;
    *)            echo "stop.sh: unknown option $a" >&2; exit 2 ;;
  esac
done

# Which directory is a process running from? lsof rather than ps, because BSD
# ps will not report another process's cwd.
cwd_of() {
  lsof -a -d cwd -Fn -p "$1" 2>/dev/null | sed -n 's/^n//p' | head -1
}

# Ours only if it is running from this checkout. This is the whole of the
# scoping rule, and it is deliberately stricter than matching on process name:
# `pkill -f s1teleop` would reach into any other clone on the machine.
ours() {
  local c
  c="$(cwd_of "$1")"
  [[ -n "$c" && ( "$c" == "$ROOT" || "$c" == "$ROOT"/* ) ]]
}

# A name an operator recognises, read back out of the process's own arguments,
# so the report matches what start.sh printed when it launched them.
label_for() {
  local args="$1"
  case "$args" in
    *scripts/start.sh*)  echo "start.sh" ;;
    *-workers*)          echo "console (supervisor)" ;;
    *bin/s1teleop*)
      if [[ "$args" =~ -name[[:space:]]+([^[:space:]]+) ]]; then
        echo "vehicle ${BASH_REMATCH[1]}"
      else
        echo "console"
      fi ;;
    *bin/s1narrate*)     echo "observer -> ${args##*-console http://}" ;;
    *detect.py*)         echo "detector -> ${args##*--console http://}" ;;
    *bin/s1tof*)         echo "s1tof probe" ;;
    *bin/s1find*)        echo "s1find" ;;
    *bin/s1probe*)       echo "s1probe" ;;
    *bin/s1capture*)     echo "s1capture" ;;
    *bin/s1bakeoff*)     echo "s1bakeoff" ;;
    *)                   echo "${args%% *}" ;;
  esac
}

# Everything start.sh can launch, plus the hand-run probes that bind the same
# ports and cause the same confusing collision.
PATTERN='bin/s1(teleop|narrate|find|tof|probe|capture|bakeoff)|detect\.py'
OWNER='scripts/start\.sh'

# Candidate pids matching PATTERN and running from here, newest last so the
# report reads in roughly the order they were started.
find_ours() {
  local pid
  for pid in $(pgrep -f "$1" 2>/dev/null | sort -n); do
    [[ "$pid" == "$$" ]] && continue
    ours "$pid" && echo "$pid"
  done
}

# A forked subshell keeps its parent's arguments, so start.sh's own detector
# subshells look exactly like start.sh to pgrep. Keep only the pids whose
# parent is not itself in the list: those are the real scripts, and signalling
# one runs its cleanup trap over the subshells anyway.
roots_only() {
  local pid ppid
  for pid in "$@"; do
    ppid="$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d ' ')"
    case " $* " in
      *" $ppid "*) continue ;;
    esac
    echo "$pid"
  done
}

report() {
  local pid args
  for pid in "$@"; do
    args="$(ps -o args= -p "$pid" 2>/dev/null || true)"
    [[ -z "$args" ]] && continue
    printf '  %-28s pid %s\n' "$(label_for "$args")" "$pid"
  done
}

# The owner first, if one is still running. start.sh has a cleanup trap that
# stops its own children properly and prints why they stopped, so let it do
# that rather than cutting its legs off and sweeping up underneath.
supervisors=($(find_ours "$OWNER" || true))
supervisors=($(roots_only ${supervisors[@]+"${supervisors[@]}"}))
children=($(find_ours "$PATTERN" || true))

if [[ ${#supervisors[@]} -eq 0 && ${#children[@]} -eq 0 ]]; then
  echo "nothing running from this checkout."
else
  echo "stopping:"
  report ${supervisors[@]+"${supervisors[@]}"} ${children[@]+"${children[@]}"}
fi

if [[ $dry -eq 1 ]]; then
  [[ ${#supervisors[@]} -gt 0 || ${#children[@]} -gt 0 ]] && echo && echo "(dry run — nothing was signalled)"
  exit 0
fi

if [[ ${#supervisors[@]} -gt 0 ]]; then
  # Longer grace than the sweep: start.sh's own cleanup takes up to five
  # seconds before it escalates, and interrupting that would defeat the point
  # of asking it nicely.
  stop_pids 8 "${supervisors[@]}"
fi

# Then whatever is left — orphans, and stacks started by hand rather than by
# start.sh. Re-read the list: the owner will already have taken most of these
# with it, and signalling a pid that has since been reused is worth avoiding.
remaining=($(find_ours "$PATTERN|$OWNER" || true))
if [[ ${#remaining[@]} -gt 0 ]]; then
  stop_pids 5 "${remaining[@]}"
fi

# Did it actually work? The point of this script is that `start.sh` will run
# afterwards, so check the thing that would stop it.
stubborn=($(find_ours "$PATTERN|$OWNER" || true))
if [[ ${#stubborn[@]} -gt 0 ]]; then
  echo
  echo "still running after KILL — this should not happen:" >&2
  report "${stubborn[@]}" >&2
  exit 1
fi

# A console port held by something that is *not* ours is the other way
# start.sh fails, and it is the one a process sweep cannot fix. Name it rather
# than leaving the operator to rediscover it.
holder="$(lsof -ti :8700 -sTCP:LISTEN 2>/dev/null | head -1 || true)"
if [[ -n "$holder" ]]; then
  echo
  echo "note: port 8700 is still held by something outside this checkout:"
  printf '  %s\n' "$(ps -o pid=,args= -p "$holder" 2>/dev/null | sed 's/^ *//')"
  echo
  echo "  start.sh will refuse to start until that is gone. If it is yours:"
  echo "      kill $holder"
  exit 1
fi

if [[ ${#supervisors[@]} -gt 0 || ${#children[@]} -gt 0 ]]; then
  echo
  echo "stopped · logs kept in logs/run/ · drives in logs/drives/"
fi
