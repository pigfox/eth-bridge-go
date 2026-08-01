// Package fake is a scriptable chain.Client for tests.
//
// Every method is driven by its own queue of pre-loaded outcomes: each call
// pops the next one, so a test says what the node returns for the first call,
// the second call and so on, and can make any single call fail. A method whose
// queue runs dry returns ErrExhausted rather than a zero value, so a test that
// under-scripts a path fails loudly instead of passing on an accident.
package fake

import (
	"context"
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ErrExhausted is returned when a method is called more times than the test
// scripted it for.
var ErrExhausted = errors.New("fake: call queue exhausted")

// outcome is one scripted return: a value, an error, or both.
type outcome[T any] struct {
	val T
	err error
}

// queue is a FIFO of scripted outcomes for a single method.
type queue[T any] struct {
	items []outcome[T]
}

// push appends one scripted outcome.
func (q *queue[T]) push(val T, err error) {
	q.items = append(q.items, outcome[T]{val: val, err: err})
}

// pop removes and returns the next scripted outcome.
func (q *queue[T]) pop() (T, error) {
	var zero T
	if len(q.items) == 0 {
		return zero, ErrExhausted
	}
	next := q.items[0]
	q.items = q.items[1:]
	return next.val, next.err
}

// Client is a scriptable chain.Client. The zero value is usable; load it with
// the Push* methods before handing it to the code under test.
//
// It is safe for concurrent use, because the bridge's confirmation loops and
// the tests' assertions can touch it from different goroutines.
type Client struct {
	mu sync.Mutex

	chainID  queue[*big.Int]
	nonce    queue[uint64]
	tipCap   queue[*big.Int]
	header   queue[*types.Header]
	gas      queue[uint64]
	send     queue[struct{}]
	receipt  queue[*types.Receipt]
	balance  queue[*big.Int]
	sent     []*types.Transaction
	estimate []ethereum.CallMsg
	closed   bool
}

// PushChainID scripts one ChainID result.
func (c *Client) PushChainID(id *big.Int, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chainID.push(id, err)
	return c
}

// PushNonce scripts one PendingNonceAt result.
func (c *Client) PushNonce(n uint64, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nonce.push(n, err)
	return c
}

// PushTipCap scripts one SuggestGasTipCap result.
func (c *Client) PushTipCap(tip *big.Int, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tipCap.push(tip, err)
	return c
}

// PushHeader scripts one HeaderByNumber result.
func (c *Client) PushHeader(h *types.Header, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.header.push(h, err)
	return c
}

// PushGas scripts one EstimateGas result.
func (c *Client) PushGas(gas uint64, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gas.push(gas, err)
	return c
}

// PushSend scripts one SendTransaction result.
func (c *Client) PushSend(err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send.push(struct{}{}, err)
	return c
}

// PushReceipt scripts one TransactionReceipt result.
func (c *Client) PushReceipt(r *types.Receipt, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receipt.push(r, err)
	return c
}

// PushBalance scripts one BalanceAt result.
func (c *Client) PushBalance(bal *big.Int, err error) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balance.push(bal, err)
	return c
}

// ChainID implements chain.Client.
func (c *Client) ChainID(context.Context) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chainID.pop()
}

// PendingNonceAt implements chain.Client.
func (c *Client) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nonce.pop()
}

// SuggestGasTipCap implements chain.Client.
func (c *Client) SuggestGasTipCap(context.Context) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tipCap.pop()
}

// HeaderByNumber implements chain.Client.
func (c *Client) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.header.pop()
}

// EstimateGas implements chain.Client. The call message is recorded so a test
// can assert on what the bridge asked to estimate.
func (c *Client) EstimateGas(_ context.Context, msg ethereum.CallMsg) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.estimate = append(c.estimate, msg)
	return c.gas.pop()
}

// SendTransaction implements chain.Client. The transaction is recorded whether
// or not the scripted outcome is an error.
func (c *Client) SendTransaction(_ context.Context, tx *types.Transaction) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, tx)
	_, err := c.send.pop()
	return err
}

// TransactionReceipt implements chain.Client.
func (c *Client) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.receipt.pop()
}

// BalanceAt implements chain.Client.
func (c *Client) BalanceAt(context.Context, common.Address, *big.Int) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balance.pop()
}

// Close implements chain.Client.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// Closed reports whether Close has been called.
func (c *Client) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Sent returns the transactions passed to SendTransaction, in order.
func (c *Client) Sent() []*types.Transaction {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*types.Transaction(nil), c.sent...)
}

// Estimated returns the call messages passed to EstimateGas, in order.
func (c *Client) Estimated() []ethereum.CallMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ethereum.CallMsg(nil), c.estimate...)
}
