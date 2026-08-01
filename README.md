# eth-bridge-go

A small, honest Go tool for moving testnet ETH between **Ethereum Sepolia**
(`11155111`) and **Base Sepolia** (`84532`).

It talks to the chains directly — plain JSON-RPC through `go-ethereum`, and for
the deposit path the Base Standard Bridge contract itself. There is no forked
node, no Optimism SDK, and no `abigen` step.

---

## What is implemented

| Route | Status |
|---|---|
| Same-chain transfer on Base Sepolia (`84532` → `84532`) | **implemented** |
| Same-chain transfer on Eth Sepolia (`11155111` → `11155111`) | **implemented** |
| **L1 → L2 deposit** (`11155111` → `84532`) | **implemented** — via the Base Standard Bridge |
| L2 → L1 withdrawal (`84532` → `11155111`) | recognised, **not implemented** — see roadmap |

Withdrawal is resolved by `internal/route` and then fails with
`route.ErrNotImplemented`. It is named rather than silently missing so that
`bridge send` tells you what it will not do instead of doing something
surprising.

**What this does not do:** it does not withdraw. Moving ETH from Base Sepolia
back to Ethereum Sepolia is not in this release, and the roadmap below explains
why it will not ship as a half-measure.

### How a deposit is confirmed

`depositETHTo` on the L1 Standard Bridge, then two independent confirmations:

1. The **L1 receipt** must come back with status `1`.
2. The **destination balance on L2 must actually increase**. A successful L1
   receipt only means the deposit was accepted for relay — it is not evidence
   that the ETH arrived. The credit is measured as a delta against a balance
   read before anything was sent.

The tool also **derives** the hash of the L2 transaction the deposit produces,
from the `TransactionDeposited` log in the L1 receipt, following the OP Stack
source-hash rule:

```
sourceHash = keccak256(uint256(0) ++ keccak256(l1BlockHash ++ uint256(logIndex)))
l2TxHash   = keccak256(0x7E ++ rlp([sourceHash, from, to, mint, value, gas, false, data]))
```

That derivation is hand-written in `internal/opstack` against the specification.
Deriving a hash that merely looks well-formed would be worse than not printing
one, so the live E2E fetches the derived hash back from Base Sepolia and fails
if it does not resolve to a successful transaction.

---

## Configuration

Read from the environment, and from nowhere else. Copy `.env.example` and
export it; `.env` is gitignored.

| Variable | Meaning |
|---|---|
| `BRIDGE_SOURCE_ADDR` | Funding account. |
| `BRIDGE_SOURCE_PK` | Its private key. Must derive to `BRIDGE_SOURCE_ADDR`, or the tool refuses to start. `0x` prefix optional. |
| `BRIDGE_DEST_ADDR` | Recipient on the destination chain. |
| `BRIDGE_SOURCE_CHAIN_ID` | `11155111` or `84532`. Any other value is rejected. |
| `BRIDGE_DEST_CHAIN_ID` | `11155111` or `84532`. Any other value is rejected. |
| `BRIDGE_L1_RPC_URL` | Ethereum Sepolia endpoint. Required only if the route touches L1. |
| `BRIDGE_L2_RPC_URL` | Base Sepolia endpoint. Required only if the route touches L2. |

The last two are the mechanically-required additions to the five configuration
items the tool is specified around — you cannot reach a chain without an
endpoint for it. Only the endpoints the route actually uses are demanded, so a
same-chain L2 transfer does not fail for want of an L1 URL.

The private key is never rendered. `Config.String()` prints `[REDACTED]`, RPC
URLs are cut back to scheme and host, and no error path in `internal/config`
interpolates the key. See [SECURITY.md](SECURITY.md).

---

## Quickstart

```console
$ go build ./cmd/bridge

$ export BRIDGE_SOURCE_ADDR=0x...        # your funded testnet account
$ export BRIDGE_SOURCE_PK=...            # its key; never commit this
$ export BRIDGE_DEST_ADDR=0x...
$ export BRIDGE_SOURCE_CHAIN_ID=84532
$ export BRIDGE_DEST_CHAIN_ID=84532
$ export BRIDGE_L2_RPC_URL=https://base-sepolia-rpc.publicnode.com

$ ./bridge send --amount 0.00001
route:  same-chain
amount: 10000000000000 wei
tx:     0x…
https://sepolia.basescan.org/tx/0x…

$ ./bridge version
0.2.0
```

For a deposit, set `BRIDGE_SOURCE_CHAIN_ID=11155111`,
`BRIDGE_DEST_CHAIN_ID=84532` and both RPC URLs. Output then carries both sides:

```console
$ ./bridge send --amount 0.0005
route:  deposit
amount: 500000000000000 wei
src tx: 0x75a3476eaf3d1fa5fd730fb90c9f88a0817f156a2af5193268342a3430e0a067
        https://sepolia.etherscan.io/tx/0x75a3476e…
dst tx: 0x72eceb7b5bf29181dca54ec420c98098c5a3756ec522f47e551f5e3498a9696d
        https://sepolia.basescan.org/tx/0x72eceb7b…
credit: 500000000000000 wei on chain 84532
```

`--amount` is in ETH and must be plain decimal. `0x10` is rejected rather than
read as sixteen ETH.

Exit codes: `0` success, `1` the network or the chain refused, `2` you need to
retype the command.

### Verified on testnet

Both same-chain routes were run live from `scripts/5.e2e-live.sh`, each with
receipt status `1`:

| Test | What | Transaction |
|---|---|---|
| T1 | Same-chain, Base Sepolia | [`0x88f28f…2b86d6`](https://sepolia.basescan.org/tx/0x88f28f33fc37a8590ac837131fdb342718eafaed0827117a2b742bc7fb2b86d6) |
| T2 | Same-chain, Ethereum Sepolia | [`0xc2e0dc…fd11c6`](https://sepolia.etherscan.io/tx/0xc2e0dc1450c4d0157efe906d58da9d0401bf895a1a64c6c52fd86201befd11c6) |
| T3 | Deposit, L1 side | [`0x75a347…e0a067`](https://sepolia.etherscan.io/tx/0x75a3476eaf3d1fa5fd730fb90c9f88a0817f156a2af5193268342a3430e0a067) |
| T3 | Deposit, L2 side (hash *derived*, then fetched back) | [`0x72eceb…a9696d`](https://sepolia.basescan.org/tx/0x72eceb7b5bf29181dca54ec420c98098c5a3756ec522f47e551f5e3498a9696d) |

T3 bridged 0.0005 ETH from Ethereum Sepolia to Base Sepolia and observed the
full 500000000000000 wei credited on L2.

---

## Design

The shape of this repository is driven by one constraint: **the coverage gate
requires exactly 100.0% statement coverage, with no exclusion list and no
`//nolint` anywhere in the tree.**

That is only achievable if untestable code is designed out rather than waived:

- **`internal/chain`** is the only package that imports `ethclient`. It declares
  a `Client` interface naming the eight RPC calls this tool makes and nothing
  else, using go-ethereum's own signatures — so `*ethclient.Client` satisfies it
  with no forwarding methods to drift out of date, and a signature change
  upstream breaks one compile-time assertion rather than the tree.
- **`internal/chain/fake`** implements that interface as a scriptable queue of
  outcomes. Every failure a live testnet will not reproduce on demand — a
  reverted transaction, a receipt that never lands, a node reporting the wrong
  chain ID — is a unit test.
- **`cmd/bridge/main.go`** is three tokens of glue. All logic is in `run()`,
  which takes argv and both output streams as parameters and returns an exit
  code. One test swaps `osExit` and `os.Args` so `main()` itself is covered.
- **Signing and deposit encoding are interface points.** `bridge.Signer`
  defaults to a local key but can be replaced, which is what a KMS or hardware
  wallet would need. `bridge.DepositEncoder` is the same idea for the bridge
  ABI. Both also make their failure paths reachable.
- **`internal/opstack`** owns the hand-written deposit ABI and the L2 hash
  derivation. `L2TxHash` joins parsing and hashing into one call so that
  callers have one error to handle rather than an unreachable second branch.

Where a branch turned out to be genuinely unreachable, the code was restructured
until it was not. `dispatch` resolves the route itself so its error paths can be
driven from a test, and `dialChain` exists so that `RPCFor`'s error is reachable.

---

## Gates

```console
$ scripts/1.lint.sh            # gofmt, go vet, golangci-lint — zero issues
$ scripts/3.test.sh            # go test -race -shuffle=on
$ scripts/4.coverage-gate.sh   # total must be exactly 100.0%
$ scripts/5.e2e-live.sh        # live testnet, spends real testnet ETH
```

CI runs lint and the coverage gate on every push. It does **not** run the live
E2E suite: that needs a funded private key, which is not something CI should
hold. Those tests carry the `e2e` build tag, so `go test ./...` and the coverage
gate never see them, and they `t.Skip` with a clear message when their
environment is absent.

---

## A note on gas

Every gas estimate is inflated by `DefaultGasMarginPercent` (30%) before it is
used as a limit. This is not superstition. An OP Stack deposit reaches a call
made through `SafeCall.callWithMinGas`, which forwards 63/64 of the gas
remaining at that point and requires a floor to be met. Gas consumption is
therefore **not monotonic** in the limit supplied, and `eth_estimateGas` —
which binary-searches for the smallest limit at which the top-level call
succeeds — can and does return a value at which the real transaction reverts.

This was not a theoretical concern. The first live deposit attempt was sent at
exactly the estimate, 616109 gas, and
[reverted after burning 575002 of it](https://sepolia.etherscan.io/tx/0xc587328908ff6ae808463f2186bfd4546d3634531f58b97b6d8676997ae10a96),
while an `eth_call` replay of the identical call at the parent block succeeded.
Unused gas is refunded, so the margin costs nothing when it is not needed.

---

## Roadmap

### v0.3.0 — L2 → L1 withdrawal, initiate only

`L2StandardBridge.withdrawTo`, parsing `MessagePassed` and writing the
withdrawal proof parameters to disk.

**Prove and finalize will not be provided.** An Optimism-stack withdrawal needs
a second transaction after the fault-proof window — roughly seven days — and a
third after that. A CLI that initiates a withdrawal and then requires the same
operator to still have the JSON file and a funded L1 account a week later is not
a bridge; it is half of one with a homework assignment attached. Until this can
prove and finalize, it will not pretend to withdraw.

---

## License

MIT — see [LICENSE](LICENSE).
