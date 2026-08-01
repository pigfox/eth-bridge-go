# Security

## This is testnet software

`eth-bridge-go` supports Ethereum Sepolia (`11155111`) and Base Sepolia
(`84532`), and nothing else. `internal/config` rejects any other chain ID at
load time. Do not point it at mainnet: it has not been reviewed for that, and
the chain allowlist exists so that a typo in `BRIDGE_SOURCE_CHAIN_ID` cannot
quietly move real value.

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

## Reporting

Open an issue at https://github.com/pigfox/eth-bridge-go/issues. Since this is
testnet-only tooling with no production deployment, there is no embargo process.
