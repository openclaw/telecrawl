#!/usr/bin/env bash
set -euo pipefail

out=${1:?usage: check-vulnerabilities.sh OUTPUT_DIRECTORY [FROZEN_DB_DIRECTORY]}
mkdir -p "$out"
out=$(cd "$out" && pwd)
db=${2:-"$out/vulndb"}
go version > "$out/compiler.txt"
go env -json GOVERSION GOOS GOARCH CGO_ENABLED GOFLAGS GOWORK GOTOOLCHAIN > "$out/environment.json"
printf 'default (no additional build tags)\n' > "$out/build-tags.txt"
go list -mod=readonly -m -json all | jq -s '.' > "$out/modules.json"
jq -e 'all(.[]; .Replace == null)' "$out/modules.json" >/dev/null

# Freeze the full public indexes and every record for this selected graph.
if [ "$#" -eq 1 ]; then
  mkdir -p "$db/index" "$db/ID"
  for index in db modules; do
    curl --disable --fail --silent --show-error --proto '=https' --retry 2 \
      --connect-timeout 15 --max-time 60 "https://vuln.go.dev/index/$index.json.gz" |
      gzip -d > "$db/index/$index.json"
  done
fi
jq -r --slurpfile modules "$out/modules.json" '
  ([$modules[0][].Path] + ["stdlib", "toolchain"]) as $paths |
  [.[] | select(.path as $p | $paths | index($p)) | .vulns[]] |
  unique_by(.id)[] | [.id, .modified] | @tsv
' "$db/index/modules.json" > "$out/required-records.tsv"
while IFS=$'\t' read -r id modified; do
  [[ "$id" =~ ^GO-[0-9]{4}-[0-9]+$ ]]
  if [ "$#" -eq 1 ]; then
    curl --disable --fail --silent --show-error --proto '=https' --retry 2 \
      --connect-timeout 15 --max-time 60 "https://vuln.go.dev/ID/$id.json.gz" |
      gzip -d > "$db/ID/$id.json"
  fi
  jq -e --arg id "$id" --arg modified "$modified" \
    '.id == $id and .modified == $modified' "$db/ID/$id.json" >/dev/null
done < "$out/required-records.tsv"
db=$(cd "$db" && pwd)
(
  cd "$db"
  find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum
) > "$out/database.sha256"
date -u +%FT%TZ > "$out/database-checked-at.txt"

scanner=$(command -v govulncheck)
sha256sum "$scanner" > "$out/scanner.sha256"
go version -m "$scanner" > "$out/scanner-build-info.txt"
status=0
govulncheck "-db=file://$db" -json ./... > "$out/scan.json" 2> "$out/scan.stderr" || status=$?
printf '%s\n' "$status" > "$out/scan.exit"
# Parse through EOF before interpreting findings. JSON exit zero alone is not clean.
jq -s '.' "$out/scan.json" > "$out/scan-array.json"
jq -e --arg compiler "$(go env GOVERSION)" --arg db "file://$db" \
  --slurpfile database "$db/index/db.json" '
  .[0].config as $config |
  $config.protocol_version == "v1.0.0" and
  $config.scanner_name == "govulncheck" and $config.scanner_version == "v1.7.0" and
  $config.go_version == $compiler and $config.db == $db and
  $config.db_last_modified == $database[0].modified and
  $config.scan_mode == "source" and $config.scan_level == "symbol" and
  ([.[] | select(has("config"))] | length) == 1 and
  ([.[] | .SBOM? | select(. != null)] | length) == 1 and
  all(.[] | .SBOM? | select(. != null); .go_version == $compiler and (.roots | length) > 0)
' "$out/scan-array.json" >/dev/null
jq '{
  config: .[0].config,
  records: [.[] | .osv? | select(. != null)],
  called: [.[] | .finding? | select(.trace[0].function? != null)],
  imported: [.[] | .finding? | select(.trace[0].package? != null and .trace[0].function? == null)],
  module_only: [.[] | .finding? | select(. != null and .trace[0].package? == null)]
}' "$out/scan-array.json" > "$out/result.json"
test "$status" -eq 0
review_status=0
if ! jq -e '(.called | length) == 0 and (.imported | length) == 0 and (.module_only | length) == 0' "$out/result.json" >/dev/null; then
  review_status=1
fi
printf '%s\n' "$review_status" > "$out/all-finding-review.exit"
jq '{
  called_records: (.called | length),
  imported_records: (.imported | length),
  module_records: (.module_only | length),
  called, imported, module_only
}' "$out/result.json"
if [ "$review_status" -ne 0 ]; then
  printf 'advisory review required: all findings remain recorded; symbol-gate success is not advisory clearance.\n'
fi
# Keep the existing symbol-level gate separate from review of every finding.
jq -e '(.called | length) == 0' "$out/result.json" >/dev/null
