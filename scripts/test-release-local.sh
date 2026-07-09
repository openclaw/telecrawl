#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin"
export RELEASE_TEST_LOG="$tmp/release.log"

cat >"$tmp/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' Darwin
EOF

cat >"$tmp/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

tag_object=1111111111111111111111111111111111111111
head_commit=2222222222222222222222222222222222222222
mismatch=3333333333333333333333333333333333333333
command="$1"
shift

case "$command" in
  status)
    ;;
  branch)
    printf '%s\n' main
    ;;
  describe)
    printf '%s\n' v0.3.2
    ;;
  cat-file)
    printf '%s\n' tag
    ;;
  verify-tag)
    ;;
  rev-parse)
    case "$1" in
      HEAD | *'^{}') printf '%s\n' "$head_commit" ;;
      refs/tags/*) printf '%s\n' "$tag_object" ;;
      *) exit 1 ;;
    esac
    ;;
  ls-remote)
    case "${RELEASE_TEST_MODE:-exact}" in
      missing)
        exit 2
        ;;
      object-mismatch)
        printf '%s\t%s\n' "$mismatch" refs/tags/v0.3.2
        printf '%s\t%s\n' "$head_commit" 'refs/tags/v0.3.2^{}'
        ;;
      commit-mismatch)
        printf '%s\t%s\n' "$tag_object" refs/tags/v0.3.2
        printf '%s\t%s\n' "$mismatch" 'refs/tags/v0.3.2^{}'
        ;;
      exact)
        printf '%s\t%s\n' "$tag_object" refs/tags/v0.3.2
        printf '%s\t%s\n' "$head_commit" 'refs/tags/v0.3.2^{}'
        ;;
    esac
    ;;
  *)
    exit 1
    ;;
esac
EOF

cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "auth" ]]; then
  printf '%s\n' test-token
elif [[ "${1:-} ${2:-}" == "release edit" ]]; then
  printf 'gh %s\n' "$*" >>"$RELEASE_TEST_LOG"
  while [[ "$#" -gt 0 ]]; do
    if [[ "$1" == "--notes-file" ]]; then
      cp "$2" "$RELEASE_TEST_LOG.notes"
      break
    fi
    shift
  done
elif [[ "${1:-}" == "api" ]]; then
  cat "$RELEASE_TEST_LOG.notes"
elif [[ "${1:-} ${2:-}" == "run list" ]]; then
  printf '%s\n' 42
else
  printf 'gh %s\n' "$*" >>"$RELEASE_TEST_LOG"
fi
EOF

cat >"$tmp/bin/goreleaser" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat >"$tmp/bin/release-helper" <<'EOF'
#!/usr/bin/env bash
printf 'release-helper %s\n' "$*" >>"$RELEASE_TEST_LOG"
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" == "--release-notes" ]]; then
    printf '%s\n' release-notes: >>"$RELEASE_TEST_LOG"
    cat "$2" >>"$RELEASE_TEST_LOG"
    break
  fi
  shift
done
EOF

chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"
export MAC_RELEASE_HELPER="$tmp/bin/release-helper"
unset GITHUB_TOKEN

for mode in missing object-mismatch commit-mismatch; do
  : >"$RELEASE_TEST_LOG"
  if RELEASE_TEST_MODE="$mode" "$root/scripts/release-local" >"$tmp/$mode.out" 2>"$tmp/$mode.err"; then
    echo "release-local accepted remote tag mode: $mode" >&2
    exit 1
  fi
  if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
    echo "release-local published after remote tag failure: $mode" >&2
    exit 1
  fi
done

: >"$RELEASE_TEST_LOG"
RELEASE_TEST_MODE=exact "$root/scripts/release-local"
grep -Eq 'release-helper codesign-run -- goreleaser release --clean --parallelism=2 --release-notes /tmp/telecrawl-v0\.3\.2-notes\.' "$RELEASE_TEST_LOG"
grep -Fq 'Update CrawlKit to the signed v0.13.4 release.' "$RELEASE_TEST_LOG"
grep -Fq 'Full changelog: https://github.com/openclaw/telecrawl/blob/v0.3.2/CHANGELOG.md' "$RELEASE_TEST_LOG"
grep -Fq 'gh release edit v0.3.2 --repo openclaw/telecrawl --notes-file' "$RELEASE_TEST_LOG"
grep -Fq 'gh workflow run update-formula.yml' "$RELEASE_TEST_LOG"
grep -Fq 'gh run watch 42' "$RELEASE_TEST_LOG"
