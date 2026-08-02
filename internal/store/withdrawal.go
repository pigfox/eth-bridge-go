// Package store writes initiated withdrawals to disk.
//
// A withdrawal initiated on L2 cannot be finished for about a week, and the
// parameters needed to finish it are not recoverable from the chain by this
// tool. Writing them out is therefore not a convenience — it is the only reason
// initiating a withdrawal with this tool is not a way to lose the ETH.
package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"

	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

// ErrIncomplete means the withdrawal is missing a field that proving it later
// would need. Writing a file that cannot be used is worse than refusing to.
var ErrIncomplete = errors.New("withdrawal is missing fields required to prove it")

// document is the on-disk shape.
//
// Every 256-bit quantity is a decimal string. JSON numbers are IEEE-754 doubles
// in most readers, which silently mangles anything above 2^53 — a nonce and a
// wei value are both routinely above it.
type document struct {
	Nonce          string `json:"nonce"`
	Sender         string `json:"sender"`
	Target         string `json:"target"`
	Value          string `json:"value"`
	GasLimit       string `json:"gasLimit"`
	Data           string `json:"data"`
	WithdrawalHash string `json:"withdrawalHash"`
	L2BlockNumber  string `json:"l2BlockNumber"`
	L2TxHash       string `json:"l2TxHash"`
	Note           string `json:"note"`
}

// note travels with the file, because the file will be read by someone a week
// later who no longer remembers what it was for.
const note = "Initiated L2->L1 withdrawal. Proving and finalizing are NOT performed " +
	"by eth-bridge-go: prove after the L2 output root covering l2BlockNumber is " +
	"published, then finalize after the fault-proof window (~7 days on the OP Stack testnets)."

// encode validates a withdrawal and renders its on-disk document.
func encode(w opstack.Withdrawal) ([]byte, error) {
	if w.Nonce == nil || w.Value == nil || w.GasLimit == nil || w.L2BlockNumber == nil {
		return nil, fmt.Errorf("%w: nonce, value, gasLimit and l2BlockNumber are all required", ErrIncomplete)
	}
	return json.MarshalIndent(document{
		Nonce:          w.Nonce.String(),
		Sender:         w.Sender.Hex(),
		Target:         w.Target.Hex(),
		Value:          w.Value.String(),
		GasLimit:       w.GasLimit.String(),
		Data:           "0x" + hex.EncodeToString(w.Data),
		WithdrawalHash: w.WithdrawalHash.Hex(),
		L2BlockNumber:  w.L2BlockNumber.String(),
		L2TxHash:       w.L2TxHash.Hex(),
		Note:           note,
	}, "", "  ")
}

// SaveWithdrawal writes a withdrawal to dir/<l2TxHash>.json and returns the
// path it wrote.
func SaveWithdrawal(dir string, txHash common.Hash, w opstack.Withdrawal) (string, error) {
	data, err := encode(w)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create withdrawal directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, txHash.Hex()+".json")
	if err := os.WriteFile(path, data, withdrawalFilePerm); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
