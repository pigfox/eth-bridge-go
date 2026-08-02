//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/registry"
)

// T2: a same-chain transfer on the rollup.
func TestT2SameChainOnL2(t *testing.T) {
	pair := livePair(t)
	runSameChain(t, pair.l2ID, pair.l2RPC)
}

// T3: a same-chain transfer on the settlement layer.
//
// The two together are the P1 claim: a plain transfer depends on no bridge and
// no pairing, so it works on either side of the pair — and, by the same
// argument, on any EVM chain.
func TestT3SameChainOnL1(t *testing.T) {
	pair := livePair(t)
	runSameChain(t, pair.l1ID, pair.l1RPC)
}

// runSameChain performs one live same-chain transfer and asserts the receipt
// came back successful.
func runSameChain(t *testing.T, chainID uint64, rpc string) {
	t.Helper()
	requireCredentials(t)

	// Source and destination are the same chain, so the second endpoint is
	// never asked for and the route is decided without touching the network.
	cfg := loadConfig(t, chainID, rpc, chainID, rpc, nil)

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	client := dialOrFail(t, ctx, cfg, chainID)
	defer client.Close()

	before := balance(t, ctx, client, cfg)
	t.Logf("chain %d: sending %s wei from %s to %s (balance before: %s wei)",
		chainID, sendAmount, cfg.SourceAddr.Hex(), cfg.DestAddr.Hex(), before)

	if before.Cmp(sendAmount) <= 0 {
		t.Skipf("chain %d: %s holds %s wei, not enough to cover %s wei plus gas",
			chainID, cfg.SourceAddr.Hex(), before, sendAmount)
	}

	b := bridge.New(cfg, client, client,
		bridge.WithConfirmTimeout(e2eTimeout),
		bridge.WithPollInterval(pollInteval),
	)

	res, err := b.SameChain(ctx, sendAmount)
	if err != nil {
		t.Fatalf("chain %d: SameChain: %v", chainID, err)
	}

	t.Logf("chain %d: tx %s", chainID, registry.ExplorerTx(chainID, res.SrcTxHash.Hex()))

	rcpt, err := b.WaitReceipt(ctx, client, res.SrcTxHash)
	if err != nil {
		t.Fatalf("chain %d: receipt: %v", chainID, err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("chain %d: receipt status = %d, want 1", chainID, rcpt.Status)
	}
	t.Logf("chain %d: receipt status 1 in block %s, gas used %d", chainID, rcpt.BlockNumber, rcpt.GasUsed)
}
