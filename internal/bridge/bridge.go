// Package bridge performs the actual transfers.
//
// It talks only to chain.Client, never to an RPC library, so every path below
// — including the failure paths that a live testnet will not reproduce on
// demand — is exercised by unit tests against internal/chain/fake.
package bridge

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

// Errors returned by the bridge.
var (
	// ErrChainMismatch means the endpoint is serving a different chain than
	// the configuration claims. Signing against it would produce a
	// transaction for the wrong network.
	ErrChainMismatch = errors.New("endpoint chain ID does not match configuration")
	// ErrNoBaseFee means the chain's latest header has no base fee, so an
	// EIP-1559 transaction cannot be priced.
	ErrNoBaseFee = errors.New("latest header has no base fee; chain is not EIP-1559")
	// ErrAmountNotPositive means the amount to send was zero or negative.
	ErrAmountNotPositive = errors.New("amount must be greater than zero")
	// ErrReceiptTimeout means no receipt appeared inside the confirm timeout.
	ErrReceiptTimeout = errors.New("timed out waiting for transaction receipt")
	// ErrTxReverted means the transaction was mined with a failure status.
	ErrTxReverted = errors.New("transaction reverted")
)

// Result describes a completed bridge operation.
type Result struct {
	// Kind is the route that was taken.
	Kind route.Kind
	// SrcTxHash is the transaction hash on the source chain.
	SrcTxHash common.Hash
	// Amount is the value moved, in wei.
	Amount *big.Int
}

// Signer signs a transaction on behalf of the source account.
//
// The default is a local signature with the key from the configuration. It is
// an interface point rather than a hard-coded call so that a remote signer — a
// KMS or a hardware wallet, neither of which hands its key to the process —
// can be dropped in without touching the transaction-building code.
type Signer func(tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)

// LocalSigner signs with a private key held in this process.
func LocalSigner(key *ecdsa.PrivateKey) Signer {
	return func(tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
		return types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	}
}

// Option customises a Bridger.
type Option func(*Bridger)

// WithSigner replaces the local key signer.
func WithSigner(s Signer) Option {
	return func(b *Bridger) { b.sign = s }
}

// WithConfirmTimeout overrides how long the bridge waits for confirmation.
func WithConfirmTimeout(d time.Duration) Option {
	return func(b *Bridger) { b.confirmTimeout = d }
}

// WithPollInterval overrides the gap between confirmation polls.
func WithPollInterval(d time.Duration) Option {
	return func(b *Bridger) { b.pollInterval = d }
}

// WithSleeper overrides the wait between polls. Tests use it to run the
// confirmation loops without spending real time in them.
func WithSleeper(s func(context.Context, time.Duration) error) Option {
	return func(b *Bridger) { b.sleep = s }
}

// Bridger moves value between two configured chains.
type Bridger struct {
	cfg config.Config
	src chain.Client
	dst chain.Client

	confirmTimeout time.Duration
	pollInterval   time.Duration
	sleep          func(context.Context, time.Duration) error
	sign           Signer
}

// New builds a Bridger over the given source and destination clients.
//
// For a same-chain route src and dst are the same client.
func New(cfg config.Config, src, dst chain.Client, opts ...Option) *Bridger {
	b := &Bridger{
		cfg:            cfg,
		src:            src,
		dst:            dst,
		confirmTimeout: config.DefaultConfirmTimeout,
		pollInterval:   config.DefaultPollInterval,
		sleep:          Sleep,
		sign:           LocalSigner(cfg.SourceKey()),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Sleep waits for d, or returns early if the context is done.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SameChain sends amount wei from the configured source address to the
// configured destination address, on the source chain, as an EIP-1559
// transaction, and waits for a successful receipt.
func (b *Bridger) SameChain(ctx context.Context, amount *big.Int) (Result, error) {
	if amount == nil || amount.Sign() <= 0 {
		return Result{}, ErrAmountNotPositive
	}
	if err := b.verifyChain(ctx, b.src, b.cfg.SourceChainID); err != nil {
		return Result{}, err
	}

	tx, err := b.signedTransfer(ctx, b.cfg.DestAddr, amount, nil)
	if err != nil {
		return Result{}, err
	}
	if err := b.src.SendTransaction(ctx, tx); err != nil {
		return Result{}, fmt.Errorf("broadcast transaction: %w", err)
	}
	if _, err := b.WaitReceipt(ctx, b.src, tx.Hash()); err != nil {
		return Result{}, err
	}
	return Result{Kind: route.KindSameChain, SrcTxHash: tx.Hash(), Amount: new(big.Int).Set(amount)}, nil
}

// verifyChain checks that the endpoint is serving the chain the configuration
// names, before anything is signed against it.
func (b *Bridger) verifyChain(ctx context.Context, c chain.Client, want uint64) error {
	got, err := c.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("read chain ID: %w", err)
	}
	if !got.IsUint64() || got.Uint64() != want {
		return fmt.Errorf("%w: endpoint reports %s, configuration says %d", ErrChainMismatch, got, want)
	}
	return nil
}

// signedTransfer builds and signs an EIP-1559 transaction to `to` carrying
// `value` and the given calldata.
func (b *Bridger) signedTransfer(ctx context.Context, to common.Address, value *big.Int, data []byte) (*types.Transaction, error) {
	from := b.cfg.SourceAddr

	nonce, err := b.src.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("read pending nonce: %w", err)
	}
	tip, err := b.src.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggest gas tip cap: %w", err)
	}
	head, err := b.src.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("read latest header: %w", err)
	}
	if head.BaseFee == nil {
		return nil, ErrNoBaseFee
	}
	// Room for the base fee to double before the transaction is mined, which
	// is the most it can move across a single block.
	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))

	gas, err := b.src.EstimateGas(ctx, ethereum.CallMsg{
		From:      from,
		To:        &to,
		GasFeeCap: feeCap,
		GasTipCap: tip,
		Value:     value,
		Data:      data,
	})
	if err != nil {
		return nil, fmt.Errorf("estimate gas: %w", err)
	}

	chainID := new(big.Int).SetUint64(b.cfg.SourceChainID)
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       gas,
		To:        &to,
		Value:     value,
		Data:      data,
	})
	signed, err := b.sign(tx, chainID)
	if err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}
	return signed, nil
}

// WaitReceipt polls for the receipt of hash until it appears, the transaction
// is found to have reverted, or the confirm timeout expires.
func (b *Bridger) WaitReceipt(ctx context.Context, c chain.Client, hash common.Hash) (*types.Receipt, error) {
	deadline := time.Now().Add(b.confirmTimeout)
	for {
		rcpt, err := c.TransactionReceipt(ctx, hash)
		switch {
		case err == nil:
			if rcpt.Status != types.ReceiptStatusSuccessful {
				return rcpt, fmt.Errorf("%w: %s", ErrTxReverted, hash.Hex())
			}
			return rcpt, nil
		case errors.Is(err, ethereum.NotFound):
			// Still pending; fall through to the wait below.
		default:
			return nil, fmt.Errorf("read receipt %s: %w", hash.Hex(), err)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s after %s", ErrReceiptTimeout, hash.Hex(), b.confirmTimeout)
		}
		if err := b.sleep(ctx, b.pollInterval); err != nil {
			return nil, fmt.Errorf("waiting for receipt %s: %w", hash.Hex(), err)
		}
	}
}

// Deposit performs an L1 to L2 deposit. It is recognised by the router but not
// implemented in this version; see the roadmap in the README.
func (b *Bridger) Deposit(context.Context, *big.Int) (Result, error) {
	return Result{}, fmt.Errorf("%w: %s", route.ErrNotImplemented, route.KindDeposit)
}

// WithdrawInitiate starts an L2 to L1 withdrawal. It is recognised by the
// router but not implemented in this version; see the roadmap in the README.
func (b *Bridger) WithdrawInitiate(context.Context, *big.Int) (Result, error) {
	return Result{}, fmt.Errorf("%w: %s", route.ErrNotImplemented, route.KindWithdrawInitiate)
}
