package config

import "time"

// Chain IDs of the two testnets this tool bridges between. They are named
// here so that no call site carries a bare numeric literal.
const (
	// ChainIDEthSepolia is the Ethereum Sepolia L1 testnet.
	ChainIDEthSepolia uint64 = 11155111
	// ChainIDBaseSepolia is the Base Sepolia L2 testnet.
	ChainIDBaseSepolia uint64 = 84532
)

// Base Sepolia bridge contracts.
//
// Verified against docs.base.org and against the chain itself before the first
// live deposit: the L1 bridge reports version 2.8.0 and an OTHER_BRIDGE of
// 0x4200000000000000000000000000000000000010, which is the L2 standard bridge
// predeploy below.
const (
	// L1StandardBridgeSepolia is the Base Sepolia standard bridge, deployed on
	// Ethereum Sepolia.
	L1StandardBridgeSepolia = "0xfd0Bf71F60660E2f608ed56e1659C450eB113120"
	// OptimismPortalSepolia emits the TransactionDeposited event from which the
	// resulting L2 transaction hash is derived.
	OptimismPortalSepolia = "0x49f53e41452C74589E85cA1677426Ba426459e85"
	// L2StandardBridgePredeploy is the L2 side of the standard bridge.
	L2StandardBridgePredeploy = "0x4200000000000000000000000000000000000010"
	// L2ToL1MessagePasserPredeploy emits the MessagePassed event that carries
	// the parameters a withdrawal is later proved with.
	L2ToL1MessagePasserPredeploy = "0x4200000000000000000000000000000000000016"
)

// LegacyERC20ETH is the sentinel token address that means "ETH" to
// L2StandardBridge.withdrawTo.
//
// This is Predeploys.LEGACY_ERC20_ETH. The other sentinel in circulation,
// 0xEeee...EEeE (Constants.ETHER), is *not* accepted by the bridge deployed on
// Base Sepolia: an eth_call with it reverts, while this address succeeds. The
// deployed L2StandardBridge reports version 1.3.0.
const LegacyERC20ETH = "0xDeadDeAddeAddEAddeadDEaDDEAdDeaDDeAD0000"

// DefaultWithdrawMinGasLimit is the gas reserved for the withdrawal's eventual
// execution on L1.
const DefaultWithdrawMinGasLimit uint32 = 200000

// DefaultWithdrawalsDir is where initiated withdrawals are recorded.
const DefaultWithdrawalsDir = "withdrawals"

// DefaultDepositMinGasLimit is the gas made available to the deposit's
// execution on L2. 200k is the value the Base documentation uses for a simple
// ETH deposit; it is paid for on L1 as calldata, not spent on L2 unless needed.
const DefaultDepositMinGasLimit uint32 = 200000

// DefaultGasMarginPercent is added to every gas estimate before it is used as
// a limit.
//
// This is not belt-and-braces. An OP Stack deposit reaches a call made through
// SafeCall.callWithMinGas, which forwards 63/64 of the gas remaining at that
// point and requires a floor to be met. Gas consumption is therefore not
// monotonic in the limit supplied, and eth_estimateGas — which binary-searches
// for the smallest limit that lets the top-level call succeed — can return a
// value at which the real transaction reverts. Unused gas is refunded, so the
// margin costs nothing when it is not needed.
const DefaultGasMarginPercent uint64 = 30

// Timing defaults for the confirmation loops.
const (
	// DefaultConfirmTimeout bounds how long a bridge operation waits for a
	// receipt or for a destination-side balance delta before giving up.
	DefaultConfirmTimeout = 10 * time.Minute
	// DefaultPollInterval is the gap between polls inside that window.
	DefaultPollInterval = 5 * time.Second
)

// Names of the environment variables Load reads. Declared as constants so the
// loader, the error strings and the tests all refer to the same spelling.
const (
	EnvSourceAddr    = "BRIDGE_SOURCE_ADDR"
	EnvSourcePK      = "BRIDGE_SOURCE_PK"
	EnvDestAddr      = "BRIDGE_DEST_ADDR"
	EnvSourceChainID = "BRIDGE_SOURCE_CHAIN_ID"
	EnvDestChainID   = "BRIDGE_DEST_CHAIN_ID"
	EnvL1RPCURL      = "BRIDGE_L1_RPC_URL"
	EnvL2RPCURL      = "BRIDGE_L2_RPC_URL"
	// EnvWithdrawalsDir is optional and defaults to DefaultWithdrawalsDir.
	EnvWithdrawalsDir = "BRIDGE_WITHDRAWALS_DIR"
)

// redacted is what stands in for the private key everywhere the configuration
// is rendered. The key is never formatted, logged or wrapped into an error.
const redacted = "[REDACTED]"
