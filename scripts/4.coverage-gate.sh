#!/usr/bin/env bash
#
# 4.coverage-gate.sh — total statement coverage must be exactly 100.0%.
#
# There is no exclusion list and no //nolint anywhere in the tree. A line that
# cannot be reached by a test is treated as a design problem and the code is
# restructured until it can be — which is why the chain client sits behind a
# narrow interface and why main() is three tokens of glue around a testable
# run(). E2E tests are tagged `e2e` and excluded from this run: they need funded
# keys and a live testnet, so folding them in would make the number depend on
# whether someone had ETH that day.
set -euo pipefail
cd "$(dirname "$0")/.."

PROFILE="${COVERAGE_PROFILE:-coverage.out}"

go test ./... -covermode=count -coverprofile="$PROFILE"

echo
echo "== per-function coverage"
go tool cover -func="$PROFILE"

TOTAL_LINE="$(go tool cover -func="$PROFILE" | tail -1)"
TOTAL="$(echo "$TOTAL_LINE" | awk '{print $NF}')"

echo
if [ "$TOTAL" != "100.0%" ]; then
  echo "COVERAGE GATE: FAIL — total is $TOTAL, want exactly 100.0%"
  echo
  echo "functions below 100%:"
  go tool cover -func="$PROFILE" | grep -v '100.0%$' | grep -v '^total:' || true
  exit 1
fi
echo "COVERAGE GATE: PASS — total $TOTAL"
