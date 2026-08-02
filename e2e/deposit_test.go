//go:build e2e

package e2e

import (
	"context"
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/config"
)

// depositAmount is the value bridged by T3.
var depositAmount = big.NewInt(500_000_000_000_000) // 0.0005 ETH

// T3: a real L1 to L2 deposit through the Base Standard Bridge.
//
// The assertions are the two that matter and one that is easy to skip: the L1
// receipt succeeded, the L2 balance actually moved, and the L2 transaction hash
// this tool *derived* from the L1 receipt resolves to a real transaction on
// Base Sepolia. That last one is the only way to know the derivation is right
// rather than merely well-formed.
func TestT3DepositEthSepoliaToBaseSepolia(t *testing.T) {
	requireEnv(t,
		config.EnvSourceAddr, config.EnvSourcePK, config.EnvDestAddr,
		envEthSepoliaRPC, envBaseSepoliaRPC,
	)

	cfg, err := config.Load(func(k string) string {
		switch k {
		case config.EnvSourceChainID:
			return strconvUint(config.ChainIDEthSepolia)
		case config.EnvDestChainID:
			return strconvUint(config.ChainIDBaseSepolia)
		case config.EnvSourceRPCURL:
			return os.Getenv(envEthSepoliaRPC)
		case config.EnvDestRPCURL:
			return os.Getenv(envBaseSepoliaRPC)
		default:
			return os.Getenv(k)
		}
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	l1 := dialOrFail(t, ctx, cfg, config.ChainIDEthSepolia)
	defer l1.Close()
	l2 := dialOrFail(t, ctx, cfg, config.ChainIDBaseSepolia)
	defer l2.Close()

	before := balance(t, ctx, l1, cfg)
	t.Logf("L1 balance before: %s wei", before)
	if before.Cmp(depositAmount) <= 0 {
		t.Skipf("%s holds %s wei on Eth Sepolia, not enough to deposit %s wei plus gas",
			cfg.SourceAddr.Hex(), before, depositAmount)
	}

	b := bridge.New(cfg, l1, l2,
		bridge.WithConfirmTimeout(e2eTimeout),
		bridge.WithPollInterval(pollInteval),
	)

	res, err := b.Deposit(ctx, depositAmount)
	if res.SrcTxHash != (common.Hash{}) {
		t.Logf("L1 tx: https://sepolia.etherscan.io/tx/%s", res.SrcTxHash.Hex())
	}
	if res.DstTxHash != (common.Hash{}) {
		t.Logf("L2 tx: https://sepolia.basescan.org/tx/%s", res.DstTxHash.Hex())
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
		t.Fatalf("Credited = %v, want a positive delta on L2", res.Credited)
	}
	t.Logf("L2 credited %s wei to %s", res.Credited, cfg.DestAddr.Hex())
	if res.Credited.Cmp(depositAmount) != 0 {
		t.Logf("note: credited %s wei against a deposit of %s wei", res.Credited, depositAmount)
	}

	// The derived L2 hash is a real transaction. If the OP Stack source-hash
	// derivation were wrong, this is what would catch it.
	l2rcpt, err := l2.TransactionReceipt(ctx, res.DstTxHash)
	if err != nil {
		t.Fatalf("derived L2 hash %s does not resolve on Base Sepolia: %v", res.DstTxHash.Hex(), err)
	}
	if l2rcpt.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("L2 receipt status = %d, want 1", l2rcpt.Status)
	}
	t.Logf("L2 receipt status 1 in block %s — derived hash confirmed", l2rcpt.BlockNumber)
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
