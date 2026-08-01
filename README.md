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
| L1 → L2 deposit (`11155111` → `84532`) | recognised, **not implemented** — see roadmap |
| L2 → L1 withdrawal (`84532` → `11155111`) | recognised, **not implemented** — see roadmap |

The two bridged routes are resolved by `internal/route` and then fail with
`route.ErrNotImplemented`. They are named rather than silently missing so that
`bridge send` tells you what it will not do instead of doing something
surprising.

**What this does not do, in v0.1.0:** it does not bridge. A same-chain transfer
is a value transfer within one chain; it exercises the whole config, signing,
broadcast and confirmation path that the deposit will reuse, but it does not
cross a domain. If you came here for a working L1→L2 deposit, it is not in this
release.

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
0.1.0
```

`--amount` is in ETH and must be plain decimal. `0x10` is rejected rather than
read as sixteen ETH.

Exit codes: `0` success, `1` the network or the chain refused, `2` you need to
retype the command.

### Verified on testnet

Both same-chain routes were run live from `scripts/5.e2e-live.sh`, each with
receipt status `1`:

| Test | Network | Transaction |
|---|---|---|
| T1 | Base Sepolia | [`0x5b1376…f1d22f`](https://sepolia.basescan.org/tx/0x5b1376017e623190ecc3cba0b5a3b7086f64eec5bec5b3a2b3346dda45f1d22f) |
| T2 | Ethereum Sepolia | [`0xbfd088…80bf61`](https://sepolia.etherscan.io/tx/0xbfd088fa26faefe85f314e7f6c48e4650d3624dfcc0211f3bc5dca9b7980bf61) |

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
- **Signing is an interface point.** `bridge.Signer` defaults to a local key but
  can be replaced, which is what a KMS or hardware wallet would need — and it
  makes the signing-failure path reachable.

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

## Roadmap

### v0.2.0 — L1 → L2 deposit

Deposit via the Base Standard Bridge on Sepolia
(`L1StandardBridge.depositETHTo`), waiting on the L1 receipt and then polling
the L2 destination balance for the credit. Route `KindDeposit` is already wired
through config, resolution and dispatch; only the call itself is missing.

### v0.2.0 — L2 → L1 withdrawal, initiate only

`L2StandardBridge.withdrawTo`, parsing the `MessagePassed` event and writing the
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
