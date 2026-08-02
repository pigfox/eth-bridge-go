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

// Route is a resolved operation, together with the contract addresses it needs.
type Route struct {
	// Kind is the operation to perform.
	Kind Kind
	// Addrs are the Standard Bridge contracts for the pair. They are set only
	// for the two bridge routes; a same-chain transfer uses no contract.
	Addrs opstack.Addresses
}

// Resolve decides what the configured pair of chains means.
//
// The decision is a capability check, not a table lookup. A same-chain transfer
// is always possible. Otherwise each side is asked whether it is an OP Stack
// L2, and the one that is gets paired against the one that is not — which is
// also where the contract addresses come from. Everything else is refused with
// the reason it failed.
func Resolve(ctx context.Context, src, dst Endpoint) (Route, error) {
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
		addrs, err := opstack.DiscoverCached(ctx, src, dst)
		if err != nil {
			return Route{}, err
		}
		return Route{Kind: KindDeposit, Addrs: addrs}, nil

	case srcIsL2:
		// The source is the rollup, so value is coming up: a withdrawal, of
		// which this tool performs only the first leg.
		addrs, err := opstack.DiscoverCached(ctx, dst, src)
		if err != nil {
			return Route{}, err
		}
		return Route{Kind: KindWithdrawInitiate, Addrs: addrs}, nil

	default:
		return Route{}, fmt.Errorf("%w: %d -> %d: neither chain is an OP Stack L2 "+
			"(neither carries code at the standard bridge predeploy %s), so there is no bridge between them to use",
			ErrUnsupportedRoute, src.ChainID, dst.ChainID, opstack.L2StandardBridgePredeploy.Hex())
	}
}
