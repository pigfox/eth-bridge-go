//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/registry"
)

// T1: a real L1 to L2 deposit through the Standard Bridge, on whatever pair the
// suite is pointed at.
//
// This runs first because it is also what funds the L2 side for the tests that
// follow it.
//
// The assertions are the two that matter and one that is easy to skip: the L1
// receipt succeeded, the L2 balance actually moved, and the L2 transaction hash
// this tool *derived* from the L1 receipt resolves to a real transaction on the
// rollup. That last one is the only way to know the derivation is right rather
// than merely well-formed.
func TestT1Deposit(t *testing.T) {
	requireCredentials(t)
	pair := livePair(t)

	cfg := loadConfig(t, pair.l1ID, pair.l1RPC, pair.l2ID, pair.l2RPC, nil)

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	l1 := dialOrFail(t, ctx, cfg, pair.l1ID)
	defer l1.Close()
	l2 := dialOrFail(t, ctx, cfg, pair.l2ID)
	defer l2.Close()

	// The addresses come from the chains. Nothing in this test knows any.
	rt := resolveLive(t, ctx, cfg, l1, l2)

	before := balance(t, ctx, l1, cfg)
	t.Logf("chain %d balance before: %s wei", pair.l1ID, before)
	if before.Cmp(depositAmount) <= 0 {
		t.Skipf("%s holds %s wei on chain %d, not enough to deposit %s wei plus gas",
			cfg.SourceAddr.Hex(), before, pair.l1ID, depositAmount)
	}

	b := bridge.New(cfg, l1, l2,
		bridge.WithL1StandardBridge(rt.Addrs.L1StandardBridge),
		bridge.WithConfirmTimeout(e2eTimeout),
		bridge.WithPollInterval(pollInteval),
	)

	res, err := b.Deposit(ctx, depositAmount)
	if res.SrcTxHash != (common.Hash{}) {
		t.Logf("L1 tx: %s", registry.ExplorerTx(pair.l1ID, res.SrcTxHash.Hex()))
	}
	if res.DstTxHash != (common.Hash{}) {
		t.Logf("L2 tx: %s", registry.ExplorerTx(pair.l2ID, res.DstTxHash.Hex()))
	}
	if err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// The L1 side succeeded.
	rcpt, err := b.WaitReceipt(ctx, l1, res.SrcTxHash)
	if err != nil {
		t.Fatalf("L1 receipt: %v", err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("L1 receipt status = %d, want 1", rcpt.Status)
	}
	t.Logf("L1 receipt status 1 in block %s, gas used %d", rcpt.BlockNumber, rcpt.GasUsed)

	// The ETH actually arrived.
	if res.Credited == nil || res.Credited.Sign() <= 0 {
		t.Fatalf("Credited = %v, want a positive delta on chain %d", res.Credited, pair.l2ID)
	}
	t.Logf("chain %d credited %s wei to %s", pair.l2ID, res.Credited, cfg.DestAddr.Hex())
	if res.Credited.Cmp(depositAmount) != 0 {
		t.Logf("note: credited %s wei against a deposit of %s wei", res.Credited, depositAmount)
	}

	// The derived L2 hash is a real transaction. If the OP Stack source-hash
	// derivation were wrong, this is what would catch it.
	l2rcpt, err := l2.TransactionReceipt(ctx, res.DstTxHash)
	if err != nil {
		t.Fatalf("derived L2 hash %s does not resolve on chain %d: %v", res.DstTxHash.Hex(), pair.l2ID, err)
	}
	if l2rcpt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("L2 receipt status = %d, want 1", l2rcpt.Status)
	}
	t.Logf("L2 receipt status 1 in block %s — derived hash confirmed", l2rcpt.BlockNumber)
}
