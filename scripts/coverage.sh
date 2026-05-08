#!/usr/bin/env sh
set -eu

threshold="${1:-35.0}"
profile="${COVERAGE_PROFILE:-coverage.out}"

go test ./... -coverprofile="$profile" -covermode=atomic
total="$(go tool cover -func="$profile" | awk '/^total:/ { sub(/%/, "", $3); print $3 }')"

awk -v total="$total" -v threshold="$threshold" 'BEGIN {
	if (total + 0 < threshold + 0) {
		printf "coverage %.1f%% below threshold %.1f%%\n", total, threshold
		exit 1
	}
	printf "coverage %.1f%% >= %.1f%%\n", total, threshold
}'
