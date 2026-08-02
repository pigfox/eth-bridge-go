// Package registry is a small, vendored snapshot of the Superchain Registry.
//
// It exists for two jobs, and is deliberately bad at being anything more. It
// supplies bridge addresses when discovery cannot reach a chain, and it turns a
// chain ID into a block-explorer link. It is never consulted first: asking the
// chain is always better than believing a file, because the file can go stale
// and the chain cannot.
//
// Every address in the snapshot was derived by running this tool's discovery
// against the live chain rather than copied from documentation, so a row is a
// cached answer to the question discovery asks rather than a second opinion.
package registry

import (
	_ "embed"
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"

	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

//go:embed registry.json
var snapshot []byte

// Chain is one row of the snapshot.
type Chain struct {
	// ChainID identifies the chain.
	ChainID uint64 `json:"chainId"`
	// Name is for humans.
	Name string `json:"name"`
	// L1ChainID is the settlement layer, and is zero for a row that is itself
	// an L1. The same rollup ID against a different L1 is a different
	// deployment, so a lookup that ignored this could return addresses for a
	// chain the caller is not talking to.
	L1ChainID uint64 `json:"l1ChainId"`
	// L1StandardBridge and OptimismPortal are the addresses discovery would
	// have derived. They are empty for an L1 row.
	L1StandardBridge string `json:"l1StandardBridge"`
	OptimismPortal   string `json:"optimismPortal"`
	// ExplorerTx is a URL prefix a transaction hash is appended to. It is
	// empty for a chain whose explorer could not be confirmed, and callers
	// print the bare hash in that case.
	ExplorerTx string `json:"explorerTx"`
}

// file is the shape of the embedded document.
type file struct {
	Chains []Chain `json:"chains"`
}

// index is the snapshot, keyed by chain ID.
var index = build(snapshot)

// build parses the snapshot into a lookup table.
//
// A snapshot that does not parse yields an empty table rather than a panic in
// an initialiser. This package is a fallback; one that takes the process down
// on the way past is worse than one that simply has nothing to offer.
func build(data []byte) map[uint64]Chain {
	var doc file
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := make(map[uint64]Chain, len(doc.Chains))
	for _, c := range doc.Chains {
		out[c.ChainID] = c
	}
	return out
}

// Lookup returns the snapshot row for a chain.
func Lookup(chainID uint64) (Chain, bool) {
	c, ok := index[chainID]
	return c, ok
}

// Addresses returns the bridge contracts for a pair, if the snapshot holds
// them and agrees that the two chains are paired.
//
// The L1 chain ID has to match. A snapshot row for a rollup that settles
// somewhere else is not an answer to the question that was asked, and
// returning it would hand back addresses for contracts that are not on the
// chain the caller is about to send to.
func Addresses(l1ChainID, l2ChainID uint64) (opstack.Addresses, bool) {
	c, ok := index[l2ChainID]
	if !ok || c.L1ChainID != l1ChainID {
		return opstack.Addresses{}, false
	}
	if !common.IsHexAddress(c.L1StandardBridge) || !common.IsHexAddress(c.OptimismPortal) {
		return opstack.Addresses{}, false
	}
	return opstack.Addresses{
		L1StandardBridge: common.HexToAddress(c.L1StandardBridge),
		L2StandardBridge: opstack.L2StandardBridgePredeploy,
		OptimismPortal:   common.HexToAddress(c.OptimismPortal),
	}, true
}

// ExplorerTx returns a block-explorer URL for a transaction, or the bare hash
// when the chain is not in the snapshot or has no confirmed explorer.
//
// A wrong link is worse than no link: it sends someone to a page about a
// different network and tells them their transaction does not exist.
func ExplorerTx(chainID uint64, hash string) string {
	c, ok := index[chainID]
	if !ok || c.ExplorerTx == "" {
		return hash
	}
	return c.ExplorerTx + hash
}
