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

// LegacyERC20ETH is the sentinel token address that means "ETH" to
// L2StandardBridge.withdrawTo.
//
// This is one of the two addresses in this tool that are hard-coded and stay
// that way, because it is a fact about the protocol rather than about any one
// deployment of it. The other, the predeploy layout, lives in internal/opstack.
// Every address that identifies a particular chain's bridge is discovered at
// runtime instead.
//
// This is Predeploys.LEGACY_ERC20_ETH. The other sentinel in circulation,
// 0xEeee...EEeE (Constants.ETHER), is *not* accepted by the L2StandardBridge
// deployed on Base Sepolia: an eth_call with it reverts, while this address
// succeeds. The deployed bridge there reports version 1.3.0.
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
	// EnvSourceRPCURL and EnvDestRPCURL are named for the part each endpoint
	// plays in the transfer, not for its layer. Which side of a bridge route is
	// the L1 and which is the rollup is discovered from the chains themselves,
	// so it cannot be a precondition of reading the configuration.
	EnvSourceRPCURL = "BRIDGE_SOURCE_RPC_URL"
	EnvDestRPCURL   = "BRIDGE_DEST_RPC_URL"
	// EnvWithdrawalsDir is optional and defaults to DefaultWithdrawalsDir.
	EnvWithdrawalsDir = "BRIDGE_WITHDRAWALS_DIR"

	// Address overrides. All optional, and all a last resort: the addresses
	// are discovered from the chains, and an operator who sets one of these is
	// asserting something the chain disagrees with or cannot be asked about.
	// They exist because a chain running contracts this tool cannot read
	// should be usable by someone who knows the addresses, not blocked.
	EnvL1StandardBridge = "BRIDGE_L1_STANDARD_BRIDGE_ADDRESS"
	EnvL2StandardBridge = "BRIDGE_L2_STANDARD_BRIDGE_ADDRESS"
	EnvOptimismPortal   = "BRIDGE_OPTIMISM_PORTAL_ADDRESS"
)

// redacted is what stands in for the private key everywhere the configuration
// is rendered. The key is never formatted, logged or wrapped into an error.
const redacted = "[REDACTED]"
