#!/usr/bin/env bash
#
# 5.e2e-live.sh — the live testnet suite. Real transactions, real ETH.
#
# Credentials come out of the pigfox2 environment file and are exported into
# the child process. They are never printed: this script echoes variable names
# and never variable values, and the tool it runs redacts the key in every
# string it produces.
#
# Any arguments are passed through to `go test`, so a single test can be run:
#   scripts/5.e2e-live.sh -run TestT3
#
# Usage: scripts/5.e2e-live.sh [go test args...]
set -euo pipefail
cd "$(dirname "$0")/.."

ENV_FILE="${PIGFOX_ENV_FILE:-$HOME/Documents/pigfox2/.env}"

if [ -f "$ENV_FILE" ]; then
  echo "== sourcing credentials from $ENV_FILE (values are never printed)"
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
else
  echo "== $ENV_FILE not found; relying on the ambient environment"
fi

# The demo deployer is both ends of a same-chain transfer: it sends to itself,
# so the only ETH that leaves the account is gas.
export BRIDGE_SOURCE_ADDR="${BRIDGE_SOURCE_ADDR:-${DEMO_DEPLOYER_ADDR:-}}"
export BRIDGE_DEST_ADDR="${BRIDGE_DEST_ADDR:-${DEMO_DEPLOYER_ADDR:-}}"
export BRIDGE_SOURCE_PK="${BRIDGE_SOURCE_PK:-${DEMO_DEPLOYER_PK:-}}"

# The chain pair the suite runs against. The tests take a settlement layer and
# an OP Stack rollup and name no network of their own, so pointing them at a
# different pair is these four variables and nothing else.
#
# The default is Ethereum Sepolia and OP Sepolia, chosen because neither the
# routing nor the address resolution has ever heard of them: a pass on that
# pair is evidence the tool works on chains it was not built against.
export BRIDGE_E2E_L1_CHAIN_ID="${BRIDGE_E2E_L1_CHAIN_ID:-11155111}"
export BRIDGE_E2E_L1_RPC_URL="${BRIDGE_E2E_L1_RPC_URL:-https://ethereum-sepolia-rpc.publicnode.com}"
export BRIDGE_E2E_L2_CHAIN_ID="${BRIDGE_E2E_L2_CHAIN_ID:-11155420}"
export BRIDGE_E2E_L2_RPC_URL="${BRIDGE_E2E_L2_RPC_URL:-https://sepolia.optimism.io}"

echo "== chain pair"
echo "   L1: chain ${BRIDGE_E2E_L1_CHAIN_ID}"
echo "   L2: chain ${BRIDGE_E2E_L2_CHAIN_ID}"

echo "== exported (names only)"
for name in BRIDGE_SOURCE_ADDR BRIDGE_DEST_ADDR BRIDGE_SOURCE_PK BRIDGE_E2E_L1_RPC_URL BRIDGE_E2E_L2_RPC_URL; do
  if [ -n "${!name:-}" ]; then
    echo "   $name: set"
  else
    echo "   $name: MISSING — the tests that need it will skip"
  fi
done

echo
echo "== live E2E (this spends testnet ETH)"
go test -tags e2e -count=1 -v -timeout 40m "$@" ./e2e/...
