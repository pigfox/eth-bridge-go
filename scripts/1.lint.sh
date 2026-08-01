#!/usr/bin/env bash
#
# 1.lint.sh — static analysis. Zero issues is the standard; there is no
# suppression list, so a new finding is fixed rather than excused.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== gofmt"
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "not gofmt-clean:"
  echo "$unformatted"
  exit 1
fi
echo "clean"

echo
echo "== go vet"
go vet ./...

echo
echo "== golangci-lint"
if ! command -v golangci-lint >/dev/null 2>&1; then
  echo "golangci-lint is not installed; install it to run the full lint gate." >&2
  exit 1
fi
golangci-lint run --timeout=5m
echo "lint: 0 issues"
