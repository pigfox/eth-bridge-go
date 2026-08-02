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
	"github.com/pigfox/eth-bridge-go/internal/opstack"
	"github.com/pigfox/eth-bridge-go/internal/route"
	"github.com/pigfox/eth-bridge-go/internal/store"
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
	// ErrDepositNotCredited means the L1 deposit was mined but the destination
	// balance on L2 had not moved before the confirm timeout expired.
	ErrDepositNotCredited = errors.New("deposit not credited on the destination chain")
	// ErrNoBridgeAddress means a deposit was asked for without an L1 standard
	// bridge address. That address names one particular rollup and has no
	// sensible default, so the operation stops here rather than sending the
	// value to the zero address.
	ErrNoBridgeAddress = errors.New("no L1 standard bridge address; discovery must supply one")
)

// WithdrawalWriter records an initiated withdrawal so that it can be proved
// later. It is a plug point so that a caller can put the record somewhere other
// than the local filesystem.
type WithdrawalWriter func(txHash common.Hash, w opstack.Withdrawal) (string, error)

// Result describes a completed bridge operation.
type Result struct {
	// Kind is the route that was taken.
	Kind route.Kind
	// SrcTxHash is the transaction hash on the source chain.
	SrcTxHash common.Hash
	// Amount is the value moved, in wei.
	Amount *big.Int
	// DstTxHash is the transaction hash on the destination chain. For a
	// deposit this is derived from the L1 receipt rather than observed, since
	// the L2 transaction is produced by the chain itself.
	DstTxHash common.Hash
	// Credited is the increase actually observed at the destination address,
	// in wei. It is nil for routes that do not cross a domain.
	Credited *big.Int
	// Withdrawal is the parsed withdrawal, and WithdrawalPath is where its
	// parameters were recorded. Both are set only for a withdrawal.
	Withdrawal     *opstack.Withdrawal
	WithdrawalPath string
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

// DepositEncoder builds the calldata for a bridge deposit.
//
// Like Signer, this is a plug point: a different OP Stack chain, or a future
// version of the standard bridge with a different deposit signature, is a
// different encoder rather than a change to the transaction-building code.
type DepositEncoder func(to common.Address, minGasLimit uint32) ([]byte, error)

// WithdrawEncoder builds the calldata for a bridge withdrawal. It is the
// withdrawal-side counterpart to DepositEncoder.
type WithdrawEncoder func(l2Token, to common.Address, amount *big.Int, minGasLimit uint32) ([]byte, error)

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

// WithWithdrawEncoder replaces the standard-bridge withdrawal encoder.
func WithWithdrawEncoder(e WithdrawEncoder) Option {
	return func(b *Bridger) { b.encodeWithdraw = e }
}

// WithL2StandardBridge overrides the L2 Standard Bridge address.
func WithL2StandardBridge(addr common.Address) Option {
	return func(b *Bridger) { b.l2Bridge = addr }
}

// WithWithdrawMinGasLimit overrides the gas reserved for the L1 execution.
func WithWithdrawMinGasLimit(g uint32) Option {
	return func(b *Bridger) { b.withdrawMinGasLimit = g }
}

// WithWithdrawalWriter replaces where initiated withdrawals are recorded.
func WithWithdrawalWriter(w WithdrawalWriter) Option {
	return func(b *Bridger) { b.writeWithdrawal = w }
}

// WithWithdrawalsDir sets the directory initiated withdrawals are written to.
func WithWithdrawalsDir(dir string) Option {
	return func(b *Bridger) { b.writeWithdrawal = fileWriter(dir) }
}

// fileWriter records withdrawals as JSON files under dir.
func fileWriter(dir string) WithdrawalWriter {
	return func(h common.Hash, w opstack.Withdrawal) (string, error) {
		return store.SaveWithdrawal(dir, h, w)
	}
}

// WithGasMarginPercent overrides the margin added to every gas estimate.
func WithGasMarginPercent(pct uint64) Option {
	return func(b *Bridger) { b.gasMarginPercent = pct }
}

// WithDepositEncoder replaces the standard-bridge deposit encoder.
func WithDepositEncoder(e DepositEncoder) Option {
	return func(b *Bridger) { b.encodeDeposit = e }
}

// WithL1StandardBridge overrides the L1 Standard Bridge address.
func WithL1StandardBridge(addr common.Address) Option {
	return func(b *Bridger) { b.l1Bridge = addr }
}

// WithDepositMinGasLimit overrides the gas made available to the deposit on L2.
func WithDepositMinGasLimit(g uint32) Option {
	return func(b *Bridger) { b.depositMinGasLimit = g }
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

	l1Bridge           common.Address
	depositMinGasLimit uint32
	encodeDeposit      DepositEncoder
	gasMarginPercent   uint64

	l2Bridge            common.Address
	withdrawMinGasLimit uint32
	writeWithdrawal     WithdrawalWriter
	encodeWithdraw      WithdrawEncoder
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

		// There is no default L1 bridge address. It identifies one particular
		// rollup, so guessing it is exactly the mistake this tool stopped
		// making: it is discovered from the chains and passed in, and a Deposit
		// without one fails rather than sending ETH somewhere plausible.
		depositMinGasLimit: config.DefaultDepositMinGasLimit,
		encodeDeposit:      opstack.DepositETHToCalldata,
		gasMarginPercent:   config.DefaultGasMarginPercent,

		// The L2 side does have a default, because the predeploy address is
		// identical on every OP Stack chain by specification.
		l2Bridge:            opstack.L2StandardBridgePredeploy,
		withdrawMinGasLimit: config.DefaultWithdrawMinGasLimit,
		encodeWithdraw:      opstack.WithdrawToCalldata,
		writeWithdrawal:     fileWriter(config.DefaultWithdrawalsDir),
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
	gas += gas * b.gasMarginPercent / 100

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

// Deposit moves ETH from L1 to L2 through the Base Standard Bridge.
//
// It calls L1StandardBridge.depositETHTo with the amount as the transaction
// value, waits for the L1 receipt, derives the hash of the L2 transaction the
// deposit will produce, and then waits for the destination balance on L2 to
// actually move. The balance check is the one that matters: a successful L1
// receipt only means the deposit was accepted for relay, not that the ETH has
// arrived.
func (b *Bridger) Deposit(ctx context.Context, amount *big.Int) (Result, error) {
	if amount == nil || amount.Sign() <= 0 {
		return Result{}, ErrAmountNotPositive
	}
	if b.l1Bridge == (common.Address{}) {
		return Result{}, ErrNoBridgeAddress
	}
	if err := b.verifyChain(ctx, b.src, b.cfg.SourceChainID); err != nil {
		return Result{}, err
	}
	if err := b.verifyChain(ctx, b.dst, b.cfg.DestChainID); err != nil {
		return Result{}, err
	}

	// Read the destination balance before anything is sent, so the credit is
	// measured as a delta rather than guessed at from an absolute value.
	before, err := b.dst.BalanceAt(ctx, b.cfg.DestAddr, nil)
	if err != nil {
		return Result{}, fmt.Errorf("read destination balance: %w", err)
	}

	data, err := b.encodeDeposit(b.cfg.DestAddr, b.depositMinGasLimit)
	if err != nil {
		return Result{}, fmt.Errorf("encode deposit calldata: %w", err)
	}

	tx, err := b.signedTransfer(ctx, b.l1Bridge, amount, data)
	if err != nil {
		return Result{}, err
	}
	if err := b.src.SendTransaction(ctx, tx); err != nil {
		return Result{}, fmt.Errorf("broadcast deposit: %w", err)
	}

	res := Result{Kind: route.KindDeposit, SrcTxHash: tx.Hash(), Amount: new(big.Int).Set(amount)}

	rcpt, err := b.WaitReceipt(ctx, b.src, tx.Hash())
	if err != nil {
		return res, err
	}

	// From here on the L1 side has succeeded, so failures are returned
	// alongside the partial result: the caller needs the L1 hash to be able to
	// go and look at what happened.
	if res.DstTxHash, err = opstack.L2TxHash(rcpt); err != nil {
		return res, fmt.Errorf("derive L2 transaction from deposit receipt: %w", err)
	}

	credited, err := b.waitForCredit(ctx, before)
	if err != nil {
		return res, err
	}
	res.Credited = credited
	return res, nil
}

// waitForCredit polls the destination balance until it rises above before, and
// returns the increase.
func (b *Bridger) waitForCredit(ctx context.Context, before *big.Int) (*big.Int, error) {
	deadline := time.Now().Add(b.confirmTimeout)
	for {
		now, err := b.dst.BalanceAt(ctx, b.cfg.DestAddr, nil)
		if err != nil {
			return nil, fmt.Errorf("read destination balance: %w", err)
		}
		if now.Cmp(before) > 0 {
			return new(big.Int).Sub(now, before), nil
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s still holds %s wei after %s",
				ErrDepositNotCredited, b.cfg.DestAddr.Hex(), now, b.confirmTimeout)
		}
		if err := b.sleep(ctx, b.pollInterval); err != nil {
			return nil, fmt.Errorf("waiting for deposit credit: %w", err)
		}
	}
}

// WithdrawInitiate starts an L2 to L1 withdrawal and records what proving it
// later will need.
//
// This is the first of three transactions. It does not move ETH to L1: the
// withdrawal must be proved once an L2 output root covering its block is
// published, and finalized after the fault-proof window, neither of which this
// tool performs. The parameters are written to disk because they are not
// recoverable from the chain by this tool, and without them the withdrawal
// cannot be completed at all.
func (b *Bridger) WithdrawInitiate(ctx context.Context, amount *big.Int) (Result, error) {
	if amount == nil || amount.Sign() <= 0 {
		return Result{}, ErrAmountNotPositive
	}
	if err := b.verifyChain(ctx, b.src, b.cfg.SourceChainID); err != nil {
		return Result{}, err
	}

	data, err := b.encodeWithdraw(
		common.HexToAddress(config.LegacyERC20ETH),
		b.cfg.DestAddr, amount, b.withdrawMinGasLimit,
	)
	if err != nil {
		return Result{}, fmt.Errorf("encode withdrawal calldata: %w", err)
	}

	tx, err := b.signedTransfer(ctx, b.l2Bridge, amount, data)
	if err != nil {
		return Result{}, err
	}
	if err := b.src.SendTransaction(ctx, tx); err != nil {
		return Result{}, fmt.Errorf("broadcast withdrawal: %w", err)
	}

	res := Result{Kind: route.KindWithdrawInitiate, SrcTxHash: tx.Hash(), Amount: new(big.Int).Set(amount)}

	rcpt, err := b.WaitReceipt(ctx, b.src, tx.Hash())
	if err != nil {
		return res, err
	}

	wd, err := opstack.ParseMessagePassed(rcpt)
	if err != nil {
		return res, fmt.Errorf("read withdrawal parameters from receipt: %w", err)
	}
	res.Withdrawal = &wd

	// The record is written before success is reported. A withdrawal that was
	// initiated but whose parameters were lost is unprovable, so failing to
	// write is a failure of the whole operation, not a warning.
	path, err := b.writeWithdrawal(tx.Hash(), wd)
	if err != nil {
		return res, fmt.Errorf("record withdrawal: %w", err)
	}
	res.WithdrawalPath = path
	return res, nil
}
