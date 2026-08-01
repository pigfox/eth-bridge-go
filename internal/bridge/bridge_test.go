package bridge

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/chain/fake"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

// A throwaway key. It funds nothing; it exists so that the tests can sign.
const testPK = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"

const destAddr = "0x000000000000000000000000000000000000dEaD"

var errBoom = errors.New("boom")

// testCfg builds a valid same-chain Base Sepolia configuration.
func testCfg(t *testing.T) config.Config {
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
			config.EnvSourceChainID: "84532",
			config.EnvDestChainID:   "84532",
			config.EnvL2RPCURL:      "https://base-sepolia.example",
		}[k]
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// noSleep is a sleeper that returns instantly, so the confirmation loops run at
// full speed in tests.
func noSleep(context.Context, time.Duration) error { return nil }

// fastOpts run the confirmation loop without spending real time in it.
func fastOpts() []Option {
	return []Option{WithSleeper(noSleep), WithPollInterval(time.Nanosecond), WithConfirmTimeout(time.Hour)}
}

// successReceipt is a mined, successful receipt.
func successReceipt() *types.Receipt {
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(100)}
}

// scriptHappySend loads everything a successful SameChain needs.
func scriptHappySend(c *fake.Client) {
	c.PushChainID(big.NewInt(84532), nil)
	c.PushNonce(42, nil)
	c.PushTipCap(big.NewInt(1_000_000), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(7_000_000)}, nil)
	c.PushGas(21000, nil)
	c.PushSend(nil)
	c.PushReceipt(successReceipt(), nil)
}

func TestSameChainHappyPath(t *testing.T) {
	cfg := testCfg(t)
	c := &fake.Client{}
	scriptHappySend(c)

	amount := big.NewInt(1_000_000_000_000_000) // 0.001 ETH
	res, err := New(cfg, c, c, fastOpts()...).SameChain(context.Background(), amount)
	if err != nil {
		t.Fatalf("SameChain: %v", err)
	}

	if res.Kind != route.KindSameChain {
		t.Errorf("Kind = %v, want KindSameChain", res.Kind)
	}
	if res.Amount.Cmp(amount) != 0 {
		t.Errorf("Amount = %s, want %s", res.Amount, amount)
	}

	sent := c.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(sent))
	}
	tx := sent[0]
	if res.SrcTxHash != tx.Hash() {
		t.Errorf("SrcTxHash = %s, want %s", res.SrcTxHash.Hex(), tx.Hash().Hex())
	}
	if tx.Nonce() != 42 {
		t.Errorf("nonce = %d, want 42", tx.Nonce())
	}
	if tx.Type() != types.DynamicFeeTxType {
		t.Errorf("tx type = %d, want DynamicFeeTxType", tx.Type())
	}
	if got := tx.To(); got == nil || *got != cfg.DestAddr {
		t.Errorf("To = %v, want %s", got, cfg.DestAddr.Hex())
	}
	if tx.Value().Cmp(amount) != 0 {
		t.Errorf("value = %s, want %s", tx.Value(), amount)
	}
	// 21000 estimated, plus the default 30% margin.
	if want := 21000 + 21000*config.DefaultGasMarginPercent/100; tx.Gas() != want {
		t.Errorf("gas = %d, want %d (estimate plus margin)", tx.Gas(), want)
	}
	// Fee cap leaves room for the base fee to double: tip + 2*baseFee.
	wantFeeCap := big.NewInt(1_000_000 + 2*7_000_000)
	if tx.GasFeeCap().Cmp(wantFeeCap) != 0 {
		t.Errorf("fee cap = %s, want %s", tx.GasFeeCap(), wantFeeCap)
	}
	if tx.ChainId().Uint64() != cfg.SourceChainID {
		t.Errorf("chain ID = %s, want %d", tx.ChainId(), cfg.SourceChainID)
	}

	// The signature must recover to the configured source account.
	from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if from != cfg.SourceAddr {
		t.Errorf("sender = %s, want %s", from.Hex(), cfg.SourceAddr.Hex())
	}

	// The estimate must describe the same transfer that was signed.
	est := c.Estimated()
	if len(est) != 1 {
		t.Fatalf("estimated %d messages, want 1", len(est))
	}
	if est[0].From != cfg.SourceAddr || est[0].To == nil || *est[0].To != cfg.DestAddr {
		t.Errorf("estimate message addressed %v -> %v", est[0].From, est[0].To)
	}
	if est[0].Value.Cmp(amount) != 0 {
		t.Errorf("estimate value = %s, want %s", est[0].Value, amount)
	}
}

func TestSameChainRejectsNonPositiveAmount(t *testing.T) {
	tests := []struct {
		name   string
		amount *big.Int
	}{
		{"nil", nil},
		{"zero", big.NewInt(0)},
		{"negative", big.NewInt(-1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &fake.Client{}
			_, err := New(testCfg(t), c, c, fastOpts()...).SameChain(context.Background(), tc.amount)
			if !errors.Is(err, ErrAmountNotPositive) {
				t.Fatalf("error = %v, want ErrAmountNotPositive", err)
			}
			// Nothing may be sent, and the node must not even be asked.
			if len(c.Sent()) != 0 {
				t.Error("a transaction was broadcast for a non-positive amount")
			}
		})
	}
}

// Each RPC in the send path gets its turn at failing. The scripted prefix is
// everything that must succeed before the failing call is reached.
func TestSameChainRPCFailures(t *testing.T) {
	amount := big.NewInt(1_000)

	tests := []struct {
		name    string
		script  func(*fake.Client)
		wantErr error
		wantIn  string
	}{
		{
			name:    "chain ID read fails",
			script:  func(c *fake.Client) { c.PushChainID(nil, errBoom) },
			wantErr: errBoom,
			wantIn:  "read chain ID",
		},
		{
			name:    "endpoint serves the wrong chain",
			script:  func(c *fake.Client) { c.PushChainID(big.NewInt(11155111), nil) },
			wantErr: ErrChainMismatch,
			wantIn:  "11155111",
		},
		{
			name: "endpoint reports a chain ID too large for uint64",
			script: func(c *fake.Client) {
				huge := new(big.Int).Lsh(big.NewInt(1), 200)
				c.PushChainID(huge, nil)
			},
			wantErr: ErrChainMismatch,
			wantIn:  "endpoint reports",
		},
		{
			name: "nonce read fails",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(0, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "read pending nonce",
		},
		{
			name: "tip cap suggestion fails",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(nil, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "suggest gas tip cap",
		},
		{
			name: "header read fails",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(nil, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "read latest header",
		},
		{
			name: "chain has no base fee",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(&types.Header{}, nil)
			},
			wantErr: ErrNoBaseFee,
			wantIn:  "base fee",
		},
		{
			name: "gas estimation fails",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(&types.Header{BaseFee: big.NewInt(2)}, nil)
				c.PushGas(0, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "estimate gas",
		},
		{
			name: "broadcast fails",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(&types.Header{BaseFee: big.NewInt(2)}, nil)
				c.PushGas(21000, nil)
				c.PushSend(errBoom)
			},
			wantErr: errBoom,
			wantIn:  "broadcast transaction",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &fake.Client{}
			tc.script(c)

			res, err := New(testCfg(t), c, c, fastOpts()...).SameChain(context.Background(), amount)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want errors.Is %v", err, tc.wantErr)
			}
			if got := err.Error(); !strings.Contains(got, tc.wantIn) {
				t.Errorf("error %q does not mention %q", got, tc.wantIn)
			}
			if res.SrcTxHash != (common.Hash{}) {
				t.Errorf("a failed send returned a transaction hash: %s", res.SrcTxHash.Hex())
			}
		})
	}
}

func TestSameChainSignerFailure(t *testing.T) {
	c := &fake.Client{}
	c.PushChainID(big.NewInt(84532), nil)
	c.PushNonce(1, nil)
	c.PushTipCap(big.NewInt(1), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(2)}, nil)
	c.PushGas(21000, nil)

	failing := WithSigner(func(*types.Transaction, *big.Int) (*types.Transaction, error) {
		return nil, errBoom
	})
	opts := append(fastOpts(), failing)

	_, err := New(testCfg(t), c, c, opts...).SameChain(context.Background(), big.NewInt(1))
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want errBoom", err)
	}
	if !strings.Contains(err.Error(), "sign transaction") {
		t.Errorf("error %q does not mention signing", err)
	}
	if len(c.Sent()) != 0 {
		t.Error("an unsigned transaction was broadcast")
	}
}

func TestSameChainRevertedTransaction(t *testing.T) {
	c := &fake.Client{}
	c.PushChainID(big.NewInt(84532), nil)
	c.PushNonce(1, nil)
	c.PushTipCap(big.NewInt(1), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(2)}, nil)
	c.PushGas(21000, nil)
	c.PushSend(nil)
	c.PushReceipt(&types.Receipt{Status: types.ReceiptStatusFailed}, nil)

	_, err := New(testCfg(t), c, c, fastOpts()...).SameChain(context.Background(), big.NewInt(1))
	if !errors.Is(err, ErrTxReverted) {
		t.Fatalf("error = %v, want ErrTxReverted", err)
	}
}

func TestWaitReceiptPollsUntilMined(t *testing.T) {
	c := &fake.Client{}
	c.PushReceipt(nil, ethereum.NotFound)
	c.PushReceipt(nil, ethereum.NotFound)
	c.PushReceipt(successReceipt(), nil)

	slept := 0
	opts := []Option{
		WithConfirmTimeout(time.Hour),
		WithPollInterval(time.Nanosecond),
		WithSleeper(func(context.Context, time.Duration) error { slept++; return nil }),
	}

	b := New(testCfg(t), c, c, opts...)
	rcpt, err := b.WaitReceipt(context.Background(), c, common.HexToHash("0xabc"))
	if err != nil {
		t.Fatalf("WaitReceipt: %v", err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		t.Errorf("status = %d, want 1", rcpt.Status)
	}
	if slept != 2 {
		t.Errorf("slept %d times, want 2 (once per pending poll)", slept)
	}
}

func TestWaitReceiptErrors(t *testing.T) {
	hash := common.HexToHash("0xabc")

	t.Run("non-pending read error is fatal", func(t *testing.T) {
		c := &fake.Client{}
		c.PushReceipt(nil, errBoom)

		b := New(testCfg(t), c, c, fastOpts()...)
		_, err := b.WaitReceipt(context.Background(), c, hash)
		if !errors.Is(err, errBoom) {
			t.Fatalf("error = %v, want errBoom", err)
		}
		if !strings.Contains(err.Error(), hash.Hex()) {
			t.Errorf("error %q does not name the transaction", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		c := &fake.Client{}
		c.PushReceipt(nil, ethereum.NotFound)

		// A deadline already in the past: the first pending poll is also the
		// last one.
		b := New(testCfg(t), c, c, WithConfirmTimeout(-time.Second), WithSleeper(noSleep))
		_, err := b.WaitReceipt(context.Background(), c, hash)
		if !errors.Is(err, ErrReceiptTimeout) {
			t.Fatalf("error = %v, want ErrReceiptTimeout", err)
		}
	})

	t.Run("interrupted wait", func(t *testing.T) {
		c := &fake.Client{}
		c.PushReceipt(nil, ethereum.NotFound)

		b := New(testCfg(t), c, c,
			WithConfirmTimeout(time.Hour),
			WithSleeper(func(context.Context, time.Duration) error { return context.Canceled }),
		)
		_, err := b.WaitReceipt(context.Background(), c, hash)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if !strings.Contains(err.Error(), "waiting for receipt") {
			t.Errorf("error %q does not say what was being waited for", err)
		}
	})
}

func TestSleep(t *testing.T) {
	t.Run("returns after the interval", func(t *testing.T) {
		if err := Sleep(context.Background(), time.Nanosecond); err != nil {
			t.Fatalf("Sleep: %v", err)
		}
	})

	t.Run("returns early when the context is done", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Fatalf("Sleep = %v, want context.Canceled", err)
		}
	})
}

func TestLocalSignerProducesARecoverableSignature(t *testing.T) {
	key, err := crypto.HexToECDSA(testPK)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	chainID := big.NewInt(84532)
	to := common.HexToAddress(destAddr)

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: chainID, Nonce: 1, GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(2), Gas: 21000, To: &to, Value: big.NewInt(3),
	})
	signed, err := LocalSigner(key)(tx, chainID)
	if err != nil {
		t.Fatalf("LocalSigner: %v", err)
	}
	from, err := types.Sender(types.LatestSignerForChainID(chainID), signed)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if from != crypto.PubkeyToAddress(key.PublicKey) {
		t.Errorf("sender = %s, want %s", from.Hex(), crypto.PubkeyToAddress(key.PublicKey).Hex())
	}
}

// Withdrawal is recognised by the router but does not ship in this version. It
// must say so rather than do something surprising.
func TestWithdrawInitiateIsNotImplemented(t *testing.T) {
	c := &fake.Client{}
	b := New(testCfg(t), c, c)

	res, err := b.WithdrawInitiate(context.Background(), big.NewInt(1))
	if !errors.Is(err, route.ErrNotImplemented) {
		t.Fatalf("error = %v, want route.ErrNotImplemented", err)
	}
	if res.SrcTxHash != (common.Hash{}) || res.Amount != nil {
		t.Errorf("result = %+v, want the zero value", res)
	}
	if len(c.Sent()) != 0 {
		t.Error("an unimplemented route broadcast a transaction")
	}
}

func TestNewAppliesDefaultsAndOptions(t *testing.T) {
	c := &fake.Client{}

	b := New(testCfg(t), c, c)
	if b.confirmTimeout != config.DefaultConfirmTimeout {
		t.Errorf("confirmTimeout = %s, want %s", b.confirmTimeout, config.DefaultConfirmTimeout)
	}
	if b.pollInterval != config.DefaultPollInterval {
		t.Errorf("pollInterval = %s, want %s", b.pollInterval, config.DefaultPollInterval)
	}
	if b.sign == nil || b.sleep == nil {
		t.Error("New left the signer or the sleeper nil")
	}
	if b.src != chain.Client(c) || b.dst != chain.Client(c) {
		t.Error("New did not record both clients")
	}

	b2 := New(testCfg(t), c, c, WithConfirmTimeout(time.Second), WithPollInterval(time.Millisecond))
	if b2.confirmTimeout != time.Second || b2.pollInterval != time.Millisecond {
		t.Errorf("options not applied: %s / %s", b2.confirmTimeout, b2.pollInterval)
	}
}

// The gas margin exists because eth_estimateGas is not reliable for OP Stack
// deposits, so it must actually be applied and must be overridable.
func TestGasMargin(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		estimate uint64
		want     uint64
	}{
		{"default margin", nil, 100_000, 130_000},
		{"custom margin", []Option{WithGasMarginPercent(50)}, 100_000, 150_000},
		{"no margin", []Option{WithGasMarginPercent(0)}, 100_000, 100_000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &fake.Client{}
			c.PushChainID(big.NewInt(84532), nil)
			c.PushNonce(1, nil)
			c.PushTipCap(big.NewInt(1), nil)
			c.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
			c.PushGas(tc.estimate, nil)
			c.PushSend(nil)
			c.PushReceipt(successReceipt(), nil)

			opts := append(fastOpts(), tc.opts...)
			if _, err := New(testCfg(t), c, c, opts...).SameChain(context.Background(), big.NewInt(1)); err != nil {
				t.Fatalf("SameChain: %v", err)
			}
			if got := c.Sent()[0].Gas(); got != tc.want {
				t.Errorf("gas = %d, want %d", got, tc.want)
			}
		})
	}
}
