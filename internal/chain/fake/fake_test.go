package fake

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/chain"
)

// The fake is only useful if it is substitutable for the real client.
var _ chain.Client = (*Client)(nil)

var errBoom = errors.New("boom")

func TestScriptedResultsArePoppedInOrder(t *testing.T) {
	ctx := context.Background()
	c := &Client{}
	c.PushChainID(big.NewInt(1), nil).PushChainID(big.NewInt(2), nil)

	for _, want := range []int64{1, 2} {
		got, err := c.ChainID(ctx)
		if err != nil {
			t.Fatalf("ChainID: %v", err)
		}
		if got.Int64() != want {
			t.Errorf("ChainID = %s, want %d", got, want)
		}
	}
}

func TestEveryMethodReturnsItsScriptedValue(t *testing.T) {
	ctx := context.Background()
	addr := common.HexToAddress("0x1")
	hash := common.HexToHash("0x2")

	c := &Client{}
	c.PushChainID(big.NewInt(84532), nil)
	c.PushNonce(7, nil)
	c.PushTipCap(big.NewInt(11), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(13)}, nil)
	c.PushGas(21000, nil)
	c.PushSend(nil)
	c.PushReceipt(&types.Receipt{Status: types.ReceiptStatusSuccessful}, nil)
	c.PushBalance(big.NewInt(17), nil)

	if got, err := c.ChainID(ctx); err != nil || got.Int64() != 84532 {
		t.Errorf("ChainID = %v, %v", got, err)
	}
	if got, err := c.PendingNonceAt(ctx, addr); err != nil || got != 7 {
		t.Errorf("PendingNonceAt = %v, %v", got, err)
	}
	if got, err := c.SuggestGasTipCap(ctx); err != nil || got.Int64() != 11 {
		t.Errorf("SuggestGasTipCap = %v, %v", got, err)
	}
	if got, err := c.HeaderByNumber(ctx, nil); err != nil || got.BaseFee.Int64() != 13 {
		t.Errorf("HeaderByNumber = %v, %v", got, err)
	}
	if got, err := c.EstimateGas(ctx, ethereum.CallMsg{From: addr}); err != nil || got != 21000 {
		t.Errorf("EstimateGas = %v, %v", got, err)
	}
	if err := c.SendTransaction(ctx, types.NewTx(&types.DynamicFeeTx{})); err != nil {
		t.Errorf("SendTransaction: %v", err)
	}
	if got, err := c.TransactionReceipt(ctx, hash); err != nil || got.Status != types.ReceiptStatusSuccessful {
		t.Errorf("TransactionReceipt = %v, %v", got, err)
	}
	if got, err := c.BalanceAt(ctx, addr, nil); err != nil || got.Int64() != 17 {
		t.Errorf("BalanceAt = %v, %v", got, err)
	}
}

func TestInjectedErrorsSurface(t *testing.T) {
	ctx := context.Background()
	addr := common.HexToAddress("0x1")

	c := &Client{}
	c.PushChainID(nil, errBoom)
	c.PushNonce(0, errBoom)
	c.PushTipCap(nil, errBoom)
	c.PushHeader(nil, errBoom)
	c.PushGas(0, errBoom)
	c.PushSend(errBoom)
	c.PushReceipt(nil, errBoom)
	c.PushBalance(nil, errBoom)

	calls := map[string]func() error{
		"ChainID":            func() error { _, err := c.ChainID(ctx); return err },
		"PendingNonceAt":     func() error { _, err := c.PendingNonceAt(ctx, addr); return err },
		"SuggestGasTipCap":   func() error { _, err := c.SuggestGasTipCap(ctx); return err },
		"HeaderByNumber":     func() error { _, err := c.HeaderByNumber(ctx, nil); return err },
		"EstimateGas":        func() error { _, err := c.EstimateGas(ctx, ethereum.CallMsg{}); return err },
		"SendTransaction":    func() error { return c.SendTransaction(ctx, types.NewTx(&types.DynamicFeeTx{})) },
		"TransactionReceipt": func() error { _, err := c.TransactionReceipt(ctx, common.Hash{}); return err },
		"BalanceAt":          func() error { _, err := c.BalanceAt(ctx, addr, nil); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, errBoom) {
			t.Errorf("%s error = %v, want errBoom", name, err)
		}
	}
}

// An under-scripted method must fail rather than hand back a zero value, or a
// test that forgot a Push would pass for the wrong reason.
func TestExhaustedQueueIsAnError(t *testing.T) {
	ctx := context.Background()
	c := &Client{}
	if _, err := c.ChainID(ctx); !errors.Is(err, ErrExhausted) {
		t.Errorf("ChainID on an empty queue = %v, want ErrExhausted", err)
	}
	if _, err := c.PendingNonceAt(ctx, common.Address{}); !errors.Is(err, ErrExhausted) {
		t.Errorf("PendingNonceAt on an empty queue = %v, want ErrExhausted", err)
	}
}

func TestRecordsSentAndEstimated(t *testing.T) {
	ctx := context.Background()
	c := &Client{}
	c.PushGas(1, nil)
	c.PushSend(errBoom)

	msg := ethereum.CallMsg{From: common.HexToAddress("0xabc"), Value: big.NewInt(5)}
	if _, err := c.EstimateGas(ctx, msg); err != nil {
		t.Fatalf("EstimateGas: %v", err)
	}
	tx := types.NewTx(&types.DynamicFeeTx{Nonce: 9})

	// The transaction is recorded even though the send fails, so a test can
	// assert on what would have gone out.
	if err := c.SendTransaction(ctx, tx); !errors.Is(err, errBoom) {
		t.Fatalf("SendTransaction error = %v, want errBoom", err)
	}

	sent := c.Sent()
	if len(sent) != 1 || sent[0].Nonce() != 9 {
		t.Errorf("Sent() = %v, want one transaction with nonce 9", sent)
	}
	est := c.Estimated()
	if len(est) != 1 || est[0].Value.Int64() != 5 {
		t.Errorf("Estimated() = %v, want one message with value 5", est)
	}
}

func TestClose(t *testing.T) {
	c := &Client{}
	if c.Closed() {
		t.Error("a fresh client reports closed")
	}
	c.Close()
	if !c.Closed() {
		t.Error("Closed() = false after Close()")
	}
}

// CodeAt and CallContract model chain state rather than a sequence, so they are
// keyed by what is being read and an unscripted read is a legitimate answer
// rather than a scripting mistake.
func TestCodeAtAndCallContractAreKeyedByAddress(t *testing.T) {
	ctx := context.Background()
	contract := common.HexToAddress("0xc0de")
	eoa := common.HexToAddress("0xea0a")
	sel := []byte{0xde, 0xad, 0xbe, 0xef}

	c := &Client{}
	c.SetCode(contract, []byte{0x60, 0x80})
	c.SetCall(contract, sel, []byte{0x01})

	code, err := c.CodeAt(ctx, contract, nil)
	if err != nil || len(code) != 2 {
		t.Errorf("CodeAt(contract) = %x, %v", code, err)
	}

	// An address nobody scripted holds no code, which is what a node says
	// about an account that is not a contract.
	code, err = c.CodeAt(ctx, eoa, big.NewInt(7))
	if err != nil || len(code) != 0 {
		t.Errorf("CodeAt(eoa) = %x, %v; want empty and no error", code, err)
	}

	ret, err := c.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: sel}, nil)
	if err != nil || len(ret) != 1 {
		t.Errorf("CallContract = %x, %v", ret, err)
	}

	// A selector the contract does not answer to reverts, as it would on a
	// real node.
	if _, err := c.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: []byte{0x00}}, nil); !errors.Is(err, ErrReverted) {
		t.Errorf("unscripted selector = %v, want ErrReverted", err)
	}
	if _, err := c.CallContract(ctx, ethereum.CallMsg{}, nil); !errors.Is(err, ErrReverted) {
		t.Errorf("call with no To = %v, want ErrReverted", err)
	}
}

func TestCodeAndCallFailuresSurface(t *testing.T) {
	ctx := context.Background()
	addr := common.HexToAddress("0xc0de")
	sel := []byte{0x01, 0x02, 0x03, 0x04}

	c := &Client{}
	c.FailCode(addr, errBoom)
	c.FailCall(addr, sel, errBoom)

	if _, err := c.CodeAt(ctx, addr, nil); !errors.Is(err, errBoom) {
		t.Errorf("CodeAt error = %v, want errBoom", err)
	}
	if _, err := c.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: sel}, nil); !errors.Is(err, errBoom) {
		t.Errorf("CallContract error = %v, want errBoom", err)
	}
}
