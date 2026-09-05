#!/usr/bin/env bash
# Install DJI's UnityBridge library where the loader looks for it (~/.unitybridge).
#
# The blob ships inside the brunoga/robomaster module, so we copy it out of the
# Go module cache rather than downloading anything. It is proprietary DJI code:
# we neither commit it to this repo nor redistribute it.
set -euo pipefail

DEST="${HOME}/.unitybridge"
MODCACHE="$(go env GOMODCACHE)"
MODULE="github.com/brunoga/robomaster"

VERSION="$(go list -m -f '{{.Version}}' "${MODULE}")"
SRC="${MODCACHE}/${MODULE}@${VERSION}/unitybridge/wrapper/lib/darwin/amd64/unitybridge.bundle/Contents/MacOS"

if [[ ! -d "${SRC}" ]]; then
  echo "error: bridge library not found at:" >&2
  echo "  ${SRC}" >&2
  echo "Run 'go mod download' first." >&2
  exit 1
fi

mkdir -p "${DEST}"
cp -f "${SRC}"/* "${DEST}/"
chmod u+rw "${DEST}"/* 2>/dev/null || true

echo "installed ${MODULE}@${VERSION} bridge -> ${DEST}"
ls -lh "${DEST}" | tail -n +2 | awk '{printf "  %-24s %s\n", $9, $5}'
