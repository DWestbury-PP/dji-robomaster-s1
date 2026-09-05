#!/usr/bin/env bash
# Build s1probe.
#
# Must be amd64: DJI never shipped an arm64 macOS build of the UnityBridge, so
# on Apple Silicon the binary runs under Rosetta 2 (DECISIONS.md #10, and
# docs/SPIKE-arm64-bridge.md for why the native route is deferred).
set -euo pipefail

cd "$(dirname "$0")/.."
mkdir -p bin

export CGO_ENABLED=1
export GOARCH=amd64

for cmd in s1probe s1teleop s1find s1capture s1bakeoff s1narrate; do
  go build -o "bin/${cmd}" "./cmd/${cmd}"
  echo "built bin/${cmd}"
  file "bin/${cmd}" | sed 's/^/  /'
done
