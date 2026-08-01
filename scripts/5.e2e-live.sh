#!/usr/bin/env bash
#
# 5.e2e-live.sh — the live testnet suite. Real transactions, real ETH.
#
# Credentials come out of the pigfox2 environment file and are exported into
# the child process. They are never printed: this script echoes variable names
# and never variable values, and the tool it runs redacts the key in every
# string it produces.
#
# Usage: scripts/5.e2e-live.sh
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

# ZK_NODE_RPC_URL is the Base Sepolia endpoint already configured in pigfox2.
# There is no Ethereum Sepolia endpoint there, so a public one is the default.
export BRIDGE_L2_RPC_URL="${BRIDGE_L2_RPC_URL:-${ZK_NODE_RPC_URL:-https://base-sepolia-rpc.publicnode.com}}"
export BRIDGE_L1_RPC_URL="${BRIDGE_L1_RPC_URL:-https://ethereum-sepolia-rpc.publicnode.com}"

echo "== exported (names only)"
for name in BRIDGE_SOURCE_ADDR BRIDGE_DEST_ADDR BRIDGE_SOURCE_PK BRIDGE_L1_RPC_URL BRIDGE_L2_RPC_URL; do
  if [ -n "${!name:-}" ]; then
    echo "   $name: set"
  else
    echo "   $name: MISSING — the tests that need it will skip"
  fi
done

echo
echo "== live E2E (this spends testnet ETH)"
go test -tags e2e -count=1 -v -timeout 20m ./e2e/...
