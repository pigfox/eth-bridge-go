package bridge

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/pigfox/eth-bridge-go/internal/chain/fake"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/opstack"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

// depositCfg is a valid Eth Sepolia -> Base Sepolia deposit configuration.
func depositCfg(t *testing.T) config.Config {
	t.Helper()
	key, err := crypto.HexToECDSA(testPK)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	src := crypto.PubkeyToAddress(key.PublicKey).Hex()

	cfg, err := config.Load(func(k string) string {
		return map[string]string{
			config.EnvSourceAddr:    src,
			config.EnvSourcePK:      testPK,
			config.EnvDestAddr:      destAddr,
			config.EnvSourceChainID: "11155111",
			config.EnvDestChainID:   "84532",
			config.EnvSourceRPCURL:  "https://eth-sepolia.example",
			config.EnvDestRPCURL:    "https://base-sepolia.example",
		}[k]
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// depositReceipt is an L1 receipt carrying a well-formed TransactionDeposited
// log, so that the L2 hash derivation has something real to work on.
func depositReceipt(amount *big.Int) *types.Receipt {
	opaque := make([]byte, 0, 73)
	opaque = append(opaque, common.LeftPadBytes(amount.Bytes(), 32)...)
	opaque = append(opaque, common.LeftPadBytes(amount.Bytes(), 32)...)
	opaque = append(opaque, common.LeftPadBytes(big.NewInt(200000).Bytes(), 8)...)
	opaque = append(opaque, 0)

	data := append(common.LeftPadBytes(big.NewInt(32).Bytes(), 32),
		common.LeftPadBytes(big.NewInt(int64(len(opaque))).Bytes(), 32)...)
	data = append(data, opaque...)
	data = append(data, make([]byte, (32-len(opaque)%32)%32)...)

	return &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(500),
		Logs: []*types.Log{{
			Topics: []common.Hash{
				opstack.TransactionDepositedTopic,
				common.HexToHash("0xaaaa"),
				common.HexToHash("0xbbbb"),
				common.BigToHash(big.NewInt(0)),
			},
			Data:      data,
			BlockHash: common.HexToHash("0xf00d"),
			Index:     1,
		}},
	}
}

// scriptDeposit loads the L1 and L2 fakes for a successful deposit.
func scriptDeposit(l1, l2 *fake.Client, amount *big.Int) {
	l1.PushChainID(big.NewInt(11155111), nil)
	l2.PushChainID(big.NewInt(84532), nil)
	l2.PushBalance(big.NewInt(1_000), nil) // before
	l1.PushNonce(5, nil)
	l1.PushTipCap(big.NewInt(1_000_000), nil)
	l1.PushHeader(&types.Header{BaseFee: big.NewInt(2_000_000)}, nil)
	l1.PushGas(120_000, nil)
	l1.PushSend(nil)
	l1.PushReceipt(depositReceipt(amount), nil)
}

func TestDepositHappyPath(t *testing.T) {
	cfg := depositCfg(t)
	amount := big.NewInt(500_000_000_000_000) // 0.0005 ETH

	l1, l2 := &fake.Client{}, &fake.Client{}
	scriptDeposit(l1, l2, amount)
	// The credit shows up on the second poll.
	l2.PushBalance(big.NewInt(1_000), nil)
	l2.PushBalance(new(big.Int).Add(big.NewInt(1_000), amount), nil)

	res, err := New(cfg, l1, l2, fastOpts()...).Deposit(context.Background(), amount)
	if err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	if res.Kind != route.KindDeposit {
		t.Errorf("Kind = %v, want KindDeposit", res.Kind)
	}
	if res.Credited.Cmp(amount) != 0 {
		t.Errorf("Credited = %s, want %s", res.Credited, amount)
	}
	if res.DstTxHash == (common.Hash{}) {
		t.Error("DstTxHash was not derived")
	}

	// The L1 transaction must go to the standard bridge, carry the value, and
	// call depositETHTo with the configured destination.
	sent := l1.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d L1 transactions, want 1", len(sent))
	}
	tx := sent[0]
	if got := tx.To(); got == nil || *got != common.HexToAddress(config.L1StandardBridgeSepolia) {
		t.Errorf("To = %v, want the L1 standard bridge", got)
	}
	if tx.Value().Cmp(amount) != 0 {
		t.Errorf("value = %s, want %s", tx.Value(), amount)
	}

	wantSelector := crypto.Keccak256([]byte("depositETHTo(address,uint32,bytes)"))[:4]
	if got := tx.Data(); len(got) < 4 || string(got[:4]) != string(wantSelector) {
		t.Errorf("calldata selector = %x, want %x", got[:4], wantSelector)
	}
	if got := common.BytesToAddress(tx.Data()[4+12 : 4+32]); got != cfg.DestAddr {
		t.Errorf("depositETHTo _to = %s, want %s", got.Hex(), cfg.DestAddr.Hex())
	}
	if got := new(big.Int).SetBytes(tx.Data()[36:68]).Uint64(); got != uint64(config.DefaultDepositMinGasLimit) {
		t.Errorf("_minGasLimit = %d, want %d", got, config.DefaultDepositMinGasLimit)
	}

	// The derived hash must be exactly what opstack computes for that receipt:
	// the bridge must not be inventing one.
	wantHash, err := opstack.L2TxHash(depositReceipt(amount))
	if err != nil {
		t.Fatalf("L2TxHash: %v", err)
	}
	if res.DstTxHash != wantHash {
		t.Errorf("DstTxHash = %s, want %s", res.DstTxHash.Hex(), wantHash.Hex())
	}
}

func TestDepositRejectsNonPositiveAmount(t *testing.T) {
	for _, amount := range []*big.Int{nil, big.NewInt(0), big.NewInt(-5)} {
		l1, l2 := &fake.Client{}, &fake.Client{}
		_, err := New(depositCfg(t), l1, l2, fastOpts()...).Deposit(context.Background(), amount)
		if !errors.Is(err, ErrAmountNotPositive) {
			t.Errorf("amount %v: error = %v, want ErrAmountNotPositive", amount, err)
		}
		if len(l1.Sent()) != 0 {
			t.Error("a deposit was broadcast for a non-positive amount")
		}
	}
}

func TestDepositFailuresBeforeBroadcast(t *testing.T) {
	amount := big.NewInt(1_000)

	tests := []struct {
		name    string
		script  func(l1, l2 *fake.Client)
		opts    []Option
		wantErr error
		wantIn  string
	}{
		{
			name:    "L1 endpoint is the wrong chain",
			script:  func(l1, _ *fake.Client) { l1.PushChainID(big.NewInt(84532), nil) },
			wantErr: ErrChainMismatch,
			wantIn:  "endpoint reports 84532",
		},
		{
			name: "L2 endpoint is the wrong chain",
			script: func(l1, l2 *fake.Client) {
				l1.PushChainID(big.NewInt(11155111), nil)
				l2.PushChainID(big.NewInt(11155111), nil)
			},
			wantErr: ErrChainMismatch,
			wantIn:  "configuration says 84532",
		},
		{
			name: "destination balance is unreadable",
			script: func(l1, l2 *fake.Client) {
				l1.PushChainID(big.NewInt(11155111), nil)
				l2.PushChainID(big.NewInt(84532), nil)
				l2.PushBalance(nil, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "read destination balance",
		},
		{
			name: "calldata cannot be encoded",
			script: func(l1, l2 *fake.Client) {
				l1.PushChainID(big.NewInt(11155111), nil)
				l2.PushChainID(big.NewInt(84532), nil)
				l2.PushBalance(big.NewInt(0), nil)
			},
			opts: []Option{WithDepositEncoder(func(common.Address, uint32) ([]byte, error) {
				return nil, errBoom
			})},
			wantErr: errBoom,
			wantIn:  "encode deposit calldata",
		},
		{
			name: "L1 nonce is unreadable",
			script: func(l1, l2 *fake.Client) {
				l1.PushChainID(big.NewInt(11155111), nil)
				l2.PushChainID(big.NewInt(84532), nil)
				l2.PushBalance(big.NewInt(0), nil)
				l1.PushNonce(0, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "read pending nonce",
		},
		{
			name: "broadcast is rejected",
			script: func(l1, l2 *fake.Client) {
				l1.PushChainID(big.NewInt(11155111), nil)
				l2.PushChainID(big.NewInt(84532), nil)
				l2.PushBalance(big.NewInt(0), nil)
				l1.PushNonce(1, nil)
				l1.PushTipCap(big.NewInt(1), nil)
				l1.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
				l1.PushGas(100_000, nil)
				l1.PushSend(errBoom)
			},
			wantErr: errBoom,
			wantIn:  "broadcast deposit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l1, l2 := &fake.Client{}, &fake.Client{}
			tc.script(l1, l2)

			res, err := New(depositCfg(t), l1, l2, append(fastOpts(), tc.opts...)...).
				Deposit(context.Background(), amount)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want errors.Is %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
			// Nothing reached L1 successfully, so no hash may be reported.
			if res.SrcTxHash != (common.Hash{}) {
				t.Errorf("a pre-broadcast failure reported hash %s", res.SrcTxHash.Hex())
			}
		})
	}
}

// Once the L1 transaction is out, later failures must still report the L1 hash:
// that hash is how the operator finds out what actually happened.
func TestDepositFailuresAfterBroadcastStillReportTheL1Hash(t *testing.T) {
	amount := big.NewInt(1_000)

	t.Run("L1 transaction reverts", func(t *testing.T) {
		l1, l2 := &fake.Client{}, &fake.Client{}
		l1.PushChainID(big.NewInt(11155111), nil)
		l2.PushChainID(big.NewInt(84532), nil)
		l2.PushBalance(big.NewInt(0), nil)
		l1.PushNonce(1, nil)
		l1.PushTipCap(big.NewInt(1), nil)
		l1.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
		l1.PushGas(100_000, nil)
		l1.PushSend(nil)
		l1.PushReceipt(&types.Receipt{Status: types.ReceiptStatusFailed}, nil)

		res, err := New(depositCfg(t), l1, l2, fastOpts()...).Deposit(context.Background(), amount)
		if !errors.Is(err, ErrTxReverted) {
			t.Fatalf("error = %v, want ErrTxReverted", err)
		}
		if res.SrcTxHash == (common.Hash{}) {
			t.Error("a reverted deposit did not report its L1 hash")
		}
	})

	t.Run("receipt has no deposit log", func(t *testing.T) {
		l1, l2 := &fake.Client{}, &fake.Client{}
		l1.PushChainID(big.NewInt(11155111), nil)
		l2.PushChainID(big.NewInt(84532), nil)
		l2.PushBalance(big.NewInt(0), nil)
		l1.PushNonce(1, nil)
		l1.PushTipCap(big.NewInt(1), nil)
		l1.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
		l1.PushGas(100_000, nil)
		l1.PushSend(nil)
		l1.PushReceipt(&types.Receipt{Status: types.ReceiptStatusSuccessful}, nil)

		res, err := New(depositCfg(t), l1, l2, fastOpts()...).Deposit(context.Background(), amount)
		if !errors.Is(err, opstack.ErrNoDepositLog) {
			t.Fatalf("error = %v, want opstack.ErrNoDepositLog", err)
		}
		if !strings.Contains(err.Error(), "derive L2 transaction") {
			t.Errorf("error %q does not say what failed", err)
		}
		if res.SrcTxHash == (common.Hash{}) {
			t.Error("the L1 hash was not reported")
		}
	})

	t.Run("credit never arrives", func(t *testing.T) {
		l1, l2 := &fake.Client{}, &fake.Client{}
		scriptDeposit(l1, l2, amount)
		l2.PushBalance(big.NewInt(1_000), nil) // unchanged

		res, err := New(depositCfg(t), l1, l2,
			WithSleeper(noSleep), WithConfirmTimeout(-time.Second),
		).Deposit(context.Background(), amount)
		if !errors.Is(err, ErrDepositNotCredited) {
			t.Fatalf("error = %v, want ErrDepositNotCredited", err)
		}
		if res.SrcTxHash == (common.Hash{}) || res.DstTxHash == (common.Hash{}) {
			t.Error("both hashes should be reported even when the credit is late")
		}
		if res.Credited != nil {
			t.Errorf("Credited = %s, want nil when nothing arrived", res.Credited)
		}
	})

	t.Run("destination balance stops responding", func(t *testing.T) {
		l1, l2 := &fake.Client{}, &fake.Client{}
		scriptDeposit(l1, l2, amount)
		l2.PushBalance(nil, errBoom)

		_, err := New(depositCfg(t), l1, l2, fastOpts()...).Deposit(context.Background(), amount)
		if !errors.Is(err, errBoom) {
			t.Fatalf("error = %v, want errBoom", err)
		}
	})

	t.Run("the wait is interrupted", func(t *testing.T) {
		l1, l2 := &fake.Client{}, &fake.Client{}
		scriptDeposit(l1, l2, amount)
		l2.PushBalance(big.NewInt(1_000), nil) // unchanged, so it sleeps

		_, err := New(depositCfg(t), l1, l2,
			WithConfirmTimeout(time.Hour),
			WithSleeper(func(context.Context, time.Duration) error { return context.Canceled }),
		).Deposit(context.Background(), amount)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if !strings.Contains(err.Error(), "waiting for deposit credit") {
			t.Errorf("error %q does not say what was being waited for", err)
		}
	})
}

func TestDepositOptions(t *testing.T) {
	addr := common.HexToAddress("0xcafe")
	b := New(depositCfg(t), &fake.Client{}, &fake.Client{},
		WithL1StandardBridge(addr),
		WithDepositMinGasLimit(42),
	)
	if b.l1Bridge != addr {
		t.Errorf("l1Bridge = %s, want %s", b.l1Bridge.Hex(), addr.Hex())
	}
	if b.depositMinGasLimit != 42 {
		t.Errorf("depositMinGasLimit = %d, want 42", b.depositMinGasLimit)
	}

	// And the defaults are the verified Base Sepolia values.
	d := New(depositCfg(t), &fake.Client{}, &fake.Client{})
	if d.l1Bridge != common.HexToAddress(config.L1StandardBridgeSepolia) {
		t.Errorf("default l1Bridge = %s", d.l1Bridge.Hex())
	}
	if d.depositMinGasLimit != config.DefaultDepositMinGasLimit {
		t.Errorf("default minGasLimit = %d", d.depositMinGasLimit)
	}
	if d.encodeDeposit == nil {
		t.Error("New left the deposit encoder nil")
	}
}
