package bridge

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/pigfox/eth-bridge-go/internal/chain/fake"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/opstack"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

// withdrawCfg is a valid Base Sepolia -> Eth Sepolia withdrawal configuration.
func withdrawCfg(t *testing.T) config.Config {
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
			config.EnvDestChainID:   "11155111",
			config.EnvL1RPCURL:      "https://eth-sepolia.example",
			config.EnvL2RPCURL:      "https://base-sepolia.example",
		}[k]
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// messagePassedReceipt is an L2 receipt carrying a well-formed MessagePassed
// log.
func messagePassedReceipt(amount *big.Int) *types.Receipt {
	w := func(v *big.Int) []byte { return common.LeftPadBytes(v.Bytes(), 32) }

	data := make([]byte, 0, 160)
	data = append(data, w(amount)...)
	data = append(data, w(big.NewInt(200000))...)
	data = append(data, w(big.NewInt(128))...)
	data = append(data, common.HexToHash("0xfeed").Bytes()...)
	data = append(data, w(big.NewInt(0))...)

	return &types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(44917575),
		Logs: []*types.Log{{
			Topics: []common.Hash{
				opstack.MessagePassedTopic,
				common.BigToHash(big.NewInt(9)),
				common.BytesToHash(common.HexToAddress("0x4200000000000000000000000000000000000007").Bytes()),
				common.BytesToHash(common.HexToAddress("0xC34855F4De64F1840e5686e64278da901e261f20").Bytes()),
			},
			Data:        data,
			BlockNumber: 44917575,
			TxHash:      common.HexToHash("0xabc"),
		}},
	}
}

// scriptWithdraw loads the L2 fake for a successful withdrawal initiation.
func scriptWithdraw(c *fake.Client, amount *big.Int) {
	c.PushChainID(big.NewInt(84532), nil)
	c.PushNonce(3, nil)
	c.PushTipCap(big.NewInt(1_000_000), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(1_000_000)}, nil)
	c.PushGas(150_000, nil)
	c.PushSend(nil)
	c.PushReceipt(messagePassedReceipt(amount), nil)
}

// recorder captures what would have been written to disk.
type recorder struct {
	hash common.Hash
	w    opstack.Withdrawal
	err  error
}

func (r *recorder) write(h common.Hash, w opstack.Withdrawal) (string, error) {
	r.hash, r.w = h, w
	if r.err != nil {
		return "", r.err
	}
	return "/tmp/withdrawals/" + h.Hex() + ".json", nil
}

func TestWithdrawInitiateHappyPath(t *testing.T) {
	cfg := withdrawCfg(t)
	amount := big.NewInt(10_000_000_000_000)

	c := &fake.Client{}
	scriptWithdraw(c, amount)
	rec := &recorder{}

	opts := append(fastOpts(), WithWithdrawalWriter(rec.write))
	res, err := New(cfg, c, c, opts...).WithdrawInitiate(context.Background(), amount)
	if err != nil {
		t.Fatalf("WithdrawInitiate: %v", err)
	}

	if res.Kind != route.KindWithdrawInitiate {
		t.Errorf("Kind = %v, want KindWithdrawInitiate", res.Kind)
	}
	if res.WithdrawalPath == "" {
		t.Error("WithdrawalPath is empty")
	}
	if res.Withdrawal == nil {
		t.Fatal("Withdrawal was not returned")
	}
	if res.Withdrawal.Nonce.Int64() != 9 {
		t.Errorf("Nonce = %s, want 9", res.Withdrawal.Nonce)
	}
	if res.Withdrawal.L2BlockNumber.Int64() != 44917575 {
		t.Errorf("L2BlockNumber = %s", res.Withdrawal.L2BlockNumber)
	}

	// The record must be keyed by the transaction that was actually sent.
	sent := c.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d transactions, want 1", len(sent))
	}
	tx := sent[0]
	if rec.hash != tx.Hash() {
		t.Errorf("recorded under %s, want %s", rec.hash.Hex(), tx.Hash().Hex())
	}

	// The transaction must go to the L2 bridge, carry the value, and name the
	// ETH sentinel the deployed contract actually accepts.
	if got := tx.To(); got == nil || *got != common.HexToAddress(config.L2StandardBridgePredeploy) {
		t.Errorf("To = %v, want the L2 standard bridge", got)
	}
	if tx.Value().Cmp(amount) != 0 {
		t.Errorf("value = %s, want %s", tx.Value(), amount)
	}
	wantSelector := crypto.Keccak256([]byte("withdrawTo(address,address,uint256,uint32,bytes)"))[:4]
	if got := tx.Data(); len(got) < 4 || string(got[:4]) != string(wantSelector) {
		t.Errorf("selector = %x, want %x", got[:4], wantSelector)
	}
	if got := common.BytesToAddress(tx.Data()[4+12 : 4+32]); got != common.HexToAddress(config.LegacyERC20ETH) {
		t.Errorf("_l2Token = %s, want %s", got.Hex(), config.LegacyERC20ETH)
	}
	if got := common.BytesToAddress(tx.Data()[4+44 : 4+64]); got != cfg.DestAddr {
		t.Errorf("_to = %s, want %s", got.Hex(), cfg.DestAddr.Hex())
	}
}

func TestWithdrawInitiateRejectsNonPositiveAmount(t *testing.T) {
	for _, amount := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1)} {
		c := &fake.Client{}
		_, err := New(withdrawCfg(t), c, c, fastOpts()...).WithdrawInitiate(context.Background(), amount)
		if !errors.Is(err, ErrAmountNotPositive) {
			t.Errorf("amount %v: error = %v, want ErrAmountNotPositive", amount, err)
		}
		if len(c.Sent()) != 0 {
			t.Error("a withdrawal was broadcast for a non-positive amount")
		}
	}
}

func TestWithdrawInitiateFailures(t *testing.T) {
	amount := big.NewInt(1_000)

	tests := []struct {
		name     string
		script   func(*fake.Client)
		wantErr  error
		wantIn   string
		wantHash bool
	}{
		{
			name:    "L2 endpoint is the wrong chain",
			script:  func(c *fake.Client) { c.PushChainID(big.NewInt(11155111), nil) },
			wantErr: ErrChainMismatch,
			wantIn:  "endpoint reports 11155111",
		},
		{
			name: "nonce is unreadable",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(0, errBoom)
			},
			wantErr: errBoom,
			wantIn:  "read pending nonce",
		},
		{
			name: "broadcast is rejected",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
				c.PushGas(150_000, nil)
				c.PushSend(errBoom)
			},
			wantErr: errBoom,
			wantIn:  "broadcast withdrawal",
		},
		{
			name: "transaction reverts",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
				c.PushGas(150_000, nil)
				c.PushSend(nil)
				c.PushReceipt(&types.Receipt{Status: types.ReceiptStatusFailed}, nil)
			},
			wantErr:  ErrTxReverted,
			wantIn:   "reverted",
			wantHash: true,
		},
		{
			name: "receipt carries no MessagePassed log",
			script: func(c *fake.Client) {
				c.PushChainID(big.NewInt(84532), nil)
				c.PushNonce(1, nil)
				c.PushTipCap(big.NewInt(1), nil)
				c.PushHeader(&types.Header{BaseFee: big.NewInt(1)}, nil)
				c.PushGas(150_000, nil)
				c.PushSend(nil)
				c.PushReceipt(&types.Receipt{Status: types.ReceiptStatusSuccessful}, nil)
			},
			wantErr:  opstack.ErrNoMessagePassedLog,
			wantIn:   "withdrawal parameters",
			wantHash: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &fake.Client{}
			tc.script(c)

			res, err := New(withdrawCfg(t), c, c, fastOpts()...).
				WithdrawInitiate(context.Background(), amount)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want errors.Is %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
			if got := res.SrcTxHash != (common.Hash{}); got != tc.wantHash {
				t.Errorf("reported a hash = %v, want %v", got, tc.wantHash)
			}
		})
	}
}

// A withdrawal whose parameters could not be recorded is unprovable, so the
// write failing must fail the whole operation — loudly, and still reporting the
// hash and the parsed parameters so nothing is lost.
func TestWithdrawInitiateFailsWhenTheRecordCannotBeWritten(t *testing.T) {
	amount := big.NewInt(1_000)
	c := &fake.Client{}
	scriptWithdraw(c, amount)

	rec := &recorder{err: errBoom}
	opts := append(fastOpts(), WithWithdrawalWriter(rec.write))

	res, err := New(withdrawCfg(t), c, c, opts...).WithdrawInitiate(context.Background(), amount)
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want errBoom", err)
	}
	if !strings.Contains(err.Error(), "record withdrawal") {
		t.Errorf("error %q does not say the record failed", err)
	}
	if res.SrcTxHash == (common.Hash{}) {
		t.Error("the L2 hash was not reported")
	}
	if res.Withdrawal == nil {
		t.Error("the parsed withdrawal was not reported, so its parameters are lost")
	}
	if res.WithdrawalPath != "" {
		t.Errorf("WithdrawalPath = %q, want empty when nothing was written", res.WithdrawalPath)
	}
}

func TestWithdrawInitiateEncoderFailure(t *testing.T) {
	c := &fake.Client{}
	c.PushChainID(big.NewInt(84532), nil)

	failing := WithWithdrawEncoder(func(_, _ common.Address, _ *big.Int, _ uint32) ([]byte, error) {
		return nil, errBoom
	})
	_, err := New(withdrawCfg(t), c, c, append(fastOpts(), failing)...).
		WithdrawInitiate(context.Background(), big.NewInt(1))
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want errBoom", err)
	}
	if !strings.Contains(err.Error(), "encode withdrawal calldata") {
		t.Errorf("error = %v, want it to name the encoding failure", err)
	}
	if len(c.Sent()) != 0 {
		t.Error("a withdrawal was broadcast despite the encoding failing")
	}
}

func TestWithdrawOptions(t *testing.T) {
	addr := common.HexToAddress("0xbeef")
	b := New(withdrawCfg(t), &fake.Client{}, &fake.Client{},
		WithL2StandardBridge(addr),
		WithWithdrawMinGasLimit(7),
		WithWithdrawalsDir(t.TempDir()),
	)
	if b.l2Bridge != addr {
		t.Errorf("l2Bridge = %s, want %s", b.l2Bridge.Hex(), addr.Hex())
	}
	if b.withdrawMinGasLimit != 7 {
		t.Errorf("withdrawMinGasLimit = %d, want 7", b.withdrawMinGasLimit)
	}

	d := New(withdrawCfg(t), &fake.Client{}, &fake.Client{})
	if d.l2Bridge != common.HexToAddress(config.L2StandardBridgePredeploy) {
		t.Errorf("default l2Bridge = %s", d.l2Bridge.Hex())
	}
	if d.withdrawMinGasLimit != config.DefaultWithdrawMinGasLimit {
		t.Errorf("default withdrawMinGasLimit = %d", d.withdrawMinGasLimit)
	}
	if d.writeWithdrawal == nil || d.encodeWithdraw == nil {
		t.Error("New left the withdrawal writer or encoder nil")
	}
}

// WithWithdrawalsDir must actually write to the directory it names.
func TestWithWithdrawalsDirWritesThere(t *testing.T) {
	dir := t.TempDir()
	amount := big.NewInt(1_000)

	c := &fake.Client{}
	scriptWithdraw(c, amount)

	opts := append(fastOpts(), WithWithdrawalsDir(dir))
	res, err := New(withdrawCfg(t), c, c, opts...).WithdrawInitiate(context.Background(), amount)
	if err != nil {
		t.Fatalf("WithdrawInitiate: %v", err)
	}
	if !strings.HasPrefix(res.WithdrawalPath, dir) {
		t.Errorf("wrote to %q, want a path under %q", res.WithdrawalPath, dir)
	}
}
