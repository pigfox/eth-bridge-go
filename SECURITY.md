# Security

## This is testnet software

**There is no longer a chain allowlist.** Earlier versions accepted only
Ethereum Sepolia and Base Sepolia and rejected every other chain ID at load
time. That check is gone: `eth-bridge-go` now works out what a pair of chains
is by asking them, which is what makes it usable on networks it was not built
against — and which also means **a typo in `BRIDGE_SOURCE_CHAIN_ID` is no longer
caught by a list.** If you have a funded mainnet key in the environment and you
mistype a chain ID, nothing here will stop you.

What still holds:

- The endpoint must actually serve the chain ID you configured. `verifyChain`
  reads `eth_chainId` and refuses to sign against a mismatch, so a chain ID
  that disagrees with its RPC URL fails before any transaction is built.
- A bridge route must be a genuinely paired L1 and OP Stack L2, proven by
  reading both chains. An unpaired pair is refused, not attempted.
- The addresses a deposit or withdrawal is sent to are derived from the chains
  rather than assumed, and the tool prints each one, and where it came from,
  before it sends anything. `(override)` beside an address means the operator
  asserted it and the chain has not confirmed it.

This tool has still not been reviewed for mainnet use. Treat it as testnet
software, and keep mainnet keys out of the environment you run it in.

## Never commit a private key

`BRIDGE_SOURCE_PK` is a raw secp256k1 key. It is read from the environment and
nowhere else.

- `.env` and `.env.*` are gitignored. Only `.env.example`, which contains zeroes,
  is tracked.
- `scripts/2.git-push-dev.sh` refuses to push if `.env` is tracked.
- The key is parsed once into an `*ecdsa.PrivateKey` and held in an unexported
  field. `Config.String()` prints `[REDACTED]` in its place.
- No error value in `internal/config` interpolates the key. The parse-failure
  path in particular discards the underlying error, because that error can quote
  the input it failed on.
- RPC URLs are redacted down to scheme and host wherever a `Config` is
  formatted, since provider endpoints routinely carry an API key in the path.
- `scripts/5.e2e-live.sh` exports credentials into the test process and prints
  variable *names* only, never values.

If you find a leak of any of the above in a log line, an error string, or a
tracked file, that is a bug — please report it.

## Key-derivation check

`Config` will not load unless `BRIDGE_SOURCE_PK` derives to exactly
`BRIDGE_SOURCE_ADDR`. A mismatch is a hard error, not a warning: signing with a
key whose address is not the one the operator named means spending an account
they did not intend to spend from.

## Withdrawal records are sensitive

`./withdrawals/<l2TxHash>.json` is the only copy of what proving a withdrawal
later needs, and it describes ETH that has not been claimed yet. The files are
written `0600`, the directory `0750`, and `withdrawals/` is gitignored. Losing
one does not let anyone else claim the withdrawal, but it does mean you cannot.

## Reporting

Open an issue at https://github.com/pigfox/eth-bridge-go/issues. Since this is
testnet-only tooling with no production deployment, there is no embargo process.
