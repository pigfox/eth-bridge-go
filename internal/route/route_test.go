package route

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/pigfox/eth-bridge-go/internal/chain/fake"
	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

// The addresses are shapes rather than deployments: resolving a route must not
// depend on knowing any real ones.
var (
	l1Bridge  = common.HexToAddress("0x1111111111111111111111111111111111111111")
	messenger = common.HexToAddress("0x2222222222222222222222222222222222222222")
	portal    = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

var errRPC = errors.New("node unreachable")

// sel is the four-byte selector for a signature, computed the same way the
// discovery code computes it.
func sel(sig string) []byte {
	return crypto.Keccak256([]byte(sig))[:4]
}

// rollup is a fake serving an OP Stack L2 whose L1 bridge is at bridgeAddr.
func rollup(bridgeAddr common.Address) *fake.Client {
	c := &fake.Client{}
	c.SetCode(opstack.L2StandardBridgePredeploy, []byte{0x60})
	c.SetCode(opstack.L2ToL1MessagePasserPredeploy, []byte{0x60})
	c.SetCall(opstack.L2StandardBridgePredeploy, sel("otherBridge()"), word(bridgeAddr))
	return c
}

// settlementLayer is a fake serving the L1 that rollup settles to.
func settlementLayer() *fake.Client {
	c := &fake.Client{}
	c.SetCode(l1Bridge, []byte{0x60})
	c.SetCode(messenger, []byte{0x60})
	c.SetCode(portal, []byte{0x60})
	c.SetCall(l1Bridge, sel("messenger()"), word(messenger))
	c.SetCall(messenger, sel("portal()"), word(portal))
	return c
}

// word ABI-encodes an address as a single 32-byte return value.
func word(a common.Address) []byte {
	return common.LeftPadBytes(a.Bytes(), 32)
}

// A same-chain transfer is decided without touching the chain at all: there is
// no contract involved, so there is nothing to ask.
func TestResolveSameChainNeedsNoProbing(t *testing.T) {
	for _, id := range []uint64{1, 10, 137, 42161, 84532, 11155111, 31337} {
		// Clients that would fail any call. Reaching them would be the bug.
		ep := Endpoint{ChainID: id, Client: &fake.Client{}}

		got, err := Resolve(context.Background(), ep, ep)
		if err != nil {
			t.Errorf("Resolve(%d, %d): %v", id, id, err)
			continue
		}
		if got.Kind != KindSameChain {
			t.Errorf("Resolve(%d, %d) = %v, want KindSameChain", id, id, got.Kind)
		}
		if got.Addrs != (opstack.Addresses{}) {
			t.Errorf("same-chain carried bridge addresses: %+v", got.Addrs)
		}
	}
}

// An L1 paired with a rollup resolves in both directions, and carries the
// addresses discovery found rather than any this package knows.
func TestResolveBridgeRoutes(t *testing.T) {
	const (
		l1ChainID = 11155111
		l2ChainID = 11155420
	)

	tests := []struct {
		name     string
		src, dst Endpoint
		want     Kind
	}{
		{
			name: "L1 to its rollup is a deposit",
			src:  Endpoint{ChainID: l1ChainID, Client: settlementLayer()},
			dst:  Endpoint{ChainID: l2ChainID, Client: rollup(l1Bridge)},
			want: KindDeposit,
		},
		{
			name: "the rollup back to its L1 initiates a withdrawal",
			src:  Endpoint{ChainID: l2ChainID, Client: rollup(l1Bridge)},
			dst:  Endpoint{ChainID: l1ChainID, Client: settlementLayer()},
			want: KindWithdrawInitiate,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(context.Background(), tc.src, tc.dst)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Kind != tc.want {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.want)
			}
			want := opstack.Addresses{
				L1StandardBridge: l1Bridge,
				L2StandardBridge: opstack.L2StandardBridgePredeploy,
				OptimismPortal:   portal,
			}
			if got.Addrs != want {
				t.Errorf("Addrs = %+v, want %+v", got.Addrs, want)
			}
		})
	}
}

// Two rollups is the case most likely to be tried by mistake, so the refusal
// has to say why rather than just no.
func TestResolveRejectsL2ToL2(t *testing.T) {
	src := Endpoint{ChainID: 84532, Client: rollup(l1Bridge)}
	dst := Endpoint{ChainID: 11155420, Client: rollup(l1Bridge)}

	got, err := Resolve(context.Background(), src, dst)
	if !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("error = %v, want ErrUnsupportedRoute", err)
	}
	if got.Kind != KindUnknown {
		t.Errorf("Kind = %v, want KindUnknown", got.Kind)
	}
	for _, want := range []string{"84532", "11155420", "L1 they share", "third-party message protocol"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not explain %q", err, want)
		}
	}
}

// Polygon to Arbitrum: two real chains, neither of them a rollup this tool can
// bridge. The error has to name the capability that was missing.
func TestResolveRejectsTwoNonRollups(t *testing.T) {
	src := Endpoint{ChainID: 137, Client: &fake.Client{}}
	dst := Endpoint{ChainID: 42161, Client: &fake.Client{}}

	_, err := Resolve(context.Background(), src, dst)
	if !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("error = %v, want ErrUnsupportedRoute", err)
	}
	for _, want := range []string{"137", "42161", "neither chain is an OP Stack L2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not explain %q", err, want)
		}
	}
}

// A rollup whose L1 is not the chain it was paired with is not a route, and the
// failure belongs to discovery rather than to the classification.
func TestResolveRejectsAMismatchedPair(t *testing.T) {
	src := Endpoint{ChainID: 11155111, Client: &fake.Client{}} // an L1 that has never heard of it
	dst := Endpoint{ChainID: 7777777, Client: rollup(l1Bridge)}

	if _, err := Resolve(context.Background(), src, dst); !errors.Is(err, opstack.ErrNotPaired) {
		t.Fatalf("error = %v, want opstack.ErrNotPaired", err)
	}

	// And the same in the withdrawal direction.
	if _, err := Resolve(context.Background(), dst, src); !errors.Is(err, opstack.ErrNotPaired) {
		t.Fatalf("reversed: error = %v, want opstack.ErrNotPaired", err)
	}
}

// A node that cannot be read is not an answer about capability, so it is
// reported rather than turned into "unsupported".
func TestResolvePropagatesRPCFailures(t *testing.T) {
	broken := &fake.Client{}
	broken.FailCode(opstack.L2StandardBridgePredeploy, errRPC)

	tests := []struct {
		name     string
		src, dst Endpoint
	}{
		{
			name: "the source cannot be read",
			src:  Endpoint{ChainID: 1, Client: broken},
			dst:  Endpoint{ChainID: 2, Client: &fake.Client{}},
		},
		{
			name: "the destination cannot be read",
			src:  Endpoint{ChainID: 1, Client: &fake.Client{}},
			dst:  Endpoint{ChainID: 2, Client: broken},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(context.Background(), tc.src, tc.dst); !errors.Is(err, errRPC) {
				t.Fatalf("error = %v, want the RPC failure", err)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindSameChain, "same-chain"},
		{KindDeposit, "deposit"},
		{KindWithdrawInitiate, "withdraw-initiate"},
		{KindUnknown, "unknown"},
		{Kind(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
