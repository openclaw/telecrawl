#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
artifacts="$root/dist/artifacts.json"
checksums="$root/dist/checksums.txt"

[[ -f "$artifacts" && -f "$checksums" ]] || {
  echo "release asset test: snapshot artifacts are missing" >&2
  exit 1
}

jq -e '
  [.[] | select(.type == "Archive")] as $archives |
  ($archives | length) == 6 and
  ([$archives[] | (.goos + "_" + .goarch)] | sort) == [
    "darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64",
    "windows_amd64", "windows_arm64"
  ] and
  ([$archives[] | select(.goos == "windows") | .name | endswith(".zip")] | all) and
  ([$archives[] | select(.goos != "windows") | .name | endswith(".tar.gz")] | all) and
  ([.[] | select(.type == "Checksum" and .name == "checksums.txt")] | length) == 1
' "$artifacts" >/dev/null

while IFS=$'\t' read -r archive goos; do
  if [[ "$goos" == windows ]]; then
    [[ "$(unzip -Z1 "$root/$archive")" == telecrawl.exe ]]
  else
    [[ "$(tar -tzf "$root/$archive")" == telecrawl ]]
  fi
done < <(jq -r '.[] | select(.type == "Archive") | [.path,.goos] | @tsv' "$artifacts")

expected=$(jq -r '.[] | select(.type == "Archive") | .name' "$artifacts" | LC_ALL=C sort)
observed=$(awk 'NF == 2 { print $2 }' "$checksums" | LC_ALL=C sort)
[[ "$observed" == "$expected" ]] || {
  echo "release asset test: checksum inventory differs from archive inventory" >&2
  diff -u <(printf '%s\n' "$expected") <(printf '%s\n' "$observed") >&2 || true
  exit 1
}
(cd "$root/dist" && shasum -a 256 -c checksums.txt >/dev/null)

echo "snapshot release asset contract passed"
