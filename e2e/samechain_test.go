//go:build e2e

// Package e2e holds tests that spend real testnet ETH against live nodes.
//
// They are behind the `e2e` build tag so that neither `go test ./...` nor the
// coverage gate can pick them up: those must be runnable by anyone who has
// cloned the repository, and these need funded keys. Run them with
// scripts/5.e2e-live.sh.
package e2e

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/config"
)

// sendAmount is deliberately tiny. The transfer is to the sender's own address,
// so the only ETH actually consumed is gas.
var sendAmount = big.NewInt(10_000_000_000_000) // 0.00001 ETH

const (
	e2eTimeout  = 10 * time.Minute
	pollInteval = 5 * time.Second
)

// T1: a same-chain transfer on Base Sepolia.
func TestT1SameChainBaseSepolia(t *testing.T) {
	runSameChain(t, config.ChainIDBaseSepolia, "Base Sepolia", "https://sepolia.basescan.org/tx/")
}

// T2: a same-chain transfer on Ethereum Sepolia.
func TestT2SameChainEthSepolia(t *testing.T) {
	runSameChain(t, config.ChainIDEthSepolia, "Ethereum Sepolia", "https://sepolia.etherscan.io/tx/")
}

// runSameChain performs one live same-chain transfer and asserts the receipt
// came back successful.
func runSameChain(t *testing.T, chainID uint64, network, explorer string) {
	t.Helper()

	rpcVar := config.EnvL1RPCURL
	if chainID == config.ChainIDBaseSepolia {
		rpcVar = config.EnvL2RPCURL
	}
	requireEnv(t, config.EnvSourceAddr, config.EnvSourcePK, config.EnvDestAddr, rpcVar)

	// The chain IDs are supplied by the test rather than the environment, so
	// that one exported environment drives both networks.
	cfg, err := config.Load(func(k string) string {
		switch k {
		case config.EnvSourceChainID, config.EnvDestChainID:
			return strconvUint(chainID)
		default:
			return os.Getenv(k)
		}
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	client := dialOrFail(t, ctx, cfg, chainID)
	defer client.Close()

	before := balance(t, ctx, client, cfg)
	t.Logf("%s: sending %s wei from %s to %s (balance before: %s wei)",
		network, sendAmount, cfg.SourceAddr.Hex(), cfg.DestAddr.Hex(), before)

	if before.Cmp(sendAmount) <= 0 {
		t.Skipf("%s: %s holds %s wei, not enough to cover %s wei plus gas",
			network, cfg.SourceAddr.Hex(), before, sendAmount)
	}

	b := bridge.New(cfg, client, client,
		bridge.WithConfirmTimeout(e2eTimeout),
		bridge.WithPollInterval(pollInteval),
	)

	res, err := b.SameChain(ctx, sendAmount)
	if err != nil {
		t.Fatalf("%s: SameChain: %v", network, err)
	}

	t.Logf("%s: tx %s", network, res.SrcTxHash.Hex())
	t.Logf("%s: %s%s", network, explorer, res.SrcTxHash.Hex())

	rcpt, err := b.WaitReceipt(ctx, client, res.SrcTxHash)
	if err != nil {
		t.Fatalf("%s: receipt: %v", network, err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("%s: receipt status = %d, want 1", network, rcpt.Status)
	}
	t.Logf("%s: receipt status 1 in block %s, gas used %d", network, rcpt.BlockNumber, rcpt.GasUsed)
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

// balance reads the source account's balance.
func balance(t *testing.T, ctx context.Context, c chain.Client, cfg config.Config) *big.Int {
	t.Helper()
	bal, err := c.BalanceAt(ctx, cfg.SourceAddr, nil)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return bal
}

// strconvUint renders a chain ID for the injected getenv.
func strconvUint(v uint64) string {
	return new(big.Int).SetUint64(v).String()
}
