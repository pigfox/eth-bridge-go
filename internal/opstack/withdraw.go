package opstack

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// withdrawToABI is the minimal ABI for the one withdrawal method this tool
// calls.
//
//	function withdrawTo(address _l2Token, address _to, uint256 _amount,
//	                    uint32 _minGasLimit, bytes _extraData) payable
const withdrawToABI = `[{
  "type": "function",
  "name": "withdrawTo",
  "stateMutability": "payable",
  "inputs": [
    {"name": "_l2Token",     "type": "address"},
    {"name": "_to",          "type": "address"},
    {"name": "_amount",      "type": "uint256"},
    {"name": "_minGasLimit", "type": "uint32"},
    {"name": "_extraData",   "type": "bytes"}
  ],
  "outputs": []
}]`

// WithdrawToCalldata builds the calldata for withdrawTo.
func WithdrawToCalldata(l2Token, to common.Address, amount *big.Int, minGasLimit uint32) ([]byte, error) {
	return packWithdrawTo(withdrawToABI, l2Token, to, amount, minGasLimit)
}

// packWithdrawTo takes the ABI source as a parameter for the same reason
// packDepositETHTo does: it keeps the parse-failure path reachable.
func packWithdrawTo(abiJSON string, l2Token, to common.Address, amount *big.Int, minGasLimit uint32) ([]byte, error) {
	parsed, err := parseABI(abiJSON)
	if err != nil {
		return nil, fmt.Errorf("parse withdraw ABI: %w", err)
	}
	return parsed.Pack("withdrawTo", l2Token, to, amount, minGasLimit, []byte{})
}

// MessagePassedTopic is the topic0 of the L2ToL1MessagePasser's
// MessagePassed(uint256,address,address,uint256,uint256,bytes,bytes32) event.
var MessagePassedTopic = crypto.Keccak256Hash(
	[]byte("MessagePassed(uint256,address,address,uint256,uint256,bytes,bytes32)"),
)

// ErrNoMessagePassedLog means the L2 receipt carried no MessagePassed event, so
// no withdrawal was actually initiated.
var ErrNoMessagePassedLog = errors.New("no MessagePassed log in receipt")

// ErrBadMessagePassedLog means the event was present but malformed.
var ErrBadMessagePassedLog = errors.New("malformed MessagePassed log")

// Withdrawal is everything needed to later prove and finalize a withdrawal on
// L1. It is what gets written to disk.
type Withdrawal struct {
	// Nonce is the message nonce assigned by the L2ToL1MessagePasser.
	Nonce *big.Int
	// Sender is the L2 contract that passed the message, and Target is the L1
	// contract that will receive it.
	Sender common.Address
	Target common.Address
	// Value is the ETH carried, and GasLimit the gas reserved for the L1 call.
	Value    *big.Int
	GasLimit *big.Int
	// Data is the calldata for the L1 call.
	Data []byte
	// WithdrawalHash identifies the withdrawal in the L2 output root.
	WithdrawalHash common.Hash
	// L2BlockNumber is the block the withdrawal was included in. Proving needs
	// an output root published at or after it.
	L2BlockNumber *big.Int
	// L2TxHash is the transaction that initiated the withdrawal.
	L2TxHash common.Hash
}

// messagePassedFixedLen is the fixed part of the event's data section: value,
// gasLimit, the offset of the dynamic data, and the withdrawal hash.
const messagePassedFixedLen = 32 * 4

// ParseMessagePassed finds the MessagePassed log in an L2 receipt and decodes
// the withdrawal it describes.
func ParseMessagePassed(rcpt *types.Receipt) (Withdrawal, error) {
	entry := findLog(rcpt, MessagePassedTopic)
	if entry == nil {
		return Withdrawal{}, ErrNoMessagePassedLog
	}
	// topics: [signature, nonce, sender, target]
	if len(entry.Topics) != 4 {
		return Withdrawal{}, fmt.Errorf("%w: %d topics, want 4", ErrBadMessagePassedLog, len(entry.Topics))
	}
	if len(entry.Data) < messagePassedFixedLen {
		return Withdrawal{}, fmt.Errorf("%w: %d bytes of data, want at least %d",
			ErrBadMessagePassedLog, len(entry.Data), messagePassedFixedLen)
	}

	data, err := dynamicBytesAt(entry.Data, entry.Data[64:96])
	if err != nil {
		return Withdrawal{}, err
	}

	return Withdrawal{
		Nonce:          new(big.Int).SetBytes(entry.Topics[1].Bytes()),
		Sender:         common.BytesToAddress(entry.Topics[2].Bytes()),
		Target:         common.BytesToAddress(entry.Topics[3].Bytes()),
		Value:          new(big.Int).SetBytes(entry.Data[0:32]),
		GasLimit:       new(big.Int).SetBytes(entry.Data[32:64]),
		Data:           data,
		WithdrawalHash: common.BytesToHash(entry.Data[96:128]),
		L2BlockNumber:  new(big.Int).SetUint64(entry.BlockNumber),
		L2TxHash:       entry.TxHash,
	}, nil
}

// dynamicBytesAt reads a dynamic bytes argument whose offset, relative to the
// start of blob, is encoded in offsetWord.
func dynamicBytesAt(blob, offsetWord []byte) ([]byte, error) {
	total := int64(len(blob))

	offset := new(big.Int).SetBytes(offsetWord)
	// The offset must leave room for the length word that follows it.
	if !offset.IsInt64() || offset.Int64() > total-32 {
		return nil, fmt.Errorf("%w: data offset %s is outside the %d-byte payload",
			ErrBadMessagePassedLog, offset, total)
	}
	at := offset.Int64()

	length := new(big.Int).SetBytes(blob[at : at+32])
	if !length.IsInt64() || length.Int64() > total-at-32 {
		return nil, fmt.Errorf("%w: declared data length %s exceeds the %d bytes present",
			ErrBadMessagePassedLog, length, total-at-32)
	}
	return blob[at+32:][:length.Int64()], nil
}
