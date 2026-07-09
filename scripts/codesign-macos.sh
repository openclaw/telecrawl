#!/usr/bin/env bash
set -euo pipefail

binary="${1:-}"
if [[ -z "$binary" ]]; then
  echo "usage: $0 <path-to-binary>" >&2
  exit 2
fi

if [[ "$(uname -s)" != "Darwin" ]]; then
  exit 0
fi

identity="${TELECRAWL_CODESIGN_IDENTITY:-${CODESIGN_IDENTITY:-}}"
if [[ -z "$identity" ]]; then
  echo "codesign: no managed identity configured; leaving $binary unsigned" >&2
  exit 0
fi

codesign \
  --force \
  --sign "$identity" \
  --timestamp \
  --options runtime \
  --identifier ai.openclaw.telecrawl \
  "$binary"

details="$(codesign --display --verbose=4 "$binary" 2>&1)"
team_id="$(awk -F= '$1 == "TeamIdentifier" { print $2; exit }' <<<"$details")"
if [[ "$team_id" != "FWJYW4S8P8" ]]; then
  echo "codesign: expected OpenClaw Foundation Team ID FWJYW4S8P8, got ${team_id:-none}" >&2
  exit 1
fi

codesign --verify --strict --verbose=2 "$binary"
