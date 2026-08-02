// Package config loads and validates the bridge's runtime configuration from
// the environment.
//
// The private key is the reason this package is careful. It is parsed once,
// held as a key rather than a string, and never rendered: String redacts it,
// and no error value in this package interpolates it. A configuration error
// that leaks the secret it was complaining about is worse than no check at all.
package config

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

// ErrMissing is returned when a required environment variable is unset or empty.
var ErrMissing = errors.New("required environment variable is not set")

// ErrInvalid is returned when a variable is set but does not hold a usable value.
var ErrInvalid = errors.New("environment variable holds an invalid value")

// Config is the validated configuration for one bridge invocation.
type Config struct {
	// SourceAddr is the funding account. It is proven to be the address that
	// SourceKey derives to.
	SourceAddr common.Address
	// DestAddr receives the value on the destination chain.
	DestAddr common.Address
	// SourceChainID and DestChainID identify the route.
	SourceChainID uint64
	DestChainID   uint64
	// SourceRPCURL and DestRPCURL address the two chains. For a same-chain
	// route only the source endpoint is required, and DestRPCURL is set to
	// the same value.
	SourceRPCURL string
	DestRPCURL   string
	// WithdrawalsDir is where initiated withdrawals are recorded. It is
	// optional and defaults to DefaultWithdrawalsDir.
	WithdrawalsDir string
	// Overrides are bridge addresses supplied by the operator. Any that is
	// left at the zero address is discovered instead.
	Overrides opstack.Addresses

	// sourceKey is unexported so that it cannot be reached by reflection-based
	// formatting of the struct from outside this package.
	sourceKey *ecdsa.PrivateKey
}

// SourceKey returns the private key for the source account.
func (c Config) SourceKey() *ecdsa.PrivateKey { return c.sourceKey }

// String renders the configuration for logging. The private key is replaced by
// a fixed placeholder, and so are the RPC URLs, which routinely carry an API
// key in the path.
func (c Config) String() string {
	return fmt.Sprintf(
		"Config{SourceAddr:%s DestAddr:%s SourceChainID:%d DestChainID:%d SourceRPCURL:%s DestRPCURL:%s SourceKey:%s}",
		c.SourceAddr.Hex(), c.DestAddr.Hex(), c.SourceChainID, c.DestChainID,
		redactURL(c.SourceRPCURL), redactURL(c.DestRPCURL), redacted,
	)
}

// redactURL keeps the scheme and host of an RPC URL and drops everything after
// it, because the path and query are where provider API keys live.
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return redacted
	}
	host, _, _ := strings.Cut(rest, "/")
	if host == "" {
		return redacted
	}
	return scheme + "://" + host + "/" + redacted
}

// RPCFor returns the RPC URL configured for the given chain ID.
//
// The lookup is by the part the chain plays in the configured route rather than
// by a table of known networks: an endpoint is either the source or the
// destination, and there is nothing else for it to be.
func (c Config) RPCFor(chainID uint64) (string, error) {
	switch chainID {
	case c.SourceChainID:
		return c.SourceRPCURL, nil
	case c.DestChainID:
		return c.DestRPCURL, nil
	default:
		return "", fmt.Errorf("%w: chain %d is neither the source (%d) nor the destination (%d)",
			ErrInvalid, chainID, c.SourceChainID, c.DestChainID)
	}
}

// Load reads the configuration using the supplied lookup function.
//
// getenv is injected rather than reached for directly so that the whole of this
// package is testable without touching the process environment.
func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	var err error

	if cfg.SourceAddr, err = requireAddress(getenv, EnvSourceAddr); err != nil {
		return Config{}, err
	}
	if cfg.DestAddr, err = requireAddress(getenv, EnvDestAddr); err != nil {
		return Config{}, err
	}
	if cfg.SourceChainID, err = requireChainID(getenv, EnvSourceChainID); err != nil {
		return Config{}, err
	}
	if cfg.DestChainID, err = requireChainID(getenv, EnvDestChainID); err != nil {
		return Config{}, err
	}
	if cfg.sourceKey, err = requireKey(getenv, cfg.SourceAddr); err != nil {
		return Config{}, err
	}
	if cfg.SourceRPCURL, cfg.DestRPCURL, err = requireRPCs(getenv, cfg.SourceChainID, cfg.DestChainID); err != nil {
		return Config{}, err
	}
	if cfg.Overrides, err = readOverrides(getenv); err != nil {
		return Config{}, err
	}
	cfg.WithdrawalsDir = orDefault(getenv(EnvWithdrawalsDir), DefaultWithdrawalsDir)
	return cfg, nil
}

// readOverrides reads the optional bridge address overrides.
//
// Each is validated even though each is optional: a variable that is set but
// malformed is a mistake the operator wants to hear about, and silently
// ignoring it would fall through to discovery and look like it had worked.
func readOverrides(getenv func(string) string) (opstack.Addresses, error) {
	var (
		out opstack.Addresses
		err error
	)
	if out.L1StandardBridge, err = optionalAddress(getenv, EnvL1StandardBridge); err != nil {
		return opstack.Addresses{}, err
	}
	if out.L2StandardBridge, err = optionalAddress(getenv, EnvL2StandardBridge); err != nil {
		return opstack.Addresses{}, err
	}
	if out.OptimismPortal, err = optionalAddress(getenv, EnvOptimismPortal); err != nil {
		return opstack.Addresses{}, err
	}
	return out, nil
}

// optionalAddress reads an address that may be absent. An unset variable gives
// the zero address and no error; a malformed one is an error.
func optionalAddress(getenv func(string) string, name string) (common.Address, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return common.Address{}, nil
	}
	if !common.IsHexAddress(raw) {
		return common.Address{}, fmt.Errorf("%w: %s is not a valid 0x-prefixed address", ErrInvalid, name)
	}
	return common.HexToAddress(raw), nil
}

// orDefault returns the trimmed value, or fallback when it is empty.
func orDefault(value, fallback string) string {
	if v := strings.TrimSpace(value); v != "" {
		return v
	}
	return fallback
}

// require returns the trimmed value of name, or ErrMissing if it is empty.
func require(getenv func(string) string, name string) (string, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return "", fmt.Errorf("%w: %s", ErrMissing, name)
	}
	return v, nil
}

// requireAddress reads a 0x-prefixed 20-byte address.
func requireAddress(getenv func(string) string, name string) (common.Address, error) {
	raw, err := require(getenv, name)
	if err != nil {
		return common.Address{}, err
	}
	if !common.IsHexAddress(raw) {
		return common.Address{}, fmt.Errorf("%w: %s is not a valid 0x-prefixed address", ErrInvalid, name)
	}
	return common.HexToAddress(raw), nil
}

// requireChainID reads a chain ID.
//
// There is no allowlist. A same-chain transfer is an EIP-155 value transfer and
// works on any EVM chain; whether a *bridge* route is possible between two
// chains is decided by asking the chains, not by consulting a table here. The
// one value rejected is zero, which EIP-155 does not assign to any network.
func requireChainID(getenv func(string) string, name string) (uint64, error) {
	raw, err := require(getenv, name)
	if err != nil {
		return 0, err
	}
	id, parseErr := strconv.ParseUint(raw, 10, 64)
	if parseErr != nil {
		return 0, fmt.Errorf("%w: %s is not a base-10 unsigned integer", ErrInvalid, name)
	}
	if id == 0 {
		return 0, fmt.Errorf("%w: %s=0 is not a chain ID", ErrInvalid, name)
	}
	return id, nil
}

// requireKey reads the source private key and proves it derives to want.
//
// The mismatch case is a hard error rather than a warning: signing with a key
// whose address is not the one the operator named means the transaction spends
// an account they did not intend to spend from.
func requireKey(getenv func(string) string, want common.Address) (*ecdsa.PrivateKey, error) {
	raw, err := require(getenv, EnvSourcePK)
	if err != nil {
		return nil, err
	}
	key, parseErr := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	if parseErr != nil {
		// parseErr is deliberately not wrapped: its message can quote the
		// offending input, which is the private key.
		return nil, fmt.Errorf("%w: %s is not a valid secp256k1 private key", ErrInvalid, EnvSourcePK)
	}
	if got := crypto.PubkeyToAddress(key.PublicKey); got != want {
		return nil, fmt.Errorf("%w: %s derives to %s, which is not %s=%s",
			ErrInvalid, EnvSourcePK, got.Hex(), EnvSourceAddr, want.Hex())
	}
	return key, nil
}

// requireRPCs demands an RPC URL for each chain the route actually touches, and
// only for those. A same-chain transfer has no second endpoint to configure, so
// it does not fail for want of one.
//
// When the route does cross chains, both endpoints are mandatory: which of them
// is the L1 and which is the rollup is not decidable from the chain IDs, so
// both have to be reachable before the route can be classified at all.
func requireRPCs(getenv func(string) string, src, dst uint64) (source, dest string, err error) {
	if source, err = require(getenv, EnvSourceRPCURL); err != nil {
		return "", "", err
	}
	if src == dst {
		return source, source, nil
	}
	if dest, err = require(getenv, EnvDestRPCURL); err != nil {
		return "", "", err
	}
	return source, dest, nil
}
