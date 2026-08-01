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
)

// redacted is what stands in for the private key everywhere the configuration
// is rendered. The key is never formatted, logged or wrapped into an error.
const redacted = "[REDACTED]"
