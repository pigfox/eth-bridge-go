// Package route maps a (source, destination) chain pair onto the kind of
// operation the bridge has to perform.
//
// Keeping this decision in one place means the CLI, the bridge and the tests
// all agree on what a pair of chain IDs means, and that an unsupported pair is
// rejected once rather than discovered halfway through a send.
package route

import (
	"errors"
	"fmt"

	"github.com/pigfox/eth-bridge-go/internal/config"
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

// ErrUnsupportedRoute is returned for a chain pair the bridge does not handle.
var ErrUnsupportedRoute = errors.New("unsupported route")

// ErrNotImplemented is returned for a route that is recognised but whose
// implementation has not shipped yet.
var ErrNotImplemented = errors.New("route is recognised but not implemented in this version")

// Resolve maps a source and destination chain ID onto a Kind.
func Resolve(src, dst uint64) (Kind, error) {
	switch {
	case src == config.ChainIDEthSepolia && dst == config.ChainIDBaseSepolia:
		return KindDeposit, nil
	case src == config.ChainIDBaseSepolia && dst == config.ChainIDEthSepolia:
		return KindWithdrawInitiate, nil
	case src == dst:
		// A same-chain transfer depends on no bridge contract and no pairing,
		// so there is nothing to check beyond the endpoint serving the chain it
		// claims to, which the bridge verifies before it signs.
		return KindSameChain, nil
	default:
		return KindUnknown, fmt.Errorf("%w: %d -> %d", ErrUnsupportedRoute, src, dst)
	}
}
