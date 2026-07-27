#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
release_local="$root/scripts/release-local"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

expect_refusal() {
  local label=$1
  shift
  if "$release_local" "$@" >"$tmp/$label.stdout" 2>"$tmp/$label.stderr"; then
    echo "release contract test: $label unexpectedly succeeded" >&2
    exit 1
  fi
  grep -Fq 'local releases are disabled' "$tmp/$label.stderr"
  grep -Fq 'gh workflow run release-unified.yml --repo openclaw/telecrawl -f version=X.Y.Z' \
    "$tmp/$label.stderr"
}

expect_refusal pilot pilot v0.3.5
expect_refusal draft draft
expect_refusal verify verify-draft v0.3.5
expect_refusal publish publish v0.3.5
expect_refusal homebrew homebrew v0.3.5

grep -Fq 'release-artifacts: release' "$root/Makefile"
for target in release-pilot release-draft verify-release release release-homebrew; do
  grep -Eq "^${target}:" "$root/Makefile"
done

echo "unified local release refusal tests passed"
