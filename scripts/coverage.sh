#!/usr/bin/env bash
# Runs the unit tests with the race detector and enforces a coverage floor over internal/.
set -euo pipefail

THRESHOLD=${THRESHOLD:-85}

go test -race -covermode=atomic -coverprofile=coverage.out ./...

# cmd/ is thin wiring and the *test packages are test doubles: exclude both from the gate.
# The leading "mode:" line does not match the pattern, so it is preserved.
grep -v -E '/(cmd|capturetest|uploadertest)/' coverage.out > coverage.internal.out

total=$(go tool cover -func=coverage.internal.out | awk '/^total:/ {print substr($3, 1, length($3)-1)}')
echo "internal coverage: ${total}% (threshold ${THRESHOLD}%)"

go tool cover -html=coverage.out -o coverage.html

if ! awk -v t="$total" -v th="$THRESHOLD" 'BEGIN { exit (t+0 >= th+0) ? 0 : 1 }'; then
  echo "FAIL: coverage ${total}% is below the ${THRESHOLD}% threshold"
  go tool cover -func=coverage.internal.out | sort -k3 -n | head -20
  exit 1
fi
echo "PASS: coverage gate"
