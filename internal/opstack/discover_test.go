package opstack

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/pigfox/eth-bridge-go/internal/chain/fake"
)

// The addresses below are shapes, not deployments. Discovery's whole point is
// that it does not know any real ones, so a test that used real ones would be
// asserting something the code never sees.
var (
	l1BridgeAddr = common.HexToAddress("0x1111111111111111111111111111111111111111")
	messengerAdr = common.HexToAddress("0x2222222222222222222222222222222222222222")
	portalAddr   = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

const (
	ethSepolia  = 11155111
	someRollup  = 84532
	otherRollup = 11155420
)

var errRPC = errors.New("node unreachable")

// addrWord ABI-encodes an address as a single 32-byte return value.
func addrWord(a common.Address) []byte {
	return common.LeftPadBytes(a.Bytes(), 32)
}

// opStackL2 is a fake serving a well-formed OP Stack L2, answering the given
// spelling of otherBridge.
func opStackL2(sig string) *fake.Client {
	c := &fake.Client{}
	c.SetCode(L2StandardBridgePredeploy, []byte{0x60})
	c.SetCode(L2ToL1MessagePasserPredeploy, []byte{0x60})
	c.SetCall(L2StandardBridgePredeploy, selector(sig), addrWord(l1BridgeAddr))
	return c
}

// pairedL1 is a fake serving the L1 that opStackL2 settles to, answering the
// given spellings of messenger and portal.
func pairedL1(messengerSig, portalSig string) *fake.Client {
	c := &fake.Client{}
	c.SetCode(l1BridgeAddr, []byte{0x60})
	c.SetCode(messengerAdr, []byte{0x60})
	c.SetCode(portalAddr, []byte{0x60})
	c.SetCall(l1BridgeAddr, selector(messengerSig), addrWord(messengerAdr))
	c.SetCall(messengerAdr, selector(portalSig), addrWord(portalAddr))
	return c
}

// endpoints wraps two fakes as the pair Discover takes.
func endpoints(l1, l2 *fake.Client) (Endpoint, Endpoint) {
	return Endpoint{ChainID: ethSepolia, Client: l1}, Endpoint{ChainID: someRollup, Client: l2}
}

// TestDiscoverAcceptsEverySelectorSpelling is the reason discovery tries more
// than one name per hop: the same field is spelled differently by different
// contract versions, and a chain running either must work.
func TestDiscoverAcceptsEverySelectorSpelling(t *testing.T) {
	for _, bridgeSig := range []string{"otherBridge()", "OTHER_BRIDGE()"} {
		for _, messengerSig := range []string{"messenger()", "MESSENGER()"} {
			for _, portalSig := range []string{"portal()", "PORTAL()"} {
				name := bridgeSig + "/" + messengerSig + "/" + portalSig
				t.Run(name, func(t *testing.T) {
					l1, l2 := endpoints(pairedL1(messengerSig, portalSig), opStackL2(bridgeSig))

					got, err := Discover(context.Background(), l1, l2)
					if err != nil {
						t.Fatalf("Discover: %v", err)
					}
					want := Addresses{
						L1StandardBridge: l1BridgeAddr,
						L2StandardBridge: L2StandardBridgePredeploy,
						OptimismPortal:   portalAddr,
					}
					if got != want {
						t.Errorf("Discover = %+v, want %+v", got, want)
					}
					if !got.Complete() {
						t.Error("Complete() = false on a full result")
					}
				})
			}
		}
	}
}

func TestAddressesComplete(t *testing.T) {
	full := Addresses{L1StandardBridge: l1BridgeAddr, L2StandardBridge: L2StandardBridgePredeploy, OptimismPortal: portalAddr}
	if !full.Complete() {
		t.Error("a full set should be complete")
	}
	for _, partial := range []Addresses{
		{},
		{L1StandardBridge: l1BridgeAddr},
		{L1StandardBridge: l1BridgeAddr, L2StandardBridge: L2StandardBridgePredeploy},
		{L2StandardBridge: L2StandardBridgePredeploy, OptimismPortal: portalAddr},
	} {
		if partial.Complete() {
			t.Errorf("%+v reported complete", partial)
		}
	}
}

// A chain with no predeploys is not an OP Stack L2, and must be told so by
// name rather than by a revert from somewhere inside a call.
func TestDiscoverRejectsAChainThatIsNotAnOPStackL2(t *testing.T) {
	for _, missing := range []common.Address{L2StandardBridgePredeploy, L2ToL1MessagePasserPredeploy} {
		l2 := opStackL2("otherBridge()")
		l2.SetCode(missing, nil)

		l1EP, l2EP := endpoints(pairedL1("messenger()", "portal()"), l2)
		_, err := Discover(context.Background(), l1EP, l2EP)
		if !errors.Is(err, ErrNotOPStack) {
			t.Fatalf("missing %s: error = %v, want ErrNotOPStack", missing.Hex(), err)
		}
		for _, want := range []string{"84532", "11155111"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name chain %s", err, want)
			}
		}
	}
}

// The Polygon-to-Arbitrum case from the brief: a user who points this at two
// chains that are not a rollup pair gets a sentence about those chains.
func TestDiscoverRejectsUnpairedChains(t *testing.T) {
	// A real OP Stack L2, and an L1 that has never heard of its bridge.
	strangerL1 := &fake.Client{}

	l1EP, l2EP := endpoints(strangerL1, opStackL2("otherBridge()"))
	_, err := Discover(context.Background(), l1EP, l2EP)
	if !errors.Is(err, ErrNotPaired) {
		t.Fatalf("error = %v, want ErrNotPaired", err)
	}
	if !errors.Is(err, ErrNoGetter) {
		t.Errorf("error = %v, want the failing hop wrapped too", err)
	}
	for _, want := range []string{"L1 standard bridge", "84532", "11155111", "holds no code"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Each hop after the first has to fail loudly too, and has to name itself.
func TestDiscoverReportsWhichHopFailed(t *testing.T) {
	tests := []struct {
		name   string
		breaks func(l1 *fake.Client)
		wantIn string
	}{
		{
			name:   "messenger is not resolvable",
			breaks: func(l1 *fake.Client) { l1.SetCall(l1BridgeAddr, selector("messenger()"), nil) },
			wantIn: "L1 cross-domain messenger",
		},
		{
			name:   "portal is not resolvable",
			breaks: func(l1 *fake.Client) { l1.SetCall(messengerAdr, selector("portal()"), nil) },
			wantIn: "optimism portal",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l1 := pairedL1("messenger()", "portal()")
			tc.breaks(l1)

			l1EP, l2EP := endpoints(l1, opStackL2("otherBridge()"))
			_, err := Discover(context.Background(), l1EP, l2EP)
			if !errors.Is(err, ErrNotPaired) {
				t.Fatalf("error = %v, want ErrNotPaired", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not name the failing hop %q", err, tc.wantIn)
			}
		})
	}
}

// A getter answering with the zero address, or with something that is not an
// address at all, is a getter this contract does not really have.
func TestDiscoverRejectsUnusableGetterReturns(t *testing.T) {
	nonAddress := make([]byte, 32)
	nonAddress[0] = 0xff // dirty high bytes: a uint256, not an address

	tests := []struct {
		name   string
		ret    []byte
		wantIn string
	}{
		{"zero address", addrWord(common.Address{}), "returned the zero address"},
		{"short return", []byte{0x01}, "returned 1 bytes"},
		{"not an address", nonAddress, "not an address"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l2 := opStackL2("otherBridge()")
			l2.SetCall(L2StandardBridgePredeploy, selector("otherBridge()"), tc.ret)
			l2.SetCall(L2StandardBridgePredeploy, selector("OTHER_BRIDGE()"), tc.ret)

			l1EP, l2EP := endpoints(pairedL1("messenger()", "portal()"), l2)
			_, err := Discover(context.Background(), l1EP, l2EP)
			if !errors.Is(err, ErrNoGetter) {
				t.Fatalf("error = %v, want ErrNoGetter", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not explain %q", err, tc.wantIn)
			}
		})
	}
}

// A node that cannot be read is not evidence that a getter is absent, so it is
// reported rather than treated as a missing variant.
func TestDiscoverPropagatesRPCFailures(t *testing.T) {
	t.Run("reading the predeploy", func(t *testing.T) {
		l2 := opStackL2("otherBridge()")
		l2.FailCode(L2StandardBridgePredeploy, errRPC)

		l1EP, l2EP := endpoints(pairedL1("messenger()", "portal()"), l2)
		if _, err := Discover(context.Background(), l1EP, l2EP); !errors.Is(err, errRPC) {
			t.Fatalf("error = %v, want the RPC failure", err)
		}
	})

	t.Run("verifying a derived address", func(t *testing.T) {
		l1 := pairedL1("messenger()", "portal()")
		l1.FailCode(l1BridgeAddr, errRPC)

		l1EP, l2EP := endpoints(l1, opStackL2("otherBridge()"))
		err := func() error {
			_, err := Discover(context.Background(), l1EP, l2EP)
			return err
		}()
		if !errors.Is(err, errRPC) {
			t.Fatalf("error = %v, want the RPC failure", err)
		}
	})
}

// A reverting call is a variant miss, and the next spelling must still be
// tried rather than the hop being abandoned.
func TestDiscoverFallsBackWhenTheFirstSpellingReverts(t *testing.T) {
	l2 := &fake.Client{}
	l2.SetCode(L2StandardBridgePredeploy, []byte{0x60})
	l2.SetCode(L2ToL1MessagePasserPredeploy, []byte{0x60})
	l2.FailCall(L2StandardBridgePredeploy, selector("otherBridge()"), errRPC)
	l2.SetCall(L2StandardBridgePredeploy, selector("OTHER_BRIDGE()"), addrWord(l1BridgeAddr))

	l1EP, l2EP := endpoints(pairedL1("messenger()", "portal()"), l2)
	got, err := Discover(context.Background(), l1EP, l2EP)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.L1StandardBridge != l1BridgeAddr {
		t.Errorf("L1StandardBridge = %s, want %s", got.L1StandardBridge.Hex(), l1BridgeAddr.Hex())
	}
}

func TestIsOPStack(t *testing.T) {
	yes, err := IsOPStack(context.Background(), Endpoint{ChainID: someRollup, Client: opStackL2("otherBridge()")})
	if err != nil || !yes {
		t.Errorf("IsOPStack on a rollup = %v, %v", yes, err)
	}

	no, err := IsOPStack(context.Background(), Endpoint{ChainID: ethSepolia, Client: &fake.Client{}})
	if err != nil || no {
		t.Errorf("IsOPStack on an L1 = %v, %v", no, err)
	}

	broken := &fake.Client{}
	broken.FailCode(L2StandardBridgePredeploy, errRPC)
	if _, err := IsOPStack(context.Background(), Endpoint{ChainID: someRollup, Client: broken}); !errors.Is(err, errRPC) {
		t.Errorf("error = %v, want the RPC failure", err)
	}
}

// The cache exists so that a deposit and the withdrawal after it do not pay for
// the same eight round trips twice.
func TestDiscoverCachedServesTheSecondCallWithoutTheChain(t *testing.T) {
	ctx := context.Background()
	l1EP, l2EP := endpoints(pairedL1("messenger()", "portal()"), opStackL2("otherBridge()"))
	l1EP.ChainID, l2EP.ChainID = ethSepolia, otherRollup

	first, err := DiscoverCached(ctx, l1EP, l2EP)
	if err != nil {
		t.Fatalf("first DiscoverCached: %v", err)
	}

	// Empty clients: a cache miss here could not possibly succeed.
	dead1, dead2 := endpoints(&fake.Client{}, &fake.Client{})
	dead1.ChainID, dead2.ChainID = ethSepolia, otherRollup

	second, err := DiscoverCached(ctx, dead1, dead2)
	if err != nil {
		t.Fatalf("second DiscoverCached: %v", err)
	}
	if second != first {
		t.Errorf("cached result = %+v, want %+v", second, first)
	}
}

// A failure must not be remembered: the usual cause is a node that was briefly
// unreachable, and caching that would make one bad moment permanent.
func TestDiscoverCachedDoesNotCacheFailures(t *testing.T) {
	ctx := context.Background()
	const pairChain = 999999

	broken, l2EP := endpoints(&fake.Client{}, opStackL2("otherBridge()"))
	broken.ChainID, l2EP.ChainID = ethSepolia, pairChain

	if _, err := DiscoverCached(ctx, broken, l2EP); err == nil {
		t.Fatal("DiscoverCached succeeded against an L1 with no bridge")
	}

	working, l2Again := endpoints(pairedL1("messenger()", "portal()"), opStackL2("otherBridge()"))
	working.ChainID, l2Again.ChainID = ethSepolia, pairChain

	got, err := DiscoverCached(ctx, working, l2Again)
	if err != nil {
		t.Fatalf("DiscoverCached after a failure: %v", err)
	}
	if got.L1StandardBridge != l1BridgeAddr {
		t.Errorf("L1StandardBridge = %s, want %s", got.L1StandardBridge.Hex(), l1BridgeAddr.Hex())
	}
}

// The selectors are computed rather than transcribed, and these are the values
// the live chains were probed with while this was written.
func TestSelectorsMatchTheProbedValues(t *testing.T) {
	tests := []struct{ sig, want string }{
		{"otherBridge()", "c89701a2"},
		{"OTHER_BRIDGE()", "7f46ddb2"},
		{"messenger()", "3cb747bf"},
		{"MESSENGER()", "927ede2d"},
		{"portal()", "6425666b"},
		{"PORTAL()", "0ff754ea"},
	}
	for _, tc := range tests {
		if got := common.Bytes2Hex(selector(tc.sig)); got != tc.want {
			t.Errorf("selector(%q) = %s, want %s", tc.sig, got, tc.want)
		}
	}
}
