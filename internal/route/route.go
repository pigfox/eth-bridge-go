// Package route decides what kind of operation a (source, destination) chain
// pair calls for, by asking the chains rather than by consulting a list.
//
// Keeping the decision in one place means the CLI, the bridge and the tests all
// agree on what a pair of chains means, and that a pair the bridge cannot serve
// is rejected once, with a reason, rather than discovered halfway through a
// send.
package route

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

// Kind is the operation a route resolves to.
type Kind int

const (
	// KindUnknown is the zero value and is never returned with a nil error.
	KindUnknown Kind = iota
	// KindSameChain is a plain value transfer within one chain.
	KindSameChain
	// KindDeposit is an L1 to L2 deposit through the Standard Bridge.
	KindDeposit
	// KindWithdrawInitiate is the first leg of an L2 to L1 withdrawal.
	KindWithdrawInitiate
)

// String renders the kind for logs and CLI output.
func (k Kind) String() string {
	switch k {
	case KindSameChain:
		return "same-chain"
	case KindDeposit:
		return "deposit"
	case KindWithdrawInitiate:
		return "withdraw-initiate"
	case KindUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// ErrUnsupportedRoute is returned for a chain pair the bridge cannot serve.
var ErrUnsupportedRoute = errors.New("unsupported route")

// Endpoint is one side of a route: a chain ID, and a read-only way to ask that
// chain what it is. It is an interface rather than a client so that resolving a
// route can be tested without a node.
type Endpoint = opstack.Endpoint

// Route is a resolved operation, together with the contract addresses it needs
// and where each of them came from.
type Route struct {
	// Kind is the operation to perform.
	Kind Kind
	// Addrs are the Standard Bridge contracts for the pair. They are set only
	// for the two bridge routes; a same-chain transfer uses no contract.
	Addrs opstack.Addresses
	// Sources records how each address was resolved, so that the operator can
	// see whether the tool asked the chain or fell back to a file before any
	// value moves.
	Sources Sources
}

// Where an address came from.
const (
	// SourceDiscovery means the address was derived from the chains.
	SourceDiscovery = "discovery"
	// SourceRegistry means discovery failed and a vendored snapshot answered.
	SourceRegistry = "registry"
	// SourceOverride means the operator supplied it.
	SourceOverride = "override"
)

// Sources records the provenance of each resolved address.
type Sources struct {
	L1StandardBridge string
	L2StandardBridge string
	OptimismPortal   string
}

// all returns a Sources with every field set to one value.
func all(src string) Sources {
	return Sources{L1StandardBridge: src, L2StandardBridge: src, OptimismPortal: src}
}

// Options tunes how the addresses for a bridge route are resolved. The zero
// value is discovery only, which is the behaviour that needs no configuration.
type Options struct {
	// Overrides are addresses supplied by the operator. Any non-zero field
	// wins over everything else, and a complete set skips the chain entirely.
	Overrides opstack.Addresses
	// Fallback is consulted only when discovery has failed. It is a function
	// rather than a package dependency so that this package keeps no table of
	// chains of its own.
	Fallback func(l1ChainID, l2ChainID uint64) (opstack.Addresses, bool)
}

// Resolve decides what the configured pair of chains means.
//
// The decision is a capability check, not a table lookup. A same-chain transfer
// is always possible. Otherwise each side is asked whether it is an OP Stack
// L2, and the one that is gets paired against the one that is not — which is
// also where the contract addresses come from. Everything else is refused with
// the reason it failed.
func Resolve(ctx context.Context, src, dst Endpoint, opts Options) (Route, error) {
	if src.ChainID == dst.ChainID {
		return Route{Kind: KindSameChain}, nil
	}

	srcIsL2, err := opstack.IsOPStack(ctx, src)
	if err != nil {
		return Route{}, err
	}
	dstIsL2, err := opstack.IsOPStack(ctx, dst)
	if err != nil {
		return Route{}, err
	}

	switch {
	case srcIsL2 && dstIsL2:
		return Route{}, fmt.Errorf("%w: %d -> %d: both chains are OP Stack L2s. "+
			"The Standard Bridge settles only through the L1 they share, so it cannot move value "+
			"directly between two rollups; that needs a third-party message protocol, which this tool does not implement",
			ErrUnsupportedRoute, src.ChainID, dst.ChainID)

	case dstIsL2:
		// The destination is the rollup, so value is going down: a deposit.
		return bridgeRoute(ctx, KindDeposit, src, dst, opts)

	case srcIsL2:
		// The source is the rollup, so value is coming up: a withdrawal, of
		// which this tool performs only the first leg.
		return bridgeRoute(ctx, KindWithdrawInitiate, dst, src, opts)

	default:
		return Route{}, fmt.Errorf("%w: %d -> %d: neither chain is an OP Stack L2 "+
			"(neither carries code at the standard bridge predeploy %s), so there is no bridge between them to use",
			ErrUnsupportedRoute, src.ChainID, dst.ChainID, opstack.L2StandardBridgePredeploy.Hex())
	}
}

// bridgeRoute resolves the addresses for a classified bridge route. l1 and l2
// are the settlement layer and the rollup, whichever way round the transfer is
// going: the contracts belong to the pair, not to the direction.
func bridgeRoute(ctx context.Context, kind Kind, l1, l2 Endpoint, opts Options) (Route, error) {
	addrs, sources, err := resolveAddresses(ctx, l1, l2, opts)
	if err != nil {
		return Route{}, err
	}
	return Route{Kind: kind, Addrs: addrs, Sources: sources}, nil
}

// resolveAddresses applies the precedence: an operator override beats
// everything, discovery beats the vendored snapshot, and the snapshot is
// reached only when the chains could not answer.
func resolveAddresses(ctx context.Context, l1, l2 Endpoint, opts Options) (opstack.Addresses, Sources, error) {
	// A full set of overrides is an instruction not to ask, so the chain is
	// not asked. Anything less still needs a base to fill the gaps from.
	if opts.Overrides.Complete() {
		return opts.Overrides, all(SourceOverride), nil
	}

	addrs, sources, err := discoverOrFallBack(ctx, l1, l2, opts)
	if err != nil {
		return opstack.Addresses{}, Sources{}, err
	}
	addrs, sources = overlay(addrs, sources, opts.Overrides)
	return addrs, sources, nil
}

// discoverOrFallBack asks the chains, and consults the snapshot only if they
// could not be asked.
func discoverOrFallBack(ctx context.Context, l1, l2 Endpoint, opts Options) (opstack.Addresses, Sources, error) {
	addrs, err := opstack.DiscoverCached(ctx, l1, l2)
	if err == nil {
		return addrs, all(SourceDiscovery), nil
	}
	if opts.Fallback == nil {
		return opstack.Addresses{}, Sources{}, err
	}
	fallback, ok := opts.Fallback(l1.ChainID, l2.ChainID)
	if !ok {
		// The discovery failure is the useful one: it says what could not be
		// read, where the snapshot can only say that it had no row.
		return opstack.Addresses{}, Sources{}, err
	}
	return fallback, all(SourceRegistry), nil
}

// overlay replaces each address the operator supplied, and records that it came
// from them rather than from the chain.
func overlay(addrs opstack.Addresses, sources Sources, over opstack.Addresses) (opstack.Addresses, Sources) {
	if over.L1StandardBridge != (common.Address{}) {
		addrs.L1StandardBridge, sources.L1StandardBridge = over.L1StandardBridge, SourceOverride
	}
	if over.L2StandardBridge != (common.Address{}) {
		addrs.L2StandardBridge, sources.L2StandardBridge = over.L2StandardBridge, SourceOverride
	}
	if over.OptimismPortal != (common.Address{}) {
		addrs.OptimismPortal, sources.OptimismPortal = over.OptimismPortal, SourceOverride
	}
	return addrs, sources
}
