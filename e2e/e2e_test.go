//go:build e2e

// Package e2e holds tests that spend real testnet ETH against live nodes.
//
// They are behind the `e2e` build tag so that neither `go test ./...` nor the
// coverage gate can pick them up: those must be runnable by anyone who has
// cloned the repository, and these need funded keys. Run them with
// scripts/5.e2e-live.sh.
//
// The suite is parameterised by chain pair. Nothing below names a network, so
// pointing it at a different L1 and a different OP Stack L2 is four environment
// variables and no code change — which is the claim the suite exists to test.
package e2e

import (
	"context"
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/opstack"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

const (
	e2eTimeout  = 10 * time.Minute
	pollInteval = 5 * time.Second
)

// Amounts. All deliberately tiny; the same-chain transfers are to the sender's
// own address, so the only ETH they consume is gas.
var (
	sendAmount     = big.NewInt(10_000_000_000_000)  // 0.00001 ETH
	depositAmount  = big.NewInt(500_000_000_000_000) // 0.0005 ETH
	withdrawAmount = big.NewInt(10_000_000_000_000)  // 0.00001 ETH
)

// The chain pair the suite runs against. These are the harness's own variables,
// not the tool's: the tool takes a source and a destination, and which of those
// is the L1 is something it works out for itself.
const (
	envL1ChainID = "BRIDGE_E2E_L1_CHAIN_ID"
	envL1RPCURL  = "BRIDGE_E2E_L1_RPC_URL"
	envL2ChainID = "BRIDGE_E2E_L2_CHAIN_ID"
	envL2RPCURL  = "BRIDGE_E2E_L2_RPC_URL"
)

// The default pair. Ethereum Sepolia and OP Sepolia are the default precisely
// because neither the routing nor the address resolution has ever heard of
// them: a pass here is evidence the tool works on a pair it was not built
// against.
const (
	defaultL1ChainID = 11155111
	defaultL1RPCURL  = "https://ethereum-sepolia-rpc.publicnode.com"
	defaultL2ChainID = 11155420
	defaultL2RPCURL  = "https://sepolia.optimism.io"
)

// chainPair is the L1 and the OP Stack L2 the suite is pointed at.
type chainPair struct {
	l1ID  uint64
	l1RPC string
	l2ID  uint64
	l2RPC string
}

// livePair reads the pair from the environment, falling back to the default.
func livePair(t *testing.T) chainPair {
	t.Helper()
	return chainPair{
		l1ID:  uintEnv(t, envL1ChainID, defaultL1ChainID),
		l1RPC: strEnv(envL1RPCURL, defaultL1RPCURL),
		l2ID:  uintEnv(t, envL2ChainID, defaultL2ChainID),
		l2RPC: strEnv(envL2RPCURL, defaultL2RPCURL),
	}
}

// strEnv reads a string variable, or returns the fallback.
func strEnv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// uintEnv reads a chain ID, or returns the fallback.
func uintEnv(t *testing.T, name string, fallback uint64) uint64 {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		t.Fatalf("%s=%q is not a chain ID: %v", name, raw, err)
	}
	return v
}

// loadConfig builds a configuration for one route.
//
// The route is injected rather than read from the ambient environment, so that
// a single exported set of credentials drives every direction the suite tests.
func loadConfig(t *testing.T, srcID uint64, srcRPC string, dstID uint64, dstRPC string, extra map[string]string) config.Config {
	t.Helper()

	injected := map[string]string{
		config.EnvSourceChainID: strconv.FormatUint(srcID, 10),
		config.EnvDestChainID:   strconv.FormatUint(dstID, 10),
		config.EnvSourceRPCURL:  srcRPC,
		config.EnvDestRPCURL:    dstRPC,
	}
	for k, v := range extra {
		injected[k] = v
	}

	cfg, err := config.Load(func(k string) string {
		if v, ok := injected[k]; ok {
			return v
		}
		return os.Getenv(k)
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// requireEnv skips the test, with a message naming what is missing, rather than
// failing it. An unfunded checkout should report "skipped", not "broken".
func requireEnv(t *testing.T, names ...string) {
	t.Helper()
	var missing []string
	for _, n := range names {
		if os.Getenv(n) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Skipf("skipping live test: %v not set (run scripts/5.e2e-live.sh)", missing)
	}
}

// requireCredentials skips unless the funding account is configured.
func requireCredentials(t *testing.T) {
	t.Helper()
	requireEnv(t, config.EnvSourceAddr, config.EnvSourcePK, config.EnvDestAddr)
}

// dialOrFail opens a client for one of the configured chains.
func dialOrFail(t *testing.T, ctx context.Context, cfg config.Config, chainID uint64) chain.Client {
	t.Helper()
	rpc, err := cfg.RPCFor(chainID)
	if err != nil {
		t.Fatalf("RPCFor(%d): %v", chainID, err)
	}
	c, err := chain.Dial(ctx, rpc)
	if err != nil {
		t.Fatalf("dial chain %d: %v", chainID, err)
	}
	return c
}

// balance reads the source account's balance.
func balance(t *testing.T, ctx context.Context, c chain.Client, cfg config.Config) *big.Int {
	t.Helper()
	bal, err := c.BalanceAt(ctx, cfg.SourceAddr, nil)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return bal
}

// resolveLive resolves a route against the live chains with the registry
// fallback deliberately switched off.
//
// That is the point of the whole suite. With no fallback, the only way the
// addresses can be produced is by deriving them from the chains, so a passing
// bridge test is evidence that discovery worked rather than that a vendored
// file happened to hold the right answer. The assertions below make the same
// point explicitly, so that a future change which quietly reintroduces a table
// fails here rather than passing.
func resolveLive(t *testing.T, ctx context.Context, cfg config.Config, src, dst chain.Client) route.Route {
	t.Helper()

	rt, err := route.Resolve(ctx,
		route.Endpoint{ChainID: cfg.SourceChainID, Client: src},
		route.Endpoint{ChainID: cfg.DestChainID, Client: dst},
		route.Options{}, // no overrides, no registry: discovery or nothing
	)
	if err != nil {
		t.Fatalf("route.Resolve(%d -> %d): %v", cfg.SourceChainID, cfg.DestChainID, err)
	}

	t.Logf("route %d -> %d resolved to %s", cfg.SourceChainID, cfg.DestChainID, rt.Kind)
	t.Logf("  l1 bridge: %s (%s)", rt.Addrs.L1StandardBridge.Hex(), rt.Sources.L1StandardBridge)
	t.Logf("  l2 bridge: %s (%s)", rt.Addrs.L2StandardBridge.Hex(), rt.Sources.L2StandardBridge)
	t.Logf("  portal:    %s (%s)", rt.Addrs.OptimismPortal.Hex(), rt.Sources.OptimismPortal)

	if !rt.Addrs.Complete() {
		t.Fatalf("resolved addresses are incomplete: %+v", rt.Addrs)
	}
	for name, src := range map[string]string{
		"l1 bridge": rt.Sources.L1StandardBridge,
		"l2 bridge": rt.Sources.L2StandardBridge,
		"portal":    rt.Sources.OptimismPortal,
	} {
		if src != route.SourceDiscovery {
			t.Errorf("%s came from %q, want %q: the addresses must be derived from the chains",
				name, src, route.SourceDiscovery)
		}
	}
	if rt.Addrs.L2StandardBridge != opstack.L2StandardBridgePredeploy {
		t.Errorf("L2StandardBridge = %s, want the predeploy", rt.Addrs.L2StandardBridge.Hex())
	}
	return rt
}
