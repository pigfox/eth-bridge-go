#!/usr/bin/env bash
#
# 3.test.sh — the unit suite, under the race detector and in shuffled order.
# E2E tests carry the `e2e` build tag and are not part of this run.
set -euo pipefail
cd "$(dirname "$0")/.."
go test -race -shuffle=on -count=1 ./...
