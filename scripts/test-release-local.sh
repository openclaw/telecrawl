#!/usr/bin/env bash
# shellcheck disable=SC2016 # Literal source-text contract assertions.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
real_git=$(command -v git)
real_go=$(command -v go)
mkdir -p "$tmp/bin"
export RELEASE_TEST_LOG="$tmp/release.log"
export RELEASE_TEST_ROOT="$root"
export RELEASE_TEST_TAGGED_CHANGELOG="$tmp/tagged-CHANGELOG.md"
export RELEASE_TEST_GRAFT_PATH="$tmp/active-grafts"
export RELEASE_TEST_MANIFEST="$tmp/.mac-release.env"
export MAC_RELEASE_MANIFEST="$RELEASE_TEST_MANIFEST"
: > "$RELEASE_TEST_MANIFEST"
cat > "$RELEASE_TEST_TAGGED_CHANGELOG" <<'EOF'
# Changelog

## [0.3.4] - 2026-07-09

### Changed

- Tagged release notes sentinel; mutable worktree notes must not be used.

## [0.3.3] - 2026-07-09
EOF

# Git can hide both untracked Go files and tracked skip-worktree changes from
# ordinary porcelain output. Prove the bypass and the fresh-clone isolation
# that the official producer relies on.
config_repo="$tmp/status-config-repo"
fresh_repo="$tmp/status-fresh-clone"
"$real_git" init --quiet "$config_repo"
"$real_git" -C "$config_repo" config user.name release-test
"$real_git" -C "$config_repo" config user.email release-test@example.com
printf '%s\n' 'module example.com/release-test' 'go 1.26.5' > "$config_repo/go.mod"
printf '%s\n' 'package main' 'const buildSource = "trusted-commit"' 'func main() {}' > "$config_repo/main.go"
"$real_git" -C "$config_repo" add go.mod main.go
"$real_git" -C "$config_repo" -c commit.gpgsign=false commit --quiet -m initial
cat > "$tmp/fake-ssh-keygen" <<'EOF'
#!/usr/bin/env bash
: > "${RELEASE_TEST_FAKE_SSH_SENTINEL:?}"
printf '%s\n' 'Good "git" signature for steipete@gmail.com with ED25519 key SHA256:WmI9lVtd7F2c5XyRHbZVO3yYYJzwsSNzcZQMPT147HI' >&2 # gitleaks:allow -- public release-key fingerprint
EOF
chmod +x "$tmp/fake-ssh-keygen"
"$real_git" -C "$config_repo" config gpg.ssh.program "$tmp/fake-ssh-keygen"
configured_ssh_program=$("$real_git" -C "$config_repo" config --get gpg.ssh.program)
RELEASE_TEST_FAKE_SSH_SENTINEL="$tmp/fake-ssh-keygen-ran" "$configured_ssh_program"
[[ -e "$tmp/fake-ssh-keygen-ran" ]] || {
  echo "repo-local fake SSH verifier regression did not reproduce" >&2
  exit 1
}
rm "$tmp/fake-ssh-keygen-ran"
[[ "$("$real_git" -C "$config_repo" \
  -c gpg.ssh.program=/usr/bin/ssh-keygen config --get gpg.ssh.program)" == /usr/bin/ssh-keygen ]] || {
  echo "command-line SSH verifier pin did not override repo-local config" >&2
  exit 1
}
"$real_git" -C "$config_repo" config status.showUntrackedFiles no
printf '%s\n' 'package main' 'const injected = true' > "$config_repo/injected.go"
[[ -z "$("$real_git" -C "$config_repo" status --porcelain)" ]] || {
  echo "Git untracked-file hiding regression did not reproduce" >&2
  exit 1
}
[[ "$("$real_git" -C "$config_repo" status --porcelain --untracked-files=all)" == '?? injected.go' ]] || {
  echo "explicit untracked-file inventory did not override local Git config" >&2
  exit 1
}
"$real_go" -C "$config_repo" list -f '{{join .GoFiles " "}}' | grep -Fq 'injected.go'
rm "$config_repo/injected.go"
"$real_git" -C "$config_repo" update-index --skip-worktree main.go
printf '%s\n' 'package main' 'const buildSource = "hidden-worktree"' 'func main() {}' > "$config_repo/main.go"
[[ -z "$("$real_git" -C "$config_repo" status --porcelain --untracked-files=all)" ]] || {
  echo "Git skip-worktree hiding regression did not reproduce" >&2
  exit 1
}
"$real_git" clone --quiet --no-checkout --no-local "$config_repo" "$fresh_repo"
"$real_git" -C "$fresh_repo" checkout --quiet --detach HEAD
grep -Fq 'trusted-commit' "$fresh_repo/main.go"
if grep -Fq 'hidden-worktree' "$fresh_repo/main.go"; then
  echo "fresh clone inherited hidden mutable working-tree bytes" >&2
  exit 1
fi
trusted_commit=$("$real_git" -C "$config_repo" rev-parse HEAD)
tree=$("$real_git" -C "$config_repo" write-tree)
unrelated_commit=$(printf '%s\n' 'unrelated root' | "$real_git" -C "$config_repo" commit-tree "$tree")
if "$real_git" -C "$config_repo" merge-base --is-ancestor "$trusted_commit" "$unrelated_commit"; then
  echo "synthetic commits unexpectedly had trusted ancestry before graft injection" >&2
  exit 1
fi
graft_path=$("$real_git" -C "$config_repo" rev-parse --git-path info/grafts)
[[ "$graft_path" == /* ]] || graft_path="$config_repo/$graft_path"
mkdir -p "$(dirname "$graft_path")"
printf '%s %s\n' "$unrelated_commit" "$trusted_commit" > "$graft_path"
if ! env GIT_NO_REPLACE_OBJECTS=1 "$real_git" -C "$config_repo" \
  merge-base --is-ancestor "$trusted_commit" "$unrelated_commit" 2>/dev/null; then
  echo "legacy Git graft did not override ancestry with replacements disabled" >&2
  exit 1
fi

cat > "$tmp/bin/uname" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == -m ]]; then
  printf '%s\n' arm64
else
  printf '%s\n' Darwin
fi
EOF

cat > "$tmp/bin/go" <<'EOF'
#!/usr/bin/env bash
[[ "${1:-}" == version ]] || exit 2
printf '%s\n' 'go version go1.26.5 darwin/arm64'
EOF

cat > "$tmp/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
tag_object=1111111111111111111111111111111111111111
head_commit=2222222222222222222222222222222222222222
mismatch=3333333333333333333333333333333333333333
tag_commit=$head_commit
[[ "${RELEASE_TEST_MODE:-exact}" != off-default-tag ]] || tag_commit=$mismatch
git_dir=
if [[ "${1:-}" == -C ]]; then
  git_dir=$2
  shift 2
fi
seen_format=false
seen_allowed_signers=false
seen_ssh_program=false
while [[ "${1:-}" == -c ]]; do
  case "${2:-}" in
    gpg.format=ssh) seen_format=true ;;
    gpg.ssh.program=/usr/bin/ssh-keygen) seen_ssh_program=true ;;
    gpg.ssh.allowedSignersFile="$RELEASE_TEST_ROOT/.github/release-allowed-signers") seen_allowed_signers=true ;;
  esac
  shift 2
done
command=$1
shift
case "$command" in
  status)
    if [[ -z "$git_dir" && "${RELEASE_TEST_MODE:-exact}" == hidden-untracked-config &&
      " ${*} " == *' --untracked-files=all '* ]]; then
      printf '%s\n' '?? injected.go'
    fi
    ;;
  branch) printf '%s\n' main ;;
  describe) printf '%s\n' v0.3.4 ;;
  clone)
    destination=${!#}
    mkdir -p "$destination"
    touch "$destination/.release-test-fresh-clone"
    printf 'git clone %s\n' "$*" >> "$RELEASE_TEST_LOG"
    ;;
  checkout) ;;
  for-each-ref)
    if [[ -z "$git_dir" && "${RELEASE_TEST_MODE:-exact}" == replacement-ref ]]; then
      printf '%s\n' refs/replace/2222222222222222222222222222222222222222
    fi
    ;;
  ls-files)
    if [[ -z "$git_dir" && "${RELEASE_TEST_MODE:-exact}" == hidden-index ]]; then
      printf '%s\n' 'S injected.go'
    fi
    ;;
  cat-file)
    case "${1:-} ${2:-}" in
      '-t FETCH_HEAD') printf '%s\n' tag ;;
      'tag FETCH_HEAD')
        printf 'object %s\ntype commit\ntag v0.3.4\n\nmock tag\n' "$tag_commit"
        ;;
      *) exit 1 ;;
    esac
    ;;
  verify-tag)
    [[ "$seen_format" == true && "$seen_ssh_program" == true &&
      "$seen_allowed_signers" == true ]] || exit 1
    if [[ "${RELEASE_TEST_MODE:-exact}" == untrusted-global-signer ]]; then
      printf '%s\n' 'Good "git" signature for attacker@example.com with ED25519 key SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' >&2
    else
      printf '%s\n' 'Good "git" signature for steipete@gmail.com with ED25519 key SHA256:WmI9lVtd7F2c5XyRHbZVO3yYYJzwsSNzcZQMPT147HI' >&2 # gitleaks:allow -- public release-key fingerprint
    fi
    ;;
  rev-parse)
    case "$1" in
      --git-path)
        [[ "${2:-}" == info/grafts ]] || exit 1
        printf '%s\n' "$RELEASE_TEST_GRAFT_PATH"
        ;;
      HEAD) printf '%s\n' "$head_commit" ;;
      'FETCH_HEAD^{}') printf '%s\n' "$tag_commit" ;;
      FETCH_HEAD) printf '%s\n' "$tag_object" ;;
      refs/tags/*'^{}') printf '%s\n' "$tag_commit" ;;
      refs/tags/*) printf '%s\n' "$tag_object" ;;
      refs/remotes/origin/main)
        if [[ "${RELEASE_TEST_MODE:-exact}" == off-default ]]; then
          printf '%s\n' "$mismatch"
        else
          printf '%s\n' "$head_commit"
        fi
        ;;
      *) exit 1 ;;
    esac
    ;;
  fetch) ;;
  show)
    case "${1:-}" in
      *:.github/release-allowed-signers) cat "$RELEASE_TEST_ROOT/.github/release-allowed-signers" ;;
      *:CHANGELOG.md) cat "$RELEASE_TEST_TAGGED_CHANGELOG" ;;
      *) exit 1 ;;
    esac
    ;;
  merge-base)
    [[ "${RELEASE_TEST_MODE:-exact}" != off-default-tag ]]
    ;;
  ls-remote)
    if [[ " ${*} " == *' --symref origin HEAD '* ]]; then
      printf 'ref: refs/heads/main\tHEAD\n'
      exit 0
    fi
    if [[ " ${*} " == *' origin refs/heads/main '* ]]; then
      if [[ "${RELEASE_TEST_MODE:-exact}" == off-default ]]; then
        printf '%s\t%s\n' "$mismatch" refs/heads/main
      else
        printf '%s\t%s\n' "$head_commit" refs/heads/main
      fi
      exit 0
    fi
    case "${RELEASE_TEST_MODE:-exact}" in
      missing) exit 2 ;;
      object-mismatch)
        printf '%s\t%s\n' "$mismatch" refs/tags/v0.3.4
        printf '%s\t%s\n' "$head_commit" 'refs/tags/v0.3.4^{}'
        ;;
      commit-mismatch)
        printf '%s\t%s\n' "$tag_object" refs/tags/v0.3.4
        printf '%s\t%s\n' "$mismatch" 'refs/tags/v0.3.4^{}'
        ;;
      exact|untrusted-global-signer|off-default|off-default-tag|unprotected-default|default-api-mismatch)
        printf '%s\t%s\n' "$tag_object" refs/tags/v0.3.4
        printf '%s\t%s\n' "$tag_commit" 'refs/tags/v0.3.4^{}'
        ;;
    esac
    ;;
  *) exit 1 ;;
esac
EOF

cat > "$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-} ${2:-}" == 'auth token' ]]; then
  [[ "${3:-} ${4:-}" == '--hostname github.com' && "$#" -eq 4 ]] || exit 90
  printf '%s\n' test-token
  exit 0
fi
if [[ "${1:-}" == api ]]; then
  shift
  [[ "${1:-} ${2:-}" == '--hostname github.com' ]] || exit 90
  shift 2
  [[ "${1:-}" != --paginate ]] || shift
  if [[ "${1:-}" == 'repos/openclaw/telecrawl' ]]; then
    jq -n '{default_branch:"main"}'
    exit 0
  fi
  if [[ "${1:-}" == 'repos/openclaw/telecrawl/branches/main' ]]; then
    protected=true
    commit=2222222222222222222222222222222222222222
    [[ "${RELEASE_TEST_MODE:-exact}" != unprotected-default ]] || protected=false
    [[ "${RELEASE_TEST_MODE:-exact}" != default-api-mismatch ]] || \
      commit=3333333333333333333333333333333333333333
    jq -n --argjson protected "$protected" --arg commit "$commit" \
      '{protected:$protected,commit:{sha:$commit}}'
    exit 0
  fi
  if [[ "${1:-}" == 'repos/openclaw/telecrawl/releases?per_page=100' ]]; then
    jq -n --rawfile body "$RELEASE_TEST_LOG.notes" '[{
      id: 42,
      tag_name: "v0.3.4",
      name: "v0.3.4",
      target_commitish: "2222222222222222222222222222222222222222",
      draft: true,
      prerelease: false,
      body: $body,
      assets: [
        {id:1,name:"checksums.txt",size:1,digest:("sha256:" + ("a" * 64)),state:"uploaded"},
        {id:2,name:"telecrawl_0.3.4_darwin_amd64.tar.gz",size:2,digest:("sha256:" + ("b" * 64)),state:"uploaded"},
        {id:3,name:"telecrawl_0.3.4_darwin_arm64.tar.gz",size:3,digest:("sha256:" + ("c" * 64)),state:"uploaded"},
        {id:4,name:"telecrawl_0.3.4_linux_amd64.tar.gz",size:4,digest:("sha256:" + ("d" * 64)),state:"uploaded"},
        {id:5,name:"telecrawl_0.3.4_linux_arm64.tar.gz",size:5,digest:("sha256:" + ("e" * 64)),state:"uploaded"},
        {id:6,name:"telecrawl_0.3.4_windows_amd64.zip",size:6,digest:("sha256:" + ("f" * 64)),state:"uploaded"},
        {id:7,name:"telecrawl_0.3.4_windows_arm64.zip",size:7,digest:("sha256:" + ("0" * 64)),state:"uploaded"}
      ]
    }]'
    exit 0
  fi
fi
printf 'gh %s\n' "$*" >> "$RELEASE_TEST_LOG"
exit 2
EOF

cat > "$tmp/bin/goreleaser" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat > "$tmp/bin/release-helper" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$PWD" != "$RELEASE_TEST_ROOT" && -f .release-test-fresh-clone ]] || {
  echo "release-helper did not run from a fresh source clone" >&2
  exit 1
}
[[ "${MAC_RELEASE_MANIFEST:-}" == "$RELEASE_TEST_MANIFEST" ]] || {
  echo "release-helper did not receive the operator release manifest" >&2
  exit 1
}
printf 'release-helper-cwd %s\n' "$PWD" >> "$RELEASE_TEST_LOG"
printf 'release-helper %s\n' "$*" >> "$RELEASE_TEST_LOG"
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" == --release-notes ]]; then
    cp "$2" "$RELEASE_TEST_LOG.notes"
    break
  fi
  shift
done
EOF

chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"
export MAC_RELEASE_HELPER="$tmp/bin/release-helper"
export GH_HOST=attacker.example
export GH_CONFIG_DIR="$tmp/hostile-gh-config"
mkdir -p "$GH_CONFIG_DIR"

unset NOTARYTOOL_KEYCHAIN_PROFILE GITHUB_TOKEN
: > "$RELEASE_TEST_LOG"
if "$root/scripts/release-local" draft >/dev/null 2>&1; then
  echo "release-local accepted a missing runtime notary profile" >&2
  exit 1
fi
[[ ! -s "$RELEASE_TEST_LOG" ]] || {
  echo "release-local started a release before the notary preflight" >&2
  exit 1
}

export NOTARYTOOL_KEYCHAIN_PROFILE=test-profile
if MAC_RELEASE_MANIFEST="$tmp/missing-release-manifest" \
  "$root/scripts/release-local" draft >/dev/null 2>&1; then
  echo "release-local accepted a missing release-mac-app manifest" >&2
  exit 1
fi
mkdir -p "$tmp/overlay"
cat > "$tmp/overlay/main.go" <<'EOF'
//go:build darwin

package main

func main() {}
EOF
jq -n \
  --arg source "$root/cmd/telecrawl/main.go" \
  --arg replacement "$tmp/overlay/main.go" \
  '{Replace:{($source):$replacement}}' > "$tmp/overlay.json"
cat > "$tmp/go.work" <<'EOF'
go 1.26.5

use .
EOF
cat > "$tmp/executable-go-hook" <<EOF
#!/usr/bin/env bash
touch "$tmp/executable-go-hook-ran"
EOF
chmod 0755 "$tmp/executable-go-hook"
for contaminated_mode in overlay workspace experiment goroot cacheprog auth; do
  : > "$RELEASE_TEST_LOG"
  case "$contaminated_mode" in
    overlay)
      contaminated_env=(GOFLAGS="-overlay=$tmp/overlay.json")
      ;;
    workspace)
      contaminated_env=(GOWORK="$tmp/go.work")
      ;;
    experiment)
      contaminated_env=(GOEXPERIMENT=fieldtrack)
      ;;
    goroot)
      contaminated_env=(GOROOT="$tmp/overlay")
      ;;
    cacheprog)
      contaminated_env=(GOCACHEPROG="$tmp/executable-go-hook")
      ;;
    auth)
      contaminated_env=(GOAUTH="$tmp/executable-go-hook")
      ;;
  esac
  if env "${contaminated_env[@]}" "$root/scripts/release-local" draft >/dev/null 2>&1; then
    echo "release-local accepted ambient Go $contaminated_mode contamination" >&2
    exit 1
  fi
  if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
    echo "release-local reached codesign-run with ambient Go $contaminated_mode contamination" >&2
    exit 1
  fi
  [[ ! -e "$tmp/executable-go-hook-ran" ]] || {
    echo "release-local executed ambient Go $contaminated_mode hook" >&2
    exit 1
  }
done

for release_mode in draft pilot; do
  release_args=()
  [[ "$release_mode" != pilot ]] || release_args=(v0.3.4)
  : > "$RELEASE_TEST_LOG"
  if RELEASE_TEST_MODE=off-default "$root/scripts/release-local" "$release_mode" "${release_args[@]}" >/dev/null 2>&1; then
    echo "release-local accepted an off-default signed release source: $release_mode" >&2
    exit 1
  fi
  if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
    echo "release-local reached codesign-run from an off-default signed source: $release_mode" >&2
    exit 1
  fi
done

for mode in unprotected-default default-api-mismatch; do
  : > "$RELEASE_TEST_LOG"
  if RELEASE_TEST_MODE=$mode "$root/scripts/release-local" draft >/dev/null 2>&1; then
    echo "release-local accepted unsafe GitHub default branch mode: $mode" >&2
    exit 1
  fi
  if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
    echo "release-local reached codesign-run with unsafe GitHub default branch mode: $mode" >&2
    exit 1
  fi
done

for mode in hidden-untracked-config hidden-index replacement-ref; do
  : > "$RELEASE_TEST_LOG"
  if RELEASE_TEST_MODE=$mode "$root/scripts/release-local" draft >/dev/null 2>&1; then
    echo "release-local accepted hidden local source mode: $mode" >&2
    exit 1
  fi
  if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
    echo "release-local reached codesign-run with hidden local source mode: $mode" >&2
    exit 1
  fi
done

printf '%s\n' '3333333333333333333333333333333333333333 2222222222222222222222222222222222222222' \
  > "$RELEASE_TEST_GRAFT_PATH"
if RELEASE_TEST_MODE=graft-file "$root/scripts/release-local" draft >/dev/null 2>&1; then
  echo "release-local accepted a legacy Git graft ancestry override" >&2
  exit 1
fi
rm "$RELEASE_TEST_GRAFT_PATH"
if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
  echo "release-local reached codesign-run with an active Git graft" >&2
  exit 1
fi

for mode in missing object-mismatch commit-mismatch off-default-tag untrusted-global-signer; do
  : > "$RELEASE_TEST_LOG"
  rm -f "$RELEASE_TEST_LOG.notes"
  if RELEASE_TEST_MODE=$mode "$root/scripts/release-local" draft >/dev/null 2>&1; then
    echo "release-local accepted remote tag mode: $mode" >&2
    exit 1
  fi
  if grep -Fq release-helper "$RELEASE_TEST_LOG"; then
    echo "release-local opened a draft after remote tag failure: $mode" >&2
    exit 1
  fi
done

: > "$RELEASE_TEST_LOG"
rm -f "$RELEASE_TEST_LOG.notes"
RELEASE_TEST_MODE=exact "$root/scripts/release-local" draft
grep -Fq 'git clone --quiet --no-checkout --no-local --no-tags' "$RELEASE_TEST_LOG"
grep -Fq 'https://github.com/openclaw/telecrawl.git' "$RELEASE_TEST_LOG"
grep -Fq 'release-helper-cwd ' "$RELEASE_TEST_LOG"
grep -Fq 'release-helper codesign-run -- env' "$RELEASE_TEST_LOG"
grep -Fq 'GOTOOLCHAIN=local GOWORK=off LC_ALL=C' "$RELEASE_TEST_LOG"
grep -Fq 'TELECRAWL_OFFICIAL_RELEASE=1 goreleaser release --draft --clean --parallelism=2 --release-notes' "$RELEASE_TEST_LOG"
grep -Fq 'Tagged release notes sentinel; mutable worktree notes must not be used.' "$RELEASE_TEST_LOG.notes"
grep -Fq 'Full changelog: https://github.com/openclaw/telecrawl/blob/v0.3.4/CHANGELOG.md' "$RELEASE_TEST_LOG.notes"
if grep -Eq 'gh (workflow|release)|--method PATCH|homebrew-tap' "$RELEASE_TEST_LOG"; then
  echo "release-local draft crossed a later serialized gate" >&2
  exit 1
fi

"$tmp/bin/gh" api --hostname github.com 'repos/openclaw/telecrawl/releases?per_page=100' |
  jq '.[0]' > "$tmp/draft-release.json"
"$root/scripts/validate-release-record.sh" \
  "$tmp/draft-release.json" v0.3.4 true \
  2222222222222222222222222222222222222222 \
  "$RELEASE_TEST_LOG.notes" > "$tmp/release-snapshot.json"
jq '.draft = false | .published_at = "2026-07-09T12:34:56Z"' \
  "$tmp/draft-release.json" > "$tmp/published-release.json"
"$root/scripts/validate-release-record.sh" \
  "$tmp/published-release.json" v0.3.4 false \
  2222222222222222222222222222222222222222 \
  "$RELEASE_TEST_LOG.notes" "$tmp/release-snapshot.json" >/dev/null

assert_release_record_rejected() {
  local label=$1 filter=$2
  jq "$filter" "$tmp/draft-release.json" > "$tmp/tampered-release.json"
  if "$root/scripts/validate-release-record.sh" \
    "$tmp/tampered-release.json" v0.3.4 true \
    2222222222222222222222222222222222222222 \
    "$RELEASE_TEST_LOG.notes" "$tmp/release-snapshot.json" >/dev/null 2>&1; then
    echo "release record validator accepted tampered $label" >&2
    exit 1
  fi
}

assert_release_record_rejected title '.name = "tampered"'
assert_release_record_rejected body '.body += "tampered"'
assert_release_record_rejected prerelease '.prerelease = true'
assert_release_record_rejected tag '.tag_name = "v9.9.9"'
assert_release_record_rejected target '.target_commitish = "3333333333333333333333333333333333333333"'
assert_release_record_rejected zero-size '.assets[0].size = 0'
assert_release_record_rejected replaced-id '.assets[0].id = 99'
assert_release_record_rejected replaced-digest '.assets[0].digest = ("sha256:" + ("9" * 64))'
assert_release_record_rejected replaced-name '.assets[0].name = "replacement"'
assert_release_record_rejected release-id '.id = 43'

grep -A2 '^release:' "$root/.goreleaser.yaml" | grep -Fq 'draft: true'
grep -Fq 'name_template: "{{ .Tag }}"' "$root/.goreleaser.yaml"
grep -Fq 'target_commitish: "{{ .Commit }}"' "$root/.goreleaser.yaml"
[[ "$(grep -Fc 'env: &release_build_env' "$root/.goreleaser.yaml")" == 1 &&
  "$(grep -Fc 'env: *release_build_env' "$root/.goreleaser.yaml")" == 1 ]] || {
  echo "GoReleaser build IDs do not share one sanitized Go environment" >&2
  exit 1
}
for go_env in \
  '"GOCACHEPROG="' \
  '"GODEBUG="' \
  'GO111MODULE=on' \
  'GOAUTH=off' \
  'GOENV=off' \
  '"GOEXPERIMENT="' \
  '"GOFLAGS="' \
  'GOFIPS140=off' \
  'GOAMD64=v1' \
  'GOARM64=v8.0' \
  'GOPROXY=https://proxy.golang.org' \
  '"GOROOT="' \
  'GOSUMDB=sum.golang.org' \
  'GOTOOLCHAIN=local' \
  'GOWORK=off'; do
  grep -Fq -- "- $go_env" "$root/.goreleaser.yaml" || {
    echo "GoReleaser is missing sanitized Go environment entry: $go_env" >&2
    exit 1
  }
done
prepare_go_function=$(sed -n '/^prepare_official_go_environment()/,/^}/p' "$root/scripts/release-local")
grep -Fq 'ambient Go build control is forbidden' <<<"$prepare_go_function"
workflow_release_tests=$(grep -R -F 'test-release-local.sh' "$root/.github/workflows")
[[ -n "$workflow_release_tests" ]]
if grep -Fv 'env -u GOTOOLCHAIN ./scripts/test-release-local.sh' <<<"$workflow_release_tests" | grep -q .; then
  echo "workflow invokes the local release test with ambient Go build controls" >&2
  exit 1
fi
grep -Fq 'go version go1.26.5 darwin/$expected_goarch' <<<"$prepare_go_function"
grep -Fq 'GO111MODULE' <<<"$prepare_go_function"
grep -Fq 'GOAUTH' <<<"$prepare_go_function"
grep -Fq 'GOCACHEPROG' <<<"$prepare_go_function"
grep -Fq 'GOFIPS140' <<<"$prepare_go_function"
grep -Fq '"GOFLAGS="' <<<"$prepare_go_function"
grep -Fq 'GOROOT' <<<"$prepare_go_function"
grep -Fq '"GOWORK=off"' <<<"$prepare_go_function"
grep -Fq "TELECRAWL_PILOT_VERSION=\"\$version\"" "$root/scripts/release-local"
grep -Fq "GORELEASER_CURRENT_TAG=\"\$tag\"" "$root/scripts/release-local"
grep -Fq 'goreleaser release --snapshot --clean --skip=publish' "$root/scripts/release-local"
grep -Fq "check-release-verifier.sh\" \"\$tag\" true \"\$trusted_tag_commit\"" "$root/scripts/release-local"
grep -Fq "dispatch_verifier \"\$tag\" false" "$root/scripts/release-local"
grep -Fq -- "-c \"gpg.ssh.allowedSignersFile=\$allowed_signers_file\"" "$root/scripts/verify-release-tag.sh"
grep -Fq -- '-c gpg.ssh.program=/usr/bin/ssh-keygen' "$root/scripts/verify-release-tag.sh"
grep -Fq '[[ -x /usr/bin/ssh-keygen ]]' "$root/scripts/verify-release-tag.sh"
grep -Fq "verify-release-tag.sh\" \"\$tag\"" "$root/scripts/release-local"
grep -Fq "git show \"\$trusted_tag_object:CHANGELOG.md\"" "$root/scripts/release-local"
for function_name in draft_release dispatch_verifier publish_release update_homebrew; do
  function_body=$(sed -n "/^${function_name}()/,/^}/p" "$root/scripts/release-local")
  grep -Fq "require_remote_signed_tag \"\$tag\"" <<<"$function_body" || {
    echo "release-local does not revalidate the remote signed tag in $function_name" >&2
    exit 1
  }
done
pilot_function=$(sed -n '/^pilot_release()/,/^}/p' "$root/scripts/release-local")
grep -Fq require_current_default_head <<<"$pilot_function"
grep -Fq 'prepare_default_source "$source_dir"' <<<"$pilot_function"
grep -Fq 'cd "$source_dir"' <<<"$pilot_function"
grep -Fq 'MAC_RELEASE_MANIFEST="$release_manifest" "$release_helper" codesign-run' <<<"$pilot_function"
grep -Fq '"$source_dir/scripts/verify-release-assets.sh"' <<<"$pilot_function"
grep -Fq '"$source_dir/scripts/verify-macos-binary.sh"' <<<"$pilot_function"
default_head_function=$(sed -n '/^require_current_default_head()/,/^}/p' "$root/scripts/release-local")
grep -Fq "'.protected'" <<<"$default_head_function"
grep -Fq '"$api_default_commit" == "$trusted_default_commit"' <<<"$default_head_function"
grep -Fq 'status --porcelain --untracked-files=all' <<<"$default_head_function"
grep -Fq 'ls-files -v' <<<"$default_head_function"
grep -Fq 'refs/replace/' <<<"$default_head_function"
grep -Fq 'require_no_git_grafts "$root" "local release"' <<<"$default_head_function"
graft_function=$(sed -n '/^require_no_git_grafts()/,/^}/p' "$root/scripts/release-local")
grep -Fq 'rev-parse --git-path info/grafts' <<<"$graft_function"
grep -Fq '[[ ! -e "$graft_path" && ! -L "$graft_path" ]]' <<<"$graft_function"
graft_check_line=$(grep -nF 'require_no_git_grafts "$root" "local release"' <<<"$default_head_function" | cut -d: -f1)
default_ref_line=$(grep -nF 'default_ref=$(trusted_git ls-remote' <<<"$default_head_function" | cut -d: -f1)
[[ "$graft_check_line" -lt "$default_ref_line" ]] || {
  echo "release-local checks Git grafts after trusting default-branch metadata" >&2
  exit 1
}
grep -Fq 'rev-parse --git-path info/grafts' "$root/scripts/verify-release-tag.sh"
draft_function=$(sed -n '/^draft_release()/,/^}/p' "$root/scripts/release-local")
grep -Fq 'prepare_rebuild_source "$tag" "$source_dir"' <<<"$draft_function"
grep -Fq 'cd "$source_dir"' <<<"$draft_function"
grep -Fq 'MAC_RELEASE_MANIFEST="$release_manifest" "$release_helper" codesign-run' <<<"$draft_function"
remote_source_function=$(sed -n '/^prepare_remote_source()/,/^}/p' "$root/scripts/release-local")
grep -Fq 'clone --quiet --no-checkout --no-local --no-tags' <<<"$remote_source_function"
grep -Fq '"https://github.com/$repository.git"' <<<"$remote_source_function"
if grep -Fq '"$root" "$destination"' <<<"$remote_source_function"; then
  echo "release-local still clones official source from the mutable local repository" >&2
  exit 1
fi
publish_function=$(sed -n '/^publish_release()/,/^}/p' "$root/scripts/release-local")
[[ "$(grep -Fc "verify_release_notes \"\$tag\"" <<<"$publish_function")" -ge 2 ]] || {
  echo "release-local does not verify notes before and after publication" >&2
  exit 1
}
[[ "$(grep -Fc 'github_api "repos/$repository/releases/$accepted_release_id"' <<<"$publish_function")" == 2 ]] || {
  echo "release-local does not GET the numeric release ID before and after publication" >&2
  exit 1
}
[[ "$(grep -Fc 'validate_release_record_file' <<<"$publish_function")" -ge 3 ]] || {
  echo "release-local does not fully validate numeric release records around publication" >&2
  exit 1
}
prepatch_line=$(grep -nF '> "$prepatch_record"' <<<"$publish_function" | cut -d: -f1)
patch_line=$(grep -nF -- '--method PATCH' <<<"$publish_function" | cut -d: -f1)
published_get_line=$(grep -nF '> "$published_record"' <<<"$publish_function" | cut -d: -f1)
[[ "$prepatch_line" -lt "$patch_line" && "$patch_line" -lt "$published_get_line" ]] || {
  echo "release-local publication revalidation order is unsafe" >&2
  exit 1
}
accepted_tag_line=$(grep -nF 'accepted_tag_object=$trusted_tag_object' <<<"$publish_function" | cut -d: -f1)
prepatch_tag_fetch_line=$(grep -nF 'require_remote_signed_tag "$tag" false' <<<"$publish_function" | tail -2 | head -1 | cut -d: -f1)
prepatch_tag_pin_line=$(grep -nF '"$trusted_tag_object" == "$accepted_tag_object"' <<<"$publish_function" | head -1 | cut -d: -f1)
[[ "$accepted_tag_line" -lt "$prepatch_tag_fetch_line" &&
  "$prepatch_tag_fetch_line" -lt "$prepatch_tag_pin_line" &&
  "$prepatch_tag_pin_line" -lt "$prepatch_line" ]] || {
  echo "release-local can publish after the verifier-accepted tag object changes" >&2
  exit 1
}
final_record_line=$(grep -nF 'release_record "$tag" false "$accepted_snapshot"' <<<"$publish_function" | cut -d: -f1)
final_tag_fetch_line=$(grep -nF 'require_remote_signed_tag "$tag" false' <<<"$publish_function" | tail -1 | cut -d: -f1)
final_tag_pin_line=$(grep -nF '"$trusted_tag_object" == "$accepted_tag_object"' <<<"$publish_function" | tail -1 | cut -d: -f1)
publish_success_line=$(grep -nF 'echo "release: published $tag' <<<"$publish_function" | cut -d: -f1)
[[ "$(grep -Fc '"$trusted_tag_object" == "$accepted_tag_object"' <<<"$publish_function")" == 2 &&
  "$final_record_line" -lt "$final_tag_fetch_line" &&
  "$final_tag_fetch_line" -lt "$final_tag_pin_line" &&
  "$final_tag_pin_line" -lt "$publish_success_line" ]] || {
  echo "release-local can report success after a tag move during publication" >&2
  exit 1
}

mkdir -p "$tmp/publish-probe-root/scripts" "$tmp/publish-probe-work"
printf '%s\n' "$publish_function" > "$tmp/publish-release-function.sh"
printf '%s\n' '{}' > "$tmp/publish-probe-work/release-snapshot.json"
cat > "$tmp/publish-probe-root/scripts/check-release-verifier.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' 42
EOF
chmod +x "$tmp/publish-probe-root/scripts/check-release-verifier.sh"
cat > "$tmp/publish-tag-move-probe.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
root=$PUBLISH_PROBE_ROOT
work_dir=$PUBLISH_PROBE_WORK
repository=openclaw/telecrawl
trusted_tag_object=1111111111111111111111111111111111111111
trusted_tag_commit=2222222222222222222222222222222222222222
release_id=42
release_snapshot_file=$work_dir/release-snapshot.json
release_json='[]'
tag_check_count=0

validate_tag() { :; }
require_tools() { :; }
verify_release_notes() { :; }
validate_release_record_file() { :; }
release_record() {
  release_id=42
  release_snapshot_file=$work_dir/release-snapshot.json
}
require_remote_signed_tag() {
  tag_check_count=$((tag_check_count + 1))
  trusted_tag_object=1111111111111111111111111111111111111111
  trusted_tag_commit=2222222222222222222222222222222222222222
  if [[ "$tag_check_count" -eq 4 ]]; then
    trusted_tag_object=3333333333333333333333333333333333333333
  fi
}
gh() {
  printf 'gh %s\n' "$*" >> "$PUBLISH_PROBE_LOG"
  printf '%s\n' '{}'
}
github_api() {
  gh api --hostname github.com "$@"
}

# shellcheck source=/dev/null
source "$PUBLISH_PROBE_FUNCTION"
publish_release v0.3.4
EOF
chmod +x "$tmp/publish-tag-move-probe.sh"
: > "$tmp/publish-probe.log"
if PUBLISH_PROBE_ROOT="$tmp/publish-probe-root" \
  PUBLISH_PROBE_WORK="$tmp/publish-probe-work" \
  PUBLISH_PROBE_LOG="$tmp/publish-probe.log" \
  PUBLISH_PROBE_FUNCTION="$tmp/publish-release-function.sh" \
  "$tmp/publish-tag-move-probe.sh" > "$tmp/publish-probe-output.log" 2>&1; then
  echo "release-local reported publication success after a deterministic live tag move" >&2
  exit 1
fi
grep -Fq -- '--method PATCH' "$tmp/publish-probe.log" || {
  echo "publication tag-move regression did not reach the PATCH window" >&2
  exit 1
}
if grep -Fq 'release: published v0.3.4' "$tmp/publish-probe-output.log"; then
  echo "release-local emitted success after a deterministic live tag move" >&2
  exit 1
fi
homebrew_function=$(sed -n '/^update_homebrew()/,/^}/p' "$root/scripts/release-local")
homebrew_revalidate_function=$(sed -n '/^revalidate_homebrew_source()/,/^}/p' "$root/scripts/release-local")
handoff_revalidate_function=$(sed -n '/^revalidate_homebrew_handoff_snapshot()/,/^}/p' "$root/scripts/release-local")
snapshot_match_function=$(sed -n '/^require_release_snapshot_matches_directory()/,/^}/p' "$root/scripts/release-local")
grep -Fq "verify_release_notes \"\$tag\" published" <<<"$homebrew_function"
[[ "$(grep -Fc "download_and_verify_public_release \\" <<<"$homebrew_function")" == 2 ]] || {
  echo "release-local does not use two independently downloaded public inventories" >&2
  exit 1
}
grep -Fq 'public_dir="$work_dir/public-release-initial"' <<<"$homebrew_function"
grep -Fq 'handoff_dir="$work_dir/public-release-handoff"' <<<"$homebrew_function"
grep -Fq 'shasum -a 256 "$handoff_dir/telecrawl_${version}_darwin_amd64.tar.gz"' <<<"$homebrew_function"
[[ "$(grep -Fc 'require_tap_default_at "$trusted_tap_branch" "$trusted_tap_commit"' <<<"$homebrew_function")" -ge 2 ]] || {
  echo "release-local does not recheck the protected tap default before dispatch" >&2
  exit 1
}
grep -Fq -- '--ref "$trusted_tap_branch"' <<<"$homebrew_function"
grep -Fq "'.workflow_id'" <<<"$homebrew_function"
grep -Fq "'.path'" <<<"$homebrew_function"
grep -Fq 'contents/Formula/telecrawl.rb?ref=$completed_tap_commit' <<<"$homebrew_function"
grep -Fq 'env -i "${homebrew_env[@]}" "$brew_bin" info --json=v2' <<<"$homebrew_function"
grep -Fq 'env -i "${homebrew_env[@]}" "$brew_bin" install --formula' <<<"$homebrew_function"
grep -Fq 'env -i "${homebrew_env[@]}" "$brew_bin" test telecrawl' <<<"$homebrew_function"
grep -Fq 'cmp -s "$handoff_candidate" "$installed_binary"' <<<"$homebrew_function"
grep -Fq '"$installed_binary" "$native_arch" "$version" static' <<<"$homebrew_function"
grep -Fq 'assert_trusted_release_helpers_clean' <<<"$homebrew_function"
install_line=$(grep -nF '"$brew_bin" install --formula' <<<"$homebrew_function" | cut -d: -f1)
installed_cmp_line=$(grep -nF 'cmp -s "$handoff_candidate" "$installed_binary"' <<<"$homebrew_function" | cut -d: -f1)
installed_static_line=$(grep -nF '"$installed_binary" "$native_arch" "$version" static' <<<"$homebrew_function" | cut -d: -f1)
brew_test_line=$(grep -nF '"$brew_bin" test telecrawl' <<<"$homebrew_function" | cut -d: -f1)
final_tap_line=$(grep -nF 'require_tap_default_at "$trusted_tap_branch" "$completed_tap_commit"' <<<"$homebrew_function" | cut -d: -f1)
homebrew_success_line=$(grep -nF 'echo "release: Homebrew formula and clean install passed' <<<"$homebrew_function" | cut -d: -f1)
[[ "$install_line" -lt "$installed_cmp_line" &&
  "$installed_cmp_line" -lt "$installed_static_line" &&
  "$installed_static_line" -lt "$brew_test_line" &&
  "$brew_test_line" -lt "$final_tap_line" &&
  "$final_tap_line" -lt "$homebrew_success_line" ]] || {
  echo "Homebrew candidate executes before installed-byte and signature verification" >&2
  exit 1
}
grep -Fq 'accepted_release_snapshot="$work_dir/homebrew-handoff-release-snapshot.json"' <<<"$homebrew_function"
grep -Fq 'cp "$release_snapshot_file" "$accepted_release_snapshot"' <<<"$homebrew_function"
grep -Fq 'verifier_release_snapshot="$work_dir/homebrew-pre-verifier-release-snapshot.json"' <<<"$homebrew_function"
pre_verifier_snapshot_line=$(grep -nF 'cp "$release_snapshot_file" "$verifier_release_snapshot"' <<<"$homebrew_function" | cut -d: -f1)
final_verifier_line=$(grep -nF 'check-release-verifier.sh' <<<"$homebrew_function" | tail -1 | cut -d: -f1)
post_verifier_record_line=$(grep -nF 'revalidate_homebrew_handoff_snapshot "$tag" "$verifier_release_snapshot"' <<<"$homebrew_function" | cut -d: -f1)
accepted_snapshot_line=$(grep -nF 'cp "$release_snapshot_file" "$accepted_release_snapshot"' <<<"$homebrew_function" | cut -d: -f1)
snapshot_bind_line=$(grep -nF 'require_release_snapshot_matches_directory' <<<"$homebrew_function" | cut -d: -f1)
handoff_hash_line=$(grep -nF 'darwin_amd64_sha256=$(shasum -a 256 "$handoff_dir/' <<<"$homebrew_function" | cut -d: -f1)
tap_dispatch_line=$(grep -nF 'gh workflow run update-formula.yml' <<<"$homebrew_function" | cut -d: -f1)
[[ "$pre_verifier_snapshot_line" -lt "$final_verifier_line" &&
  "$final_verifier_line" -lt "$post_verifier_record_line" &&
  "$post_verifier_record_line" -lt "$accepted_snapshot_line" &&
  "$accepted_snapshot_line" -lt "$snapshot_bind_line" &&
  "$snapshot_bind_line" -lt "$handoff_hash_line" &&
  "$handoff_hash_line" -lt "$tap_dispatch_line" ]] || {
  echo "Homebrew hashes are not derived from a post-verifier stable release snapshot" >&2
  exit 1
}
grep -Fq 'release_record "$tag" false "$expected_snapshot"' <<<"$handoff_revalidate_function"
grep -Fq 'verify_release_notes "$tag" published' <<<"$handoff_revalidate_function"
grep -Fq 'expected_title="Update telecrawl for ${tag} (request-id=${request_id}; source-tag-object=${accepted_tag_object}; source-tag-commit=${accepted_tag_commit})"' <<<"$homebrew_function"
grep -Fq 'expected_tap_run_path=".github/workflows/update-formula.yml"' <<<"$homebrew_function"
grep -Fq '"$(jq -r '\''.path'\'' <<<"$tap_run_json")" == "$expected_tap_run_path"' <<<"$homebrew_function"
cat > "$tmp/live-tap-run.json" <<'EOF'
{"id":29010348667,"path":".github/workflows/update-formula.yml","head_branch":"main","head_sha":"ea346bb1b7b92cd3183b878c8c9c4d5a0f9acf92","workflow_id":220664022}
EOF
jq -e '
  .path == ".github/workflows/update-formula.yml" and
  .head_branch == "main" and
  .head_sha == "ea346bb1b7b92cd3183b878c8c9c4d5a0f9acf92" and
  .workflow_id == 220664022
' "$tmp/live-tap-run.json" >/dev/null || {
  echo "live Homebrew workflow-run record shape was rejected" >&2
  exit 1
}
jq '.path += "@main"' "$tmp/live-tap-run.json" > "$tmp/suffixed-tap-run.json"
if jq -e '.path == ".github/workflows/update-formula.yml"' \
  "$tmp/suffixed-tap-run.json" >/dev/null; then
  echo "documentation-example Homebrew workflow path suffix was accepted" >&2
  exit 1
fi
grep -Fq "'telecrawl: update formula for %s\\n\\nSource-Repository: %s\\nSource-Tag-Object: %s\\nSource-Tag-Commit: %s\\nRequest-ID: %s'" <<<"$homebrew_function"
grep -Fq 'repos/openclaw/homebrew-tap/git/commits/$completed_tap_commit' <<<"$homebrew_function"
grep -Fq "'.parents[0].sha // empty'" <<<"$homebrew_function"

brew_test_line=$(grep -nF '"$brew_bin" test telecrawl' <<<"$homebrew_function" | cut -d: -f1)
post_test_helper_line=$(grep -nF 'assert_trusted_release_helpers_clean' <<<"$homebrew_function" | tail -1 | cut -d: -f1)
post_test_release_line=$(grep -nF 'revalidate_homebrew_source' <<<"$homebrew_function" | tail -1 | cut -d: -f1)
final_tap_line=$(grep -nF 'require_tap_default_at "$trusted_tap_branch" "$completed_tap_commit"' <<<"$homebrew_function" | cut -d: -f1)
homebrew_success_line=$(grep -nF 'echo "release: Homebrew formula and clean install passed' <<<"$homebrew_function" | cut -d: -f1)
[[ "$brew_test_line" -lt "$post_test_helper_line" &&
  "$post_test_helper_line" -lt "$post_test_release_line" &&
  "$post_test_release_line" -lt "$final_tap_line" &&
  "$final_tap_line" -lt "$homebrew_success_line" ]] || {
  echo "Homebrew closeout does not revalidate exact source state after candidate execution" >&2
  exit 1
}
grep -Fq '"$trusted_tag_object" == "$expected_tag_object"' <<<"$homebrew_revalidate_function"
grep -Fq 'release_record "$tag" false "$expected_release_snapshot"' <<<"$homebrew_revalidate_function"
grep -Fq '"$tag" false "$expected_tag_commit" "$expected_tag_object"' <<<"$homebrew_revalidate_function"

mkdir -p "$tmp/homebrew-closeout-probe-root/scripts"
cat > "$tmp/homebrew-closeout-probe-root/scripts/check-release-verifier.sh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' 42
EOF
chmod +x "$tmp/homebrew-closeout-probe-root/scripts/check-release-verifier.sh"
printf '%s\n' "$homebrew_revalidate_function" > "$tmp/revalidate-homebrew-source.sh"
cat > "$tmp/homebrew-closeout-probe.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
root=$HOMEBREW_CLOSEOUT_PROBE_ROOT
trusted_tag_object=
trusted_tag_commit=
verifier_run=
require_remote_signed_tag() {
  trusted_tag_object=1111111111111111111111111111111111111111
  trusted_tag_commit=2222222222222222222222222222222222222222
  [[ "${HOMEBREW_CLOSEOUT_TEST_MODE:-exact}" != tag-move ]] || \
    trusted_tag_object=3333333333333333333333333333333333333333
}
release_record() {
  [[ "${3:-}" == "$HOMEBREW_CLOSEOUT_EXPECTED_SNAPSHOT" ]]
  [[ "${HOMEBREW_CLOSEOUT_TEST_MODE:-exact}" != release-move ]]
}
verify_release_notes() { :; }
# shellcheck source=/dev/null
source "$HOMEBREW_CLOSEOUT_FUNCTION"
revalidate_homebrew_source \
  v0.3.4 \
  1111111111111111111111111111111111111111 \
  2222222222222222222222222222222222222222 \
  "$HOMEBREW_CLOSEOUT_EXPECTED_SNAPSHOT" \
  "$HOMEBREW_CLOSEOUT_EXPECTED_HASH"
EOF
chmod +x "$tmp/homebrew-closeout-probe.sh"
printf '%s\n' '{}' > "$tmp/homebrew-accepted-release-snapshot.json"
homebrew_snapshot_hash=$(shasum -a 256 "$tmp/homebrew-accepted-release-snapshot.json" | awk '{print $1}')
for closeout_mode in tag-move release-move; do
  if HOMEBREW_CLOSEOUT_PROBE_ROOT="$tmp/homebrew-closeout-probe-root" \
    HOMEBREW_CLOSEOUT_FUNCTION="$tmp/revalidate-homebrew-source.sh" \
    HOMEBREW_CLOSEOUT_EXPECTED_SNAPSHOT="$tmp/homebrew-accepted-release-snapshot.json" \
    HOMEBREW_CLOSEOUT_EXPECTED_HASH="$homebrew_snapshot_hash" \
    HOMEBREW_CLOSEOUT_TEST_MODE="$closeout_mode" \
    "$tmp/homebrew-closeout-probe.sh" >/dev/null 2>&1; then
    echo "Homebrew closeout accepted live source movement: $closeout_mode" >&2
    exit 1
  fi
done
HOMEBREW_CLOSEOUT_PROBE_ROOT="$tmp/homebrew-closeout-probe-root" \
  HOMEBREW_CLOSEOUT_FUNCTION="$tmp/revalidate-homebrew-source.sh" \
  HOMEBREW_CLOSEOUT_EXPECTED_SNAPSHOT="$tmp/homebrew-accepted-release-snapshot.json" \
  HOMEBREW_CLOSEOUT_EXPECTED_HASH="$homebrew_snapshot_hash" \
  "$tmp/homebrew-closeout-probe.sh"

mkdir -p "$tmp/homebrew-snapshot-a"
snapshot_assets="$tmp/homebrew-snapshot-assets.jsonl"
snapshot_asset_id=1000
: > "$snapshot_assets"
for name in \
  checksums.txt \
  telecrawl_0.3.4_darwin_amd64.tar.gz \
  telecrawl_0.3.4_darwin_arm64.tar.gz \
  telecrawl_0.3.4_linux_amd64.tar.gz \
  telecrawl_0.3.4_linux_arm64.tar.gz \
  telecrawl_0.3.4_windows_amd64.zip \
  telecrawl_0.3.4_windows_arm64.zip; do
  printf 'A:%s\n' "$name" > "$tmp/homebrew-snapshot-a/$name"
  size=$(wc -c < "$tmp/homebrew-snapshot-a/$name" | tr -d ' ')
  digest=$(shasum -a 256 "$tmp/homebrew-snapshot-a/$name" | awk '{print $1}')
  snapshot_asset_id=$((snapshot_asset_id + 1))
  jq -cn --argjson id "$snapshot_asset_id" --arg name "$name" \
    --argjson size "$size" --arg digest "sha256:$digest" \
    '{id:$id,name:$name,size:$size,digest:$digest}' >> "$snapshot_assets"
done
jq -s '{id:9001,tag_name:"v0.3.4",assets:.}' \
  "$snapshot_assets" > "$tmp/homebrew-snapshot-a.json"
printf '%s\n' "$snapshot_match_function" > "$tmp/require-release-snapshot-matches-directory.sh"
cat > "$tmp/homebrew-snapshot-probe.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
gh() {
  printf 'gh %s\n' "$*" >> "$HOMEBREW_SNAPSHOT_DISPATCH_LOG"
}
# shellcheck source=/dev/null
source "$HOMEBREW_SNAPSHOT_FUNCTION"
require_release_snapshot_matches_directory \
  "$HOMEBREW_SNAPSHOT_FILE" "$HOMEBREW_SNAPSHOT_DIR"
gh workflow run update-formula.yml --repo github.com/openclaw/homebrew-tap
EOF
chmod +x "$tmp/homebrew-snapshot-probe.sh"
: > "$tmp/homebrew-snapshot-dispatch.log"
HOMEBREW_SNAPSHOT_FUNCTION="$tmp/require-release-snapshot-matches-directory.sh" \
  HOMEBREW_SNAPSHOT_FILE="$tmp/homebrew-snapshot-a.json" \
  HOMEBREW_SNAPSHOT_DIR="$tmp/homebrew-snapshot-a" \
  HOMEBREW_SNAPSHOT_DISPATCH_LOG="$tmp/homebrew-snapshot-dispatch.log" \
  "$tmp/homebrew-snapshot-probe.sh"
[[ "$(<"$tmp/homebrew-snapshot-dispatch.log")" == \
  'gh workflow run update-formula.yml --repo github.com/openclaw/homebrew-tap' ]] || {
  echo "matching Homebrew handoff snapshot did not reach dispatch" >&2
  exit 1
}
for snapshot_move in digest size; do
  : > "$tmp/homebrew-snapshot-dispatch.log"
  if [[ "$snapshot_move" == digest ]]; then
    jq '(.assets[] | select(.name == "telecrawl_0.3.4_darwin_arm64.tar.gz")) |=
      (.id = 2001 | .digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")' \
      "$tmp/homebrew-snapshot-a.json" > "$tmp/homebrew-snapshot-b.json"
  else
    jq '(.assets[] | select(.name == "telecrawl_0.3.4_darwin_amd64.tar.gz")) |=
      (.id = 2002 | .size += 1)' \
      "$tmp/homebrew-snapshot-a.json" > "$tmp/homebrew-snapshot-b.json"
  fi
  if HOMEBREW_SNAPSHOT_FUNCTION="$tmp/require-release-snapshot-matches-directory.sh" \
    HOMEBREW_SNAPSHOT_FILE="$tmp/homebrew-snapshot-b.json" \
    HOMEBREW_SNAPSHOT_DIR="$tmp/homebrew-snapshot-a" \
    HOMEBREW_SNAPSHOT_DISPATCH_LOG="$tmp/homebrew-snapshot-dispatch.log" \
    "$tmp/homebrew-snapshot-probe.sh" >/dev/null 2>&1; then
    echo "Homebrew handoff A was accepted against replacement snapshot B: $snapshot_move" >&2
    exit 1
  fi
  [[ ! -s "$tmp/homebrew-snapshot-dispatch.log" ]] || {
    echo "Homebrew workflow dispatched after A-to-B asset replacement: $snapshot_move" >&2
    exit 1
  }
done

printf '%s\n' "$handoff_revalidate_function" > "$tmp/revalidate-homebrew-handoff-snapshot.sh"
cat > "$tmp/homebrew-final-check-probe.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
release_snapshot_file="$HOMEBREW_FINAL_CHECK_RELEASE_SNAPSHOT"
live_release_snapshot="$HOMEBREW_FINAL_CHECK_PRE_SNAPSHOT"
release_record() {
  local tag=$1 draft=$2 expected_snapshot=$3
  [[ "$tag" == v0.3.4 && "$draft" == false ]]
  cp "$live_release_snapshot" "$release_snapshot_file"
  cmp "$expected_snapshot" "$release_snapshot_file"
}
verify_release_notes() { :; }
check_release_verifier() {
  [[ "$HOMEBREW_FINAL_CHECK_MODE" == stable ]] || \
    live_release_snapshot="$HOMEBREW_FINAL_CHECK_POST_SNAPSHOT"
}
gh() {
  printf 'gh %s\n' "$*" >> "$HOMEBREW_FINAL_CHECK_DISPATCH_LOG"
}
# shellcheck source=/dev/null
source "$HOMEBREW_FINAL_CHECK_REVALIDATE_FUNCTION"
# shellcheck source=/dev/null
source "$HOMEBREW_SNAPSHOT_FUNCTION"
check_release_verifier
revalidate_homebrew_handoff_snapshot v0.3.4 "$HOMEBREW_FINAL_CHECK_PRE_SNAPSHOT"
cp "$release_snapshot_file" "$HOMEBREW_FINAL_CHECK_ACCEPTED_SNAPSHOT"
require_release_snapshot_matches_directory \
  "$HOMEBREW_FINAL_CHECK_ACCEPTED_SNAPSHOT" "$HOMEBREW_SNAPSHOT_DIR"
gh workflow run update-formula.yml --repo github.com/openclaw/homebrew-tap
EOF
chmod +x "$tmp/homebrew-final-check-probe.sh"
for final_check_mode in stable replaced; do
  : > "$tmp/homebrew-final-check-dispatch.log"
  if HOMEBREW_FINAL_CHECK_MODE="$final_check_mode" \
    HOMEBREW_FINAL_CHECK_PRE_SNAPSHOT="$tmp/homebrew-snapshot-a.json" \
    HOMEBREW_FINAL_CHECK_POST_SNAPSHOT="$tmp/homebrew-snapshot-b.json" \
    HOMEBREW_FINAL_CHECK_RELEASE_SNAPSHOT="$tmp/homebrew-final-check-release.json" \
    HOMEBREW_FINAL_CHECK_ACCEPTED_SNAPSHOT="$tmp/homebrew-final-check-accepted.json" \
    HOMEBREW_FINAL_CHECK_REVALIDATE_FUNCTION="$tmp/revalidate-homebrew-handoff-snapshot.sh" \
    HOMEBREW_SNAPSHOT_FUNCTION="$tmp/require-release-snapshot-matches-directory.sh" \
    HOMEBREW_SNAPSHOT_DIR="$tmp/homebrew-snapshot-a" \
    HOMEBREW_FINAL_CHECK_DISPATCH_LOG="$tmp/homebrew-final-check-dispatch.log" \
    "$tmp/homebrew-final-check-probe.sh" >/dev/null 2>&1; then
    [[ "$final_check_mode" == stable ]] || {
      echo "Homebrew final checker accepted A-to-B release movement" >&2
      exit 1
    }
  elif [[ "$final_check_mode" == stable ]]; then
    echo "stable Homebrew final checker snapshot did not reach dispatch" >&2
    exit 1
  fi
  if [[ "$final_check_mode" == stable ]]; then
    grep -Fxq 'gh workflow run update-formula.yml --repo github.com/openclaw/homebrew-tap' \
      "$tmp/homebrew-final-check-dispatch.log"
  elif [[ -s "$tmp/homebrew-final-check-dispatch.log" ]]; then
    echo "Homebrew workflow dispatched after movement during the final verifier check" >&2
    exit 1
  fi
done

tap_default_function=$(sed -n '/^load_tap_default()/,/^}/p' "$root/scripts/release-local")
grep -Fq "'.protected'" <<<"$tap_default_function"
grep -Fq "'.default_branch // empty'" <<<"$tap_default_function"
tap_contract_function=$(sed -n '/^require_tap_hash_contract()/,/^}/p' "$root/scripts/release-local")
grep -Fq '# verified-hashes-v1' <<<"$tap_contract_function"
grep -Fq '45b93a0b3de27e46b636a0cef819fb1ecef25bcd' "$root/scripts/release-local"
grep -Fq '.github/scripts/update_formula.py' <<<"$tap_contract_function"
grep -Fq 'cmp -s "$base_file" "$current_file"' <<<"$tap_contract_function"
grep -Fq '.github/workflows/update-formula.yml' <<<"$tap_contract_function"
grep -Fq 'trusted_tap_workflow_id' <<<"$tap_contract_function"
grep -Fq 'request_id' <<<"$tap_contract_function"
download_function=$(sed -n '/^download_and_verify_public_release()/,/^}/p' "$root/scripts/release-local")
download_runner=$(sed -n '/^run_release_asset_download()/,/^}/p' "$root/scripts/release-local")
grep -Fq 'export GH_TOKEN="$token"' <<<"$download_runner"
grep -Fq 'unset GITHUB_TOKEN' <<<"$download_runner"
grep -Fq '"$root/scripts/download-release-assets.sh" "$@"' <<<"$download_runner"
[[ "$(grep -Fc 'unset download_token' <<<"$download_function")" -ge 2 ]] || {
  echo "release-local does not immediately clear the parent download token" >&2
  exit 1
}
if grep -Eq 'download_token_env|env .*GH_TOKEN=|printf .*GH_TOKEN' <<<"$download_function$download_runner"; then
  echo "release-local exposes the download token through a child argv assignment" >&2
  exit 1
fi
grep -Fq 'verify-release-assets.sh' <<<"$download_function"
grep -Fq 'rebuild-release-assets.sh' <<<"$download_function"
grep -Fq 'freeze-release-inventory.sh' <<<"$download_function"
grep -Fq 'assert_trusted_release_helpers_clean' <<<"$download_function"
if grep -Fq ' execute' <<<"$download_function"; then
  echo "public release candidate executes before the caller finishes trust decisions" >&2
  exit 1
fi

mkdir -p "$tmp/token-probe-root/scripts"
cat > "$tmp/token-probe-root/scripts/download-release-assets.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${GH_TOKEN:-}" == "$RELEASE_TEST_EXPECTED_TOKEN" ]]
[[ -z "${GITHUB_TOKEN+x}" ]]
[[ "${GITHUB_REPOSITORY:-}" == openclaw/telecrawl ]]
printf '%s\n' "$@" > "$RELEASE_TEST_ARGV_LOG"
EOF
chmod +x "$tmp/token-probe-root/scripts/download-release-assets.sh"
printf '%s\n' "$download_runner" > "$tmp/run-release-asset-download.sh"
cat > "$tmp/token-probe.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
root=$RELEASE_TEST_PROBE_ROOT
repository=openclaw/telecrawl
(
  # shellcheck source=/dev/null
  source "$RELEASE_TEST_RUNNER_FUNCTION"
  run_release_asset_download "$RELEASE_TEST_EXPECTED_TOKEN" alpha 'two words'
)
EOF
chmod +x "$tmp/token-probe.sh"
RELEASE_TEST_PROBE_ROOT="$tmp/token-probe-root" \
  RELEASE_TEST_RUNNER_FUNCTION="$tmp/run-release-asset-download.sh" \
  RELEASE_TEST_EXPECTED_TOKEN='argv7d02' \
  RELEASE_TEST_ARGV_LOG="$tmp/download-argv.log" \
  GITHUB_TOKEN=must-be-removed \
  "$tmp/token-probe.sh"
grep -Fxq alpha "$tmp/download-argv.log"
grep -Fxq 'two words' "$tmp/download-argv.log"
if grep -Fq 'argv7d02' "$tmp/download-argv.log"; then
  echo "release asset download token appeared in the downloader argv" >&2
  exit 1
fi

[[ "$(grep -Fc '      - -trimpath' "$root/.goreleaser.yaml")" == 2 ]] || {
  echo "GoReleaser does not trim paths in every build ID" >&2
  exit 1
}
grep -Fq -- '      -trimpath' "$root/scripts/rebuild-release-assets.sh"
for go_env in '"GOCACHEPROG="' '"GO111MODULE=on"' '"GOAUTH=off"' '"GOENV=off"' '"GOEXPERIMENT="' '"GOFLAGS="' \
  '"GOFIPS140=off"' '"GOROOT="' '"GOTOOLCHAIN=local"' '"GOWORK=off"'; do
  grep -Fq "$go_env" "$root/scripts/rebuild-release-assets.sh" || {
    echo "rebuild helper is missing sanitized Go environment entry: $go_env" >&2
    exit 1
  }
done

echo "local release gate tests passed"
