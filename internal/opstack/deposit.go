// Package opstack encodes the two pieces of the OP Stack deposit flow this
// tool needs: the calldata for L1StandardBridge.depositETHTo, and the
// derivation of the L2 transaction hash that a deposit produces.
//
// Both are hand-written against the specification rather than pulled in with
// abigen or the Optimism SDK. The ABI here describes exactly one method, and
// the hash derivation is about forty lines; taking a code generator and a large
// dependency tree for that would be a worse trade than owning it.
package opstack

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// depositETHToABI is the minimal ABI for the one method this tool calls.
//
//	function depositETHTo(address _to, uint32 _minGasLimit, bytes _extraData) payable
const depositETHToABI = `[{
  "type": "function",
  "name": "depositETHTo",
  "stateMutability": "payable",
  "inputs": [
    {"name": "_to",          "type": "address"},
    {"name": "_minGasLimit", "type": "uint32"},
    {"name": "_extraData",   "type": "bytes"}
  ],
  "outputs": []
}]`

// DepositETHToCalldata builds the calldata for depositETHTo.
//
// extraData is emitted in the bridge's event and is not executed; this tool
// always passes it empty, which is what "0x" means at the call site.
func DepositETHToCalldata(to common.Address, minGasLimit uint32) ([]byte, error) {
	return packDepositETHTo(depositETHToABI, to, minGasLimit)
}

// packDepositETHTo takes the ABI source as a parameter rather than reaching for
// the constant, so that the parse-failure path is reachable from a test instead
// of being a panic in a package initialiser that nothing can exercise.
func packDepositETHTo(abiJSON string, to common.Address, minGasLimit uint32) ([]byte, error) {
	parsed, err := parseABI(abiJSON)
	if err != nil {
		return nil, fmt.Errorf("parse deposit ABI: %w", err)
	}
	return parsed.Pack("depositETHTo", to, minGasLimit, []byte{})
}

// parseABI parses one of this package's embedded ABI fragments.
func parseABI(abiJSON string) (abi.ABI, error) {
	return abi.JSON(strings.NewReader(abiJSON))
}

// TransactionDepositedTopic is the topic0 of
// TransactionDeposited(address,address,uint256,bytes), emitted by the
// OptimismPortal for every deposit.
var TransactionDepositedTopic = crypto.Keccak256Hash(
	[]byte("TransactionDeposited(address,address,uint256,bytes)"),
)

// Errors returned when a receipt does not look like a deposit.
var (
	// ErrNoDepositLog means the L1 receipt carried no TransactionDeposited event.
	ErrNoDepositLog = errors.New("no TransactionDeposited log in receipt")
	// ErrBadDepositLog means the event was present but malformed.
	ErrBadDepositLog = errors.New("malformed TransactionDeposited log")
)

// opaqueDataMinLen is the fixed part of opaqueData: a 32-byte mint, a 32-byte
// value, an 8-byte gas limit and a 1-byte isCreation flag, packed without
// padding by the portal.
const opaqueDataMinLen = 32 + 32 + 8 + 1

// depositTxType is the EIP-2718 type byte for an OP Stack deposit transaction.
const depositTxType = 0x7E

// Deposit is the L2 transaction that an L1 deposit will produce.
type Deposit struct {
	// SourceHash ties the L2 transaction to the L1 log that caused it.
	SourceHash common.Hash
	// From is the sender on L2. For a bridged deposit this is the aliased L1
	// cross-domain messenger, not the operator's own address.
	From common.Address
	// To is the L2 recipient, or nil for a contract creation.
	To *common.Address
	// Mint is the ETH minted on L2, and Value is the ETH transferred by the
	// L2 call.
	Mint  *big.Int
	Value *big.Int
	// Gas is the L2 gas limit.
	Gas uint64
	// Data is the L2 calldata.
	Data []byte
}

// depositRLP is the RLP payload of a deposit transaction, in the field order
// the OP Stack specifies.
type depositRLP struct {
	SourceHash          common.Hash
	From                common.Address
	To                  *common.Address `rlp:"nil"`
	Mint                *big.Int
	Value               *big.Int
	Gas                 uint64
	IsSystemTransaction bool
	Data                []byte
}

// Hash returns the L2 transaction hash of the deposit.
func (d Deposit) Hash() (common.Hash, error) {
	payload, err := rlp.EncodeToBytes(depositRLP{
		SourceHash: d.SourceHash,
		From:       d.From,
		To:         d.To,
		Mint:       d.Mint,
		Value:      d.Value,
		Gas:        d.Gas,
		// User deposits are never system transactions. The field is retained
		// in the encoding for historical reasons and is always false.
		IsSystemTransaction: false,
		Data:                d.Data,
	})
	if err != nil {
		return common.Hash{}, fmt.Errorf("rlp encode deposit: %w", err)
	}
	return crypto.Keccak256Hash(append([]byte{depositTxType}, payload...)), nil
}

// L2TxHash returns the hash of the L2 transaction that an L1 deposit receipt
// will produce.
//
// The two steps are joined here rather than at the call site so that a caller
// has one error to handle instead of two, and so that Hash's own failure mode —
// a field RLP cannot represent, which ParseDeposit never produces — does not
// become an unreachable branch in every caller.
func L2TxHash(rcpt *types.Receipt) (common.Hash, error) {
	dep, err := ParseDeposit(rcpt)
	if err != nil {
		return common.Hash{}, err
	}
	return dep.Hash()
}

// ParseDeposit finds the TransactionDeposited log in an L1 receipt and decodes
// the L2 transaction it will produce.
//
// The source hash is
//
//	keccak256(uint256(0) ++ keccak256(l1BlockHash ++ uint256(logIndex)))
//
// where the leading zero is the "user deposit" source domain.
func ParseDeposit(rcpt *types.Receipt) (Deposit, error) {
	entry := findLog(rcpt, TransactionDepositedTopic)
	if entry == nil {
		return Deposit{}, ErrNoDepositLog
	}
	// topics: [signature, from, to, version]
	if len(entry.Topics) != depositLogTopics {
		return Deposit{}, fmt.Errorf("%w: %d topics, want %d", ErrBadDepositLog, len(entry.Topics), depositLogTopics)
	}

	opaque, err := unwrapOpaqueData(entry.Data)
	if err != nil {
		return Deposit{}, err
	}

	d := Deposit{
		SourceHash: userDepositSourceHash(entry.BlockHash, uint64(entry.Index)),
		From:       common.BytesToAddress(entry.Topics[1].Bytes()),
		Mint:       new(big.Int).SetBytes(opaque[0:32]),
		Value:      new(big.Int).SetBytes(opaque[32:64]),
		Gas:        new(big.Int).SetBytes(opaque[64:72]).Uint64(),
		Data:       opaque[opaqueDataMinLen:],
	}
	if isCreation := opaque[72]; isCreation == 0 {
		to := common.BytesToAddress(entry.Topics[2].Bytes())
		d.To = &to
	}
	return d, nil
}

// findLog returns the first log in the receipt with the given topic0.
func findLog(rcpt *types.Receipt, topic common.Hash) *types.Log {
	for _, l := range rcpt.Logs {
		if len(l.Topics) > 0 && l.Topics[0] == topic {
			return l
		}
	}
	return nil
}

// unwrapOpaqueData strips the ABI dynamic-bytes header from the log data.
//
// opaqueData is the event's only non-indexed argument, so the data section is
// a 32-byte offset, a 32-byte length, and then the bytes themselves padded up
// to a multiple of 32.
func unwrapOpaqueData(data []byte) ([]byte, error) {
	const headerLen = 64
	if len(data) < headerLen {
		return nil, fmt.Errorf("%w: %d bytes of log data, want at least %d",
			ErrBadDepositLog, len(data), headerLen)
	}
	// The comparison is done in int64 and the slice is indexed with the int64
	// directly. Widening the available length is always safe, whereas
	// narrowing the declared length to an int before it has been bounds-checked
	// is exactly the overflow this check exists to prevent.
	avail := int64(len(data) - headerLen)
	declared := new(big.Int).SetBytes(data[32:64])
	if !declared.IsInt64() || declared.Int64() > avail {
		return nil, fmt.Errorf("%w: declared opaque length %s exceeds the %d bytes present",
			ErrBadDepositLog, declared, avail)
	}
	opaque := data[headerLen:][:declared.Int64()]
	if len(opaque) < opaqueDataMinLen {
		return nil, fmt.Errorf("%w: opaque data is %d bytes, want at least %d",
			ErrBadDepositLog, len(opaque), opaqueDataMinLen)
	}
	return opaque, nil
}

// userDepositSourceHash derives the source hash for a user deposit.
func userDepositSourceHash(l1BlockHash common.Hash, logIndex uint64) common.Hash {
	var inner [64]byte
	copy(inner[:32], l1BlockHash.Bytes())
	new(big.Int).SetUint64(logIndex).FillBytes(inner[32:])

	var outer [64]byte
	// outer[:32] stays zero: the source domain for a user deposit.
	copy(outer[32:], crypto.Keccak256(inner[:]))

	return crypto.Keccak256Hash(outer[:])
}
