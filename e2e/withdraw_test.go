//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/config"
)

// withdrawAmount is the value withdrawn by T4.
var withdrawAmount = big.NewInt(10_000_000_000_000) // 0.00001 ETH

// T4: a real L2 to L1 withdrawal, initiated only.
//
// This is the first of three transactions. The test asserts what this tool
// actually claims to do — that the withdrawal was initiated, that the
// MessagePassed parameters were captured, and that they were written somewhere
// they can be read from a week later. It does not assert anything arrives on
// L1, because nothing will for about seven days and this tool does not prove or
// finalize.
func TestT4WithdrawInitiateBaseSepoliaToEthSepolia(t *testing.T) {
	requireEnv(t,
		config.EnvSourceAddr, config.EnvSourcePK, config.EnvDestAddr,
		config.EnvL2RPCURL,
	)

	dir := t.TempDir()
	cfg, err := config.Load(func(k string) string {
		switch k {
		case config.EnvSourceChainID:
			return strconvUint(config.ChainIDBaseSepolia)
		case config.EnvDestChainID:
			return strconvUint(config.ChainIDEthSepolia)
		case config.EnvL1RPCURL:
			// A withdrawal only touches L2, but config demands an endpoint for
			// every chain the route names.
			if v := os.Getenv(k); v != "" {
				return v
			}
			return "https://ethereum-sepolia-rpc.publicnode.com"
		case config.EnvWithdrawalsDir:
			return dir
		default:
			return os.Getenv(k)
		}
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	l2 := dialOrFail(t, ctx, cfg, config.ChainIDBaseSepolia)
	defer l2.Close()

	before := balance(t, ctx, l2, cfg)
	t.Logf("L2 balance before: %s wei", before)
	if before.Cmp(withdrawAmount) <= 0 {
		t.Skipf("%s holds %s wei on Base Sepolia, not enough to withdraw %s wei plus gas",
			cfg.SourceAddr.Hex(), before, withdrawAmount)
	}

	b := bridge.New(cfg, l2, l2,
		bridge.WithConfirmTimeout(e2eTimeout),
		bridge.WithPollInterval(pollInteval),
		bridge.WithWithdrawalsDir(dir),
	)

	res, err := b.WithdrawInitiate(ctx, withdrawAmount)
	if res.SrcTxHash != (common.Hash{}) {
		t.Logf("L2 tx: https://sepolia.basescan.org/tx/%s", res.SrcTxHash.Hex())
	}
	if err != nil {
		t.Fatalf("WithdrawInitiate: %v", err)
	}

	rcpt, err := b.WaitReceipt(ctx, l2, res.SrcTxHash)
	if err != nil {
		t.Fatalf("L2 receipt: %v", err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("L2 receipt status = %d, want 1", rcpt.Status)
	}
	t.Logf("L2 receipt status 1 in block %s, gas used %d", rcpt.BlockNumber, rcpt.GasUsed)

	if res.Withdrawal == nil {
		t.Fatal("no withdrawal parameters were captured")
	}
	w := res.Withdrawal
	t.Logf("withdrawal hash: %s", w.WithdrawalHash.Hex())
	t.Logf("nonce: %s  target: %s  value: %s wei  l2 block: %s",
		w.Nonce, w.Target.Hex(), w.Value, w.L2BlockNumber)

	if w.WithdrawalHash == (common.Hash{}) {
		t.Error("withdrawal hash is zero")
	}
	if w.Value.Cmp(withdrawAmount) != 0 {
		t.Errorf("withdrawal value = %s, want %s", w.Value, withdrawAmount)
	}
	if w.L2BlockNumber.Sign() <= 0 {
		t.Errorf("L2BlockNumber = %s, want positive", w.L2BlockNumber)
	}

	// The record has to be on disk and readable, because it is the only copy of
	// what proving this withdrawal will need.
	want := filepath.Join(dir, res.SrcTxHash.Hex()+".json")
	if res.WithdrawalPath != want {
		t.Errorf("WithdrawalPath = %s, want %s", res.WithdrawalPath, want)
	}
	raw, err := os.ReadFile(res.WithdrawalPath)
	if err != nil {
		t.Fatalf("read the saved withdrawal: %v", err)
	}
	var saved map[string]string
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("the saved withdrawal is not readable JSON: %v", err)
	}
	for _, field := range []string{"nonce", "sender", "target", "value", "gasLimit", "data", "withdrawalHash", "l2BlockNumber"} {
		if saved[field] == "" {
			t.Errorf("saved record is missing %q", field)
		}
	}
	if saved["withdrawalHash"] != w.WithdrawalHash.Hex() {
		t.Errorf("saved withdrawalHash = %s, want %s", saved["withdrawalHash"], w.WithdrawalHash.Hex())
	}
	t.Logf("recorded at %s", res.WithdrawalPath)
	t.Log("NOTE: this withdrawal is NOT complete. Prove and finalize (~7 days) are not performed by this tool.")
}
