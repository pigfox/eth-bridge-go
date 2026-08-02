package opstack

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The OP Stack predeploys. These are the two addresses in this package that are
// not derived, and they are the only ones that can honestly be constants: the
// predeploy layout is fixed by the specification and is identical on every OP
// Stack L2, so hard-coding them says something about the protocol rather than
// about one deployment of it.
var (
	// L2StandardBridgePredeploy is the L2 side of the Standard Bridge.
	L2StandardBridgePredeploy = common.HexToAddress("0x4200000000000000000000000000000000000010")
	// L2ToL1MessagePasserPredeploy emits the MessagePassed event that carries
	// the parameters a withdrawal is later proved with.
	L2ToL1MessagePasserPredeploy = common.HexToAddress("0x4200000000000000000000000000000000000016")
)

// Errors returned by discovery.
var (
	// ErrNotOPStack means the chain does not carry the OP Stack predeploys, so
	// there is no Standard Bridge on it to talk to.
	ErrNotOPStack = errors.New("chain is not an OP Stack L2")
	// ErrNotPaired means both chains are real, and are not two ends of the
	// same rollup: the L1 contracts the L2 names are not deployed on the L1
	// that was supplied.
	ErrNotPaired = errors.New("chains are not a paired L1 and OP Stack L2")
	// ErrNoGetter means no known spelling of a getter returned a usable
	// address, which is what a contract version this tool has not seen looks
	// like from the outside.
	ErrNoGetter = errors.New("no known getter returned a usable address")
)

// Caller is the read-only view of a chain that discovery needs. It is a subset
// of chain.Client, declared here so that this package does not depend on the
// package that dials nodes.
type Caller interface {
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// Endpoint is one chain: a client, and the ID that client is expected to be
// serving. The ID is carried only so that failures can name the chain the
// operator configured rather than the one this package happens to be probing.
type Endpoint struct {
	ChainID uint64
	Client  Caller
}

// Addresses are the Standard Bridge contracts for one L1 and OP Stack L2 pair.
type Addresses struct {
	// L1StandardBridge is the deposit entry point, on the L1.
	L1StandardBridge common.Address
	// L2StandardBridge is the withdrawal entry point, on the L2. It is always
	// the predeploy.
	L2StandardBridge common.Address
	// OptimismPortal is where deposits surface on the L1 as
	// TransactionDeposited events. This tool does not send to it; it is
	// derived and checked because a pair that has no reachable portal is not a
	// pair, and because proving a withdrawal will need it.
	OptimismPortal common.Address
}

// Complete reports whether every address is set.
func (a Addresses) Complete() bool {
	return a.L1StandardBridge != (common.Address{}) &&
		a.L2StandardBridge != (common.Address{}) &&
		a.OptimismPortal != (common.Address{})
}

// IsOPStack reports whether the endpoint is serving an OP Stack L2.
//
// The test is that both predeploys the bridge depends on carry code. An
// ordinary L1, and any chain that is not an OP Stack rollup, has nothing at
// those addresses at all.
func IsOPStack(ctx context.Context, e Endpoint) (bool, error) {
	for _, at := range []common.Address{L2StandardBridgePredeploy, L2ToL1MessagePasserPredeploy} {
		ok, err := hasCode(ctx, e.Client, at)
		if err != nil {
			return false, fmt.Errorf("read code at %s on chain %d: %w", at.Hex(), e.ChainID, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// Discover derives the L1 side of an OP Stack pair from the L2's own
// predeploys, and checks that the two chains really are two ends of one rollup.
//
// Nothing here is looked up in a table. The L2 is asked which L1 bridge it
// answers to, that bridge is asked for its messenger and the messenger for its
// portal, and every answer is required to be a contract that actually exists on
// the L1 that was supplied. A pair that fails any of those checks is reported
// as unpaired rather than used, because the alternative is sending ETH to an
// address on the strength of a guess.
func Discover(ctx context.Context, l1, l2 Endpoint) (Addresses, error) {
	// 1. The destination has to be an OP Stack L2 before anything it says
	//    about its own bridge means anything.
	opStack, err := IsOPStack(ctx, l2)
	if err != nil {
		return Addresses{}, err
	}
	if !opStack {
		return Addresses{}, fmt.Errorf("%w: chain %d carries no code at the standard bridge predeploy %s (checked while pairing it with chain %d)",
			ErrNotOPStack, l2.ChainID, L2StandardBridgePredeploy.Hex(), l1.ChainID)
	}

	// 2. and 3. The L1 bridge the L2 names has to exist on the L1 supplied.
	onL1 := codeVerifier(l1.Client)
	l1Bridge, err := otherBridgeGetter.resolve(ctx, l2.Client, L2StandardBridgePredeploy, onL1)
	if err != nil {
		return Addresses{}, pairingError(l1, l2, "L1 standard bridge", err)
	}

	// 4. and 5. The portal is two hops further on, and both hops are read from
	//    the L1 itself.
	messenger, err := messengerGetter.resolve(ctx, l1.Client, l1Bridge, onL1)
	if err != nil {
		return Addresses{}, pairingError(l1, l2, "L1 cross-domain messenger", err)
	}
	portal, err := portalGetter.resolve(ctx, l1.Client, messenger, onL1)
	if err != nil {
		return Addresses{}, pairingError(l1, l2, "optimism portal", err)
	}

	return Addresses{
		L1StandardBridge: l1Bridge,
		L2StandardBridge: L2StandardBridgePredeploy,
		OptimismPortal:   portal,
	}, nil
}

// pairingError names the hop that failed and both chains, so that pointing the
// tool at two unrelated networks produces a sentence about those networks
// rather than a decoding error from somewhere inside a call.
func pairingError(l1, l2 Endpoint, what string, err error) error {
	return fmt.Errorf("%w: cannot resolve the %s for chain %d (as an L2) against chain %d (as its L1): %w",
		ErrNotPaired, what, l2.ChainID, l1.ChainID, err)
}

// codeVerifier turns a client into the check the getters apply to every
// candidate address: an address is only accepted if a contract lives at it on
// the chain that is supposed to host it.
func codeVerifier(on Caller) func(context.Context, common.Address) (bool, error) {
	return func(ctx context.Context, addr common.Address) (bool, error) {
		return hasCode(ctx, on, addr)
	}
}

// hasCode reports whether an address holds deployed bytecode.
func hasCode(ctx context.Context, on Caller, addr common.Address) (bool, error) {
	code, err := on.CodeAt(ctx, addr, nil)
	if err != nil {
		return false, err
	}
	return len(code) > 0, nil
}

// getter is one hop of the derivation, together with every spelling of it that
// a deployed OP Stack contract might answer to.
//
// The names were not stable across contract versions: the same field is
// otherBridge() on one and OTHER_BRIDGE() on another, and a chain running an
// older or newer release than the one this tool was written against is a
// routine situation rather than an error. Trying each in turn costs one
// read-only call and removes a whole class of "works on Base, not on yours".
type getter struct {
	field string
	sigs  []string
}

var (
	otherBridgeGetter = getter{field: "otherBridge", sigs: []string{"otherBridge()", "OTHER_BRIDGE()"}}
	messengerGetter   = getter{field: "messenger", sigs: []string{"messenger()", "MESSENGER()"}}
	portalGetter      = getter{field: "portal", sigs: []string{"portal()", "PORTAL()"}}
)

// resolve calls each spelling in turn and returns the first answer that is a
// non-zero address with code at it on the chain verify checks.
//
// A variant that reverts, or answers with something that is not an address, is
// a variant this contract does not have, and the next one is tried. A failure
// from verify itself is different: the node could not be read, which is not
// evidence about the getter, so it is returned rather than swallowed.
func (g getter) resolve(
	ctx context.Context,
	on Caller,
	at common.Address,
	verify func(context.Context, common.Address) (bool, error),
) (common.Address, error) {
	reasons := make([]string, 0, len(g.sigs))
	for _, sig := range g.sigs {
		addr, why := callAddress(ctx, on, at, sig)
		if why != "" {
			reasons = append(reasons, sig+": "+why)
			continue
		}
		ok, err := verify(ctx, addr)
		if err != nil {
			return common.Address{}, fmt.Errorf("check code at %s: %w", addr.Hex(), err)
		}
		if ok {
			return addr, nil
		}
		reasons = append(reasons, fmt.Sprintf("%s: returned %s, which holds no code", sig, addr.Hex()))
	}
	return common.Address{}, fmt.Errorf("%w: %s on %s (%s)",
		ErrNoGetter, g.field, at.Hex(), strings.Join(reasons, "; "))
}

// callAddress makes one read-only call and decodes a single address return.
//
// The second result is a reason the answer was unusable rather than an error,
// because at this level every failure means the same thing — try the next
// spelling — and an error value would invite a caller to treat them
// differently.
func callAddress(ctx context.Context, on Caller, at common.Address, sig string) (common.Address, string) {
	ret, err := on.CallContract(ctx, ethereum.CallMsg{To: &at, Data: selector(sig)}, nil)
	if err != nil {
		return common.Address{}, "call failed"
	}
	// A single address return is one 32-byte word, left-padded with zeros.
	// Anything else is a different function that happens to share a selector,
	// or a contract that returned nothing at all.
	const wordLen = 32
	if len(ret) != wordLen {
		return common.Address{}, fmt.Sprintf("returned %d bytes, want %d", len(ret), wordLen)
	}
	if !allZero(ret[:wordLen-common.AddressLength]) {
		return common.Address{}, "returned a word that is not an address"
	}
	addr := common.BytesToAddress(ret)
	if addr == (common.Address{}) {
		return common.Address{}, "returned the zero address"
	}
	return addr, ""
}

// allZero reports whether every byte is zero.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// selector returns the four-byte function selector for a signature.
//
// The selectors are computed rather than written down. A mistyped hex literal
// would be a call to some other function entirely, and there is no way to
// notice that by reading it.
func selector(sig string) []byte {
	return crypto.Keccak256([]byte(sig))[:4]
}

// pairKey identifies a chain pair for the cache.
type pairKey struct{ l1, l2 uint64 }

var (
	cacheMu sync.Mutex
	cache   = map[pairKey]Addresses{}
)

// DiscoverCached is Discover with the answer remembered for the lifetime of
// the process.
//
// Discovery costs five to eight round trips, and the answer cannot change
// while the process runs: the predeploys are immutable and the L1 contracts
// behind them are upgraded through proxies whose addresses stay put. A deposit
// and the withdrawal that follows it therefore pay for the derivation once.
//
// Only successes are cached. A failure is usually a node that was unreachable
// for a moment, and remembering that would turn a blip into a permanent one.
func DiscoverCached(ctx context.Context, l1, l2 Endpoint) (Addresses, error) {
	k := pairKey{l1: l1.ChainID, l2: l2.ChainID}

	cacheMu.Lock()
	hit, ok := cache[k]
	cacheMu.Unlock()
	if ok {
		return hit, nil
	}

	addrs, err := Discover(ctx, l1, l2)
	if err != nil {
		return Addresses{}, err
	}

	cacheMu.Lock()
	cache[k] = addrs
	cacheMu.Unlock()
	return addrs, nil
}
