package registry

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

// The embedded snapshot has to parse, or every fallback in the tool is silently
// unavailable and nothing else would notice.
func TestEmbeddedSnapshotParses(t *testing.T) {
	if len(index) == 0 {
		t.Fatal("the embedded snapshot produced an empty index")
	}
	for id, c := range index {
		if c.ChainID != id {
			t.Errorf("row keyed %d holds chainId %d", id, c.ChainID)
		}
		if c.Name == "" {
			t.Errorf("chain %d has no name", id)
		}
		// A row is either an L1 (no bridge, no settlement layer) or a rollup
		// with both addresses. Half a row would be a fallback that resolves
		// into a partly-zero address set.
		if c.L1ChainID == 0 {
			if c.L1StandardBridge != "" || c.OptimismPortal != "" {
				t.Errorf("chain %d has bridge addresses but no L1", id)
			}
			continue
		}
		if !common.IsHexAddress(c.L1StandardBridge) {
			t.Errorf("chain %d: l1StandardBridge %q is not an address", id, c.L1StandardBridge)
		}
		if !common.IsHexAddress(c.OptimismPortal) {
			t.Errorf("chain %d: optimismPortal %q is not an address", id, c.OptimismPortal)
		}
	}
}

// These are the chains the snapshot was built from, each derived live. The test
// pins the identities so that an edit that mangles a row is caught here rather
// than by a transaction going to the wrong contract.
func TestSnapshotHoldsTheDerivedRows(t *testing.T) {
	const ethSepolia = 11155111

	tests := []struct {
		chainID  uint64
		name     string
		l1Bridge string
		portal   string
	}{
		{84532, "Base Sepolia", "0xfd0Bf71F60660E2f608ed56e1659C450eB113120", "0x49f53e41452C74589E85cA1677426Ba426459e85"},
		{11155420, "OP Sepolia", "0xfBb0621E0B23b5478B630BD55a5f21f67730B0F1", "0x16Fc5058F25648194471939df75CF27A2fdC48BC"},
		{999999999, "Zora Sepolia", "0x5376f1D543dcbB5BD416c56C189e4cB7399fCcCB", "0xeffE2C6cA9Ab797D418f0D91eA60807713f3536F"},
		{919, "Mode Sepolia", "0xbC5C679879B2965296756CD959C3C739769995E2", "0x320e1580effF37E008F1C92700d1eBa47c1B23fD"},
		{1301, "Unichain Sepolia", "0xEa58fcA6849d79EAd1f26608855c2D6407d54Ce2", "0x0d83dab629f0e0f9D36c0Cbc89B69a489f0751bD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Lookup(tc.chainID)
			if !ok {
				t.Fatalf("chain %d is not in the snapshot", tc.chainID)
			}
			if got.Name != tc.name {
				t.Errorf("name = %q, want %q", got.Name, tc.name)
			}
			if got.L1ChainID != ethSepolia {
				t.Errorf("l1ChainId = %d, want %d", got.L1ChainID, ethSepolia)
			}

			addrs, ok := Addresses(ethSepolia, tc.chainID)
			if !ok {
				t.Fatalf("Addresses(%d, %d) reported nothing", ethSepolia, tc.chainID)
			}
			if addrs.L1StandardBridge != common.HexToAddress(tc.l1Bridge) {
				t.Errorf("L1StandardBridge = %s, want %s", addrs.L1StandardBridge.Hex(), tc.l1Bridge)
			}
			if addrs.OptimismPortal != common.HexToAddress(tc.portal) {
				t.Errorf("OptimismPortal = %s, want %s", addrs.OptimismPortal.Hex(), tc.portal)
			}
			if addrs.L2StandardBridge != opstack.L2StandardBridgePredeploy {
				t.Errorf("L2StandardBridge = %s, want the predeploy", addrs.L2StandardBridge.Hex())
			}
			if !addrs.Complete() {
				t.Error("the snapshot produced an incomplete address set")
			}
		})
	}
}

// A rollup paired with the wrong L1 is a different deployment, and the snapshot
// must not answer for it.
func TestAddressesRequiresTheL1ToMatch(t *testing.T) {
	if _, ok := Addresses(1, 84532); ok {
		t.Error("Base Sepolia resolved against Ethereum mainnet")
	}
	if _, ok := Addresses(11155111, 424242); ok {
		t.Error("an unknown chain resolved")
	}
	// An L1 row carries no bridge addresses, so it cannot answer as a rollup.
	if _, ok := Addresses(11155111, 11155111); ok {
		t.Error("Ethereum Sepolia resolved as its own rollup")
	}
}

func TestExplorerTx(t *testing.T) {
	const hash = "0xdeadbeef"

	if got, want := ExplorerTx(11155111, hash), "https://sepolia.etherscan.io/tx/"+hash; got != want {
		t.Errorf("ExplorerTx(eth sepolia) = %q, want %q", got, want)
	}
	if got, want := ExplorerTx(11155420, hash), "https://sepolia-optimism.etherscan.io/tx/"+hash; got != want {
		t.Errorf("ExplorerTx(op sepolia) = %q, want %q", got, want)
	}
	// A chain with no confirmed explorer, and a chain that is not in the
	// snapshot at all, both fall back to the bare hash rather than to a guess.
	if got := ExplorerTx(919, hash); got != hash {
		t.Errorf("ExplorerTx(mode sepolia) = %q, want the bare hash", got)
	}
	if got := ExplorerTx(424242, hash); got != hash {
		t.Errorf("ExplorerTx(unknown) = %q, want the bare hash", got)
	}
}

// A snapshot that cannot be parsed leaves the registry empty instead of taking
// the process down on the way past.
func TestUnparseableSnapshotYieldsAnEmptyIndex(t *testing.T) {
	if got := build([]byte("{not json")); got != nil {
		t.Errorf("build on bad JSON = %v, want nil", got)
	}
}

// A row whose addresses are malformed is not usable, and must not resolve into
// a partly-zero address set.
func TestMalformedRowDoesNotResolve(t *testing.T) {
	saved := index
	t.Cleanup(func() { index = saved })

	index = build([]byte(`{"chains":[{"chainId":7,"name":"Broken","l1ChainId":1,"l1StandardBridge":"nope","optimismPortal":"0x49f53e41452C74589E85cA1677426Ba426459e85"}]}`))
	if _, ok := Addresses(1, 7); ok {
		t.Error("a row with a malformed bridge address resolved")
	}

	index = build([]byte(`{"chains":[{"chainId":7,"name":"Broken","l1ChainId":1,"l1StandardBridge":"0x49f53e41452C74589E85cA1677426Ba426459e85","optimismPortal":"nope"}]}`))
	if _, ok := Addresses(1, 7); ok {
		t.Error("a row with a malformed portal address resolved")
	}
}
