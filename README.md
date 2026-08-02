# eth-bridge-go

A small, honest Go tool for moving ETH within one EVM chain, or between an L1
and an OP Stack L2 anchored to it.

It talks to the chains directly — plain JSON-RPC through `go-ethereum`, and for
the bridge paths the Standard Bridge contracts themselves. There is no forked
node, no Optimism SDK, and no `abigen` step.

**It knows no chain IDs and no contract addresses.** Which pairs of chains can
be bridged, and which contracts to use, are worked out by asking the chains at
run time. There is no allowlist to add your network to.

---

## What is implemented

| Route | Scope | Status |
|---|---|---|
| Same-chain transfer | **any EVM chain** | **implemented** |
| **L1 → L2 deposit** | **any L1 → any OP Stack L2 anchored to it** | **implemented**, via the Standard Bridge |
| **L2 → L1 withdrawal** | **any OP Stack L2 → the L1 it is anchored to** | **initiate only** — see the warning below |
| L2 → L2 | — | **not possible with this tool**, and not a missing feature — see below |

Same-chain is unrestricted because a plain transfer is an EIP-155 value transfer
that depends on no bridge contract and no pairing. The only thing that has to be
true is that the endpoint really serves the chain ID you configured, which the
tool checks before it signs.

The two bridge routes are restricted to a **paired** L1 and OP Stack L2 — the
rollup and the settlement layer it actually posts to. Pointing the tool at an
arbitrary pair of chains is refused with the reason, not with a revert.

### Why there is no L2 → L2

The Standard Bridge settles through the L1 that both chains share. It has no
mechanism to move value directly from one rollup to another, so "OP Sepolia to
Base Sepolia" is not a route this tool declines to implement — it is not a
thing the Standard Bridge does. Doing it needs a third-party message protocol
(LayerZero, CCIP, Hyperlane), which is a different protocol and a different
tool. The route resolver refuses that pair and says so.

### The withdrawal is only started, not finished

`bridge send` on the withdrawal route sends the **first of three** transactions.
It does not move ETH to L1, and this tool performs neither of the other two:

1. **initiate** — `L2StandardBridge.withdrawTo` on L2. This is what ships.
2. **prove** — on L1, once an output root covering the L2 block is published.
   **Not implemented.**
3. **finalize** — on L1, after the fault-proof window, roughly **7 days** on
   the OP Stack testnets. **Not implemented.**

Prove and finalize need a machine, a funded L1 account and the saved parameters
*a week later*. Shipping a command that initiates a withdrawal and leaves the
operator to work the rest out is not a bridge; it is half of one with a homework
assignment attached. So the tool says exactly that, in the output, every time.

What it does do is capture everything proving will need — nonce, sender, target,
value, gas limit, data, withdrawal hash and L2 block number, parsed from the
`MessagePassed` event — and write it to `./withdrawals/<l2TxHash>.json`. Those
parameters are not recoverable from the chain by this tool. If the write fails,
the whole operation fails: a withdrawal whose parameters were lost cannot be
completed at all.

Every 256-bit value in that file is a **decimal string**, not a JSON number. The
message nonce is routinely above 2^53, where most JSON readers would silently
corrupt it.

### How the contract addresses are found

Nothing in this tool holds the bridge address of any particular chain. Each run
derives them, starting from the two OP Stack predeploys — which are fixed by the
specification and identical on every rollup:

```
L2StandardBridge.otherBridge()       -> L1StandardBridge
L1StandardBridge.messenger()         -> L1CrossDomainMessenger
L1CrossDomainMessenger.portal()      -> OptimismPortal
```

Getter names are not stable across OP Stack contract versions, so each hop tries
both spellings it has been known by — `otherBridge()` / `OTHER_BRIDGE()`,
`messenger()` / `MESSENGER()`, `portal()` / `PORTAL()` — as raw four-byte
selectors computed from the signature, and takes the first that returns a
non-zero address **with code deployed at it** on the chain that should be
hosting it.

Three checks all have to pass, and each failure names the hop and both chain
IDs:

1. the L2 carries code at both predeploys — otherwise it is not an OP Stack L2;
2. the derived L1 bridge has code on the L1 supplied — otherwise the two chains
   are not paired;
3. the derived portal has code on that L1 too.

So pointing this at Polygon and Arbitrum produces *"chain 137 is not an OP Stack
L2"*, not a revert from somewhere inside a call.

Resolution order, and the tool prints which one answered before it sends
anything:

| Source | When |
|---|---|
| `override` | you set one of the `BRIDGE_*_ADDRESS` variables |
| `discovery` | the normal case: derived from the chains |
| `registry` | **only** if the chains could not be reached |

The registry is a small vendored Superchain Registry snapshot in
`internal/registry/registry.json`. Every address in it was itself produced by
running this tool's discovery against the live chain, so it is a cached answer
to the same question rather than a second opinion. It currently holds the five
OP Stack **Sepolia** chains that could be verified: Base, OP, Zora, Mode and
Unichain. **Mainnet rows are deliberately absent** — they could not be verified
under this repository's no-mainnet testing rule, and an unverified address in a
fallback the tool sends ETH to is worse than no fallback at all.

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
one, so the live E2E fetches the derived hash back from the rollup and fails if
it does not resolve to a successful transaction.

---

## Configuration

Read from the environment, and from nowhere else. Copy `.env.example` and
export it; `.env` is gitignored.

| Variable | Meaning |
|---|---|
| `BRIDGE_SOURCE_ADDR` | Funding account. |
| `BRIDGE_SOURCE_PK` | Its private key. Must derive to `BRIDGE_SOURCE_ADDR`, or the tool refuses to start. `0x` prefix optional. |
| `BRIDGE_DEST_ADDR` | Recipient on the destination chain. |
| `BRIDGE_SOURCE_CHAIN_ID` | Chain to send from. Any chain ID except `0`. |
| `BRIDGE_DEST_CHAIN_ID` | Chain to send to. Equal to the source for a plain transfer. |
| `BRIDGE_SOURCE_RPC_URL` | Endpoint for the source chain. |
| `BRIDGE_DEST_RPC_URL` | Endpoint for the destination chain. Required only when the two chain IDs differ. |
| `BRIDGE_WITHDRAWALS_DIR` | Optional. Where initiated withdrawals are recorded. Defaults to `./withdrawals`. |

The endpoints are named for the part each plays in the transfer, not for its
layer, because which side of a bridge route is the L1 and which is the rollup is
something the tool discovers — so it cannot be a precondition of reading the
configuration. When the route does cross chains both endpoints are mandatory:
classifying it means asking both chains what they are.

These three are optional, and are a last resort rather than a normal setting.
Each asserts an address the chain has not confirmed, and the tool prints
`(override)` beside it before sending anything:

| Variable | Meaning |
|---|---|
| `BRIDGE_L1_STANDARD_BRIDGE_ADDRESS` | Skip discovery of the L1 Standard Bridge. |
| `BRIDGE_L2_STANDARD_BRIDGE_ADDRESS` | Skip discovery of the L2 Standard Bridge. |
| `BRIDGE_OPTIMISM_PORTAL_ADDRESS` | Skip discovery of the OptimismPortal. |

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
$ export BRIDGE_SOURCE_CHAIN_ID=11155420          # OP Sepolia, to itself
$ export BRIDGE_DEST_CHAIN_ID=11155420
$ export BRIDGE_SOURCE_RPC_URL=https://sepolia.optimism.io

$ ./bridge send --amount 0.00001
route:  same-chain
amount: 10000000000000 wei
src tx: 0x…
        https://sepolia-optimism.etherscan.io/tx/0x…

$ ./bridge version
0.3.0
```

For a deposit, set the destination to the rollup and give both endpoints. The
addresses are discovered and printed with where they came from, before anything
is sent:

```console
$ export BRIDGE_SOURCE_CHAIN_ID=11155111          # Ethereum Sepolia
$ export BRIDGE_DEST_CHAIN_ID=11155420            # OP Sepolia
$ export BRIDGE_SOURCE_RPC_URL=https://ethereum-sepolia-rpc.publicnode.com
$ export BRIDGE_DEST_RPC_URL=https://sepolia.optimism.io

$ ./bridge send --amount 0.0005
l1 bridge:  0xFBb0621E0B23b5478B630BD55a5f21f67730B0F1 (discovery)
l2 bridge:  0x4200000000000000000000000000000000000010 (discovery)
portal:     0x16Fc5058F25648194471939df75CF27A2fdC48BC (discovery)
route:  deposit
amount: 500000000000000 wei
src tx: 0xadac5d2f99beb43dd2da10536e7f33a5758cdd7233cff51b2d6938c5856dafc7
        https://sepolia.etherscan.io/tx/0xadac5d2f…
dst tx: 0xfb7ef0cd72068a8559f605bfe6a8a1397c207bc7d86c4e993fffba3cc7ed5fb6
        https://sepolia-optimism.etherscan.io/tx/0xfb7ef0cd…
credit: 500000000000000 wei on chain 11155420
```

`--amount` is in ETH and must be plain decimal. `0x10` is rejected rather than
read as sixteen ETH.

Exit codes: `0` success, `1` the network or the chain refused, `2` you need to
retype the command.

### Verified on testnet

The E2E suite names no network. It takes an L1 and an OP Stack L2 from four
`BRIDGE_E2E_*` variables and runs the same unchanged tests against whatever it
is given. Every bridge route is resolved with the registry fallback **switched
off** and asserts that all three addresses report source `discovery`, so a pass
can only mean the addresses were derived from the chains.

Two chain pairs were run live. All receipts status `1`.

**Ethereum Sepolia (`11155111`) ↔ OP Sepolia (`11155420`)** — the primary
evidence. Neither chain appears in any routing or address-resolution code.
Discovered: L1 bridge `0xFBb0621E…30B0F1`, portal `0x16Fc5058…dC48BC`.

| Test | What | Transaction |
|---|---|---|
| T1 | Deposit, L1 side | [`0xadac5d…6dafc7`](https://sepolia.etherscan.io/tx/0xadac5d2f99beb43dd2da10536e7f33a5758cdd7233cff51b2d6938c5856dafc7) |
| T1 | Deposit, L2 side (hash *derived*, then fetched back) | [`0xfb7ef0…ed5fb6`](https://sepolia-optimism.etherscan.io/tx/0xfb7ef0cd72068a8559f605bfe6a8a1397c207bc7d86c4e993fffba3cc7ed5fb6) |
| T2 | Same-chain, OP Sepolia | [`0x12b723…470e90`](https://sepolia-optimism.etherscan.io/tx/0x12b7236e1e0f7888fe0875c9132e1d27fb3c0b39c445a20876ea3cbfa3470e90) |
| T3 | Same-chain, Ethereum Sepolia | [`0x2c5d53…a1b033`](https://sepolia.etherscan.io/tx/0x2c5d530fed754be45797586c41e12acbe9ae74cca10df8775ef88765e3a1b033) |
| T4 | Withdrawal, initiated on L2 | [`0xfbe276…f7c2e0`](https://sepolia-optimism.etherscan.io/tx/0xfbe276e77ce25eff71e719f982bf3d9dd310e8caed616ec608c2d4663ef7c2e0) |

**Ethereum Sepolia ↔ Base Sepolia (`84532`)** — the regression run, on the pair
this tool used to have hard-coded. Discovery reproduced both former constants
exactly: L1 bridge `0xfd0Bf71F…113120`, portal `0x49f53e41…459e85`.

| Test | What | Transaction |
|---|---|---|
| T1 | Deposit, L1 side | [`0xf6996d…4153e4`](https://sepolia.etherscan.io/tx/0xf6996d2f86a83adc7aa8043fd774d29b09dee009962fab099b8aeafd084153e4) |
| T1 | Deposit, L2 side (derived, then fetched back) | [`0xf04202…49ea2b`](https://sepolia.basescan.org/tx/0xf042020a61f97a7145fa1cbe3a96c17c4517cbdb0e668ac426165f775249ea2b) |
| T2 | Same-chain, Base Sepolia | [`0xefc66f…0d64457`](https://sepolia.basescan.org/tx/0xefc66f119a13ff724a71d6ad093d8ddf7ac4820a7915b6952314d07f90d64457) |
| T3 | Same-chain, Ethereum Sepolia | [`0x2a3859…d57974`](https://sepolia.etherscan.io/tx/0x2a3859ac713c0c8a75e8b2f88055431ac7ebd178b4cfd73b22b7563820d57974) |
| T4 | Withdrawal, initiated on L2 | [`0x1b1bab…b4119d`](https://sepolia.basescan.org/tx/0x1b1bab363eb133e9776d8312ec2056cb7230ee997148d7833ca1862a72b4119d) |

Each T1 bridged 0.0005 ETH and observed the full 500000000000000 wei credited on
the rollup, measured as a balance delta. Each T4 initiated a 0.00001 ETH
withdrawal and captured its proof parameters; **both of those withdrawals are
unfinished, and this tool will not finish them.**

What this does and does not prove. It shows the tool works on a chain pair it
was not built against, and that discovery agrees with the addresses that were
previously hard-coded. It does not show that every OP Stack chain works: only
these two pairs were run, and only on Sepolia.

---

## Design

The shape of this repository is driven by one constraint: **the coverage gate
requires exactly 100.0% statement coverage, with no exclusion list and no
`//nolint` anywhere in the tree.**

That is only achievable if untestable code is designed out rather than waived:

- **`internal/chain`** is the only package that imports `ethclient`. It declares
  a `Client` interface naming the ten RPC calls this tool makes and nothing
  else, using go-ethereum's own signatures — so `*ethclient.Client` satisfies it
  with no forwarding methods to drift out of date, and a signature change
  upstream breaks one compile-time assertion rather than the tree.
- **`internal/chain/fake`** implements that interface. The eight sequential
  calls are scripted as per-method queues; `CodeAt` and `CallContract` are keyed
  by what they read instead, because they model chain state rather than a
  sequence of events — and because an unscripted address holding no code, or a
  selector that reverts, are states discovery has to handle and so must be
  reachable without scripting. Every failure a live testnet will not reproduce
  on demand — a reverted transaction, a receipt that never lands, a node
  reporting the wrong chain ID, a getter this tool has never seen — is a unit
  test.
- **`cmd/bridge/main.go`** is three tokens of glue. All logic is in `run()`,
  which takes argv and both output streams as parameters and returns an exit
  code. One test swaps `osExit` and `os.Args` so `main()` itself is covered.
- **Signing and deposit encoding are interface points.** `bridge.Signer`
  defaults to a local key but can be replaced, which is what a KMS or hardware
  wallet would need. `bridge.DepositEncoder` is the same idea for the bridge
  ABI. Both also make their failure paths reachable.
- **`internal/store`** writes the withdrawal record. `encode` validates and
  renders in one function so that the "incomplete record" path is a real,
  reachable check rather than a dead error branch.
- **`internal/opstack`** owns the hand-written deposit and withdrawal ABIs, the
  L2 hash derivation, the `MessagePassed` decoding, and address discovery.
  `L2TxHash` joins parsing and hashing into one call so that callers have one
  error to handle rather than an unreachable second branch. The two predeploy
  addresses live here rather than in `internal/config`, because they are facts
  about the protocol rather than about any one deployment of it.
- **`internal/route`** takes an interface, not a client, so resolving a route —
  which now means probing two chains — is tested entirely offline against the
  fake, including the selector-variance and unpaired-chain paths that a live
  testnet cannot be asked to produce.
- **`internal/registry`** is the vendored snapshot. It is only ever a fallback,
  and a snapshot that fails to parse yields an empty table rather than a panic
  in an initialiser: a fallback that takes the process down on the way past is
  worse than one that simply has nothing to offer.

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

### v0.4.0 — prove and finalize

The missing two thirds of a withdrawal. Both are L1 transactions against the
`OptimismPortal`, and both need the JSON written by the initiate step:

- **prove** requires the withdrawal parameters plus a Merkle-Patricia proof of
  the `L2ToL1MessagePasser` storage slot, against an output root that covers the
  recorded `l2BlockNumber`.
- **finalize** requires only the parameters, but not until the fault-proof
  window has elapsed.

The awkward part is not the cryptography, it is the shape: this is a tool that
would have to be run again a week later, which means state, scheduling and a
story for what happens when the operator's machine is gone. That design decision
is worth making deliberately rather than bolting on.

Discovery already resolves and validates the `OptimismPortal` for the pair, so
prove and finalize would not need to be told where to send. That is the one
piece of the work already done.

Until then the tool initiates withdrawals, records what finishing one needs, and
says plainly that it is not finishing it.

### Registry coverage

`internal/registry/registry.json` holds only Sepolia rows, each derived live.
Mainnet rows are missing because they could not be verified without touching
mainnet. Adding one is running discovery against that chain and pasting what it
returns — not copying an address out of a documentation page.

---

## License

MIT — see [LICENSE](LICENSE).
