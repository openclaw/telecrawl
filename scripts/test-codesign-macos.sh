#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin"
binary="$tmp/telecrawl"
touch "$binary"

cat > "$tmp/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' Darwin
EOF

cat > "$tmp/bin/codesign" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--display" ]]; then
  printf 'TeamIdentifier=%s\n' "${FAKE_TEAM_ID:-}" >&2
  exit 0
fi
printf '%s\n' "$*" >> "$CODESIGN_LOG"
EOF

chmod +x "$tmp/bin/uname" "$tmp/bin/codesign"
export PATH="$tmp/bin:$PATH"
export CODESIGN_LOG="$tmp/codesign.log"
unset TELECRAWL_CODESIGN_IDENTITY CODESIGN_IDENTITY

"$root/scripts/codesign-macos.sh" "$binary"
if [[ -e "$CODESIGN_LOG" ]]; then
  echo "codesign hook ran without an identity" >&2
  exit 1
fi

export TELECRAWL_CODESIGN_IDENTITY="Developer ID Application: OpenClaw Foundation (FWJYW4S8P8)"
export FAKE_TEAM_ID="FWJYW4S8P8"
"$root/scripts/codesign-macos.sh" "$binary"
grep -Fq -- "--options runtime --identifier ai.openclaw.telecrawl $binary" "$CODESIGN_LOG"
grep -Fq -- "--verify --strict --verbose=2 $binary" "$CODESIGN_LOG"

export FAKE_TEAM_ID="WRONGTEAM1"
if "$root/scripts/codesign-macos.sh" "$binary" 2> "$tmp/wrong-team.err"; then
  echo "codesign hook accepted the wrong Team ID" >&2
  exit 1
fi
grep -Fq "expected OpenClaw Foundation Team ID FWJYW4S8P8" "$tmp/wrong-team.err"
