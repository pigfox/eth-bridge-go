// Package chain is the only package in this module that knows about
// go-ethereum's RPC client.
//
// Everything above it talks to the Client interface below, which names exactly
// the eight calls the bridge makes and nothing else. Two things fall out of
// that. The bridge becomes testable without a node, because internal/chain/fake
// implements the same interface. And the surface that has to be kept in step
// with go-ethereum is one file rather than the whole tree.
//
// The interface is declared with go-ethereum's own signatures, so
// *ethclient.Client satisfies it as it stands and this package needs no
// forwarding methods to go stale.
package chain

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Client is the narrow view of an Ethereum node that this module uses.
type Client interface {
	// ChainID reports the chain the endpoint is actually serving, which is
	// checked against the configured ID before anything is signed.
	ChainID(ctx context.Context) (*big.Int, error)
	// PendingNonceAt returns the next nonce for an account.
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	// SuggestGasTipCap suggests a priority fee.
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)
	// HeaderByNumber returns a header; a nil number means the latest block.
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	// EstimateGas estimates the gas needed for a call.
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
	// SendTransaction broadcasts a signed transaction.
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	// TransactionReceipt returns the receipt for a hash, or
	// ethereum.NotFound while the transaction is still pending.
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	// BalanceAt returns an account balance; a nil block means the latest.
	BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
	// Close releases the underlying connection.
	Close()
}

// Dial connects to an Ethereum JSON-RPC endpoint.
//
// The returned Client is a live *ethclient.Client. HTTP endpoints are not
// contacted here — the first real request is what discovers an unreachable
// node — so an error from this function means the URL itself is unusable.
func Dial(ctx context.Context, rawurl string) (Client, error) {
	c, err := ethclient.DialContext(ctx, rawurl)
	if err != nil {
		return nil, err
	}
	return c, nil
}
