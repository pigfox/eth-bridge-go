package opstack

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	senderAddr = common.HexToAddress("0x4200000000000000000000000000000000000007")
	targetAddr = common.HexToAddress("0xC34855F4De64F1840e5686e64278da901e261f20")
	wdHash     = common.HexToHash("0xfeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface")
)

// word renders a big-endian 32-byte word.
func word(v int64) []byte { return common.LeftPadBytes(big.NewInt(v).Bytes(), 32) }

// messagePassedReceipt builds a receipt carrying one MessagePassed log.
func messagePassedReceipt(nonce int64, value, gasLimit int64, payload []byte) *types.Receipt {
	// value | gasLimit | offset to data | withdrawalHash | length | bytes
	data := make([]byte, 0, 160+len(payload))
	data = append(data, word(value)...)
	data = append(data, word(gasLimit)...)
	data = append(data, word(128)...)
	data = append(data, wdHash.Bytes()...)
	data = append(data, word(int64(len(payload)))...)
	data = append(data, payload...)
	if pad := (32 - len(payload)%32) % 32; pad > 0 {
		data = append(data, make([]byte, pad)...)
	}

	return &types.Receipt{Logs: []*types.Log{{
		Topics: []common.Hash{
			MessagePassedTopic,
			common.BigToHash(big.NewInt(nonce)),
			common.BytesToHash(senderAddr.Bytes()),
			common.BytesToHash(targetAddr.Bytes()),
		},
		Data:        data,
		BlockNumber: 44917575,
		TxHash:      common.HexToHash("0xabc"),
	}}}
}

func TestWithdrawToCalldata(t *testing.T) {
	token := common.HexToAddress("0xDeadDeAddeAddEAddeadDEaDDEAdDeaDDeAD0000")
	to := common.HexToAddress("0xb6c3a56CA2f99e3F5d7d16ad968df9f71cCC184D")
	amount := big.NewInt(10_000_000_000_000)

	got, err := WithdrawToCalldata(token, to, amount, 200000)
	if err != nil {
		t.Fatalf("WithdrawToCalldata: %v", err)
	}

	wantSelector := crypto.Keccak256([]byte("withdrawTo(address,address,uint256,uint32,bytes)"))[:4]
	if !bytes.Equal(got[:4], wantSelector) {
		t.Errorf("selector = %x, want %x", got[:4], wantSelector)
	}
	// five head words plus the empty extraData length word
	if len(got) != 4+32*6 {
		t.Fatalf("calldata is %d bytes, want %d", len(got), 4+32*6)
	}
	if v := common.BytesToAddress(got[4+12 : 4+32]); v != token {
		t.Errorf("_l2Token = %s, want %s", v.Hex(), token.Hex())
	}
	if v := common.BytesToAddress(got[4+44 : 4+64]); v != to {
		t.Errorf("_to = %s, want %s", v.Hex(), to.Hex())
	}
	if v := new(big.Int).SetBytes(got[4+64 : 4+96]); v.Cmp(amount) != 0 {
		t.Errorf("_amount = %s, want %s", v, amount)
	}
	if v := new(big.Int).SetBytes(got[4+96 : 4+128]).Uint64(); v != 200000 {
		t.Errorf("_minGasLimit = %d, want 200000", v)
	}
}

func TestPackWithdrawToRejectsBadABI(t *testing.T) {
	_, err := packWithdrawTo(`not json at all`, common.Address{}, common.Address{}, big.NewInt(1), 1)
	if err == nil {
		t.Fatal("packWithdrawTo accepted malformed ABI JSON")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("parse withdraw ABI")) {
		t.Errorf("error = %v, want it to say the ABI failed to parse", err)
	}
}

func TestMessagePassedTopic(t *testing.T) {
	want := crypto.Keccak256Hash([]byte("MessagePassed(uint256,address,address,uint256,uint256,bytes,bytes32)"))
	if MessagePassedTopic != want {
		t.Errorf("topic = %s, want %s", MessagePassedTopic.Hex(), want.Hex())
	}
}

func TestParseMessagePassed(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	rcpt := messagePassedReceipt(7, 500_000_000_000_000, 200000, payload)

	w, err := ParseMessagePassed(rcpt)
	if err != nil {
		t.Fatalf("ParseMessagePassed: %v", err)
	}
	if w.Nonce.Int64() != 7 {
		t.Errorf("Nonce = %s, want 7", w.Nonce)
	}
	if w.Sender != senderAddr || w.Target != targetAddr {
		t.Errorf("Sender/Target = %s/%s", w.Sender.Hex(), w.Target.Hex())
	}
	if w.Value.Int64() != 500_000_000_000_000 {
		t.Errorf("Value = %s", w.Value)
	}
	if w.GasLimit.Int64() != 200000 {
		t.Errorf("GasLimit = %s", w.GasLimit)
	}
	if !bytes.Equal(w.Data, payload) {
		t.Errorf("Data = %x, want %x", w.Data, payload)
	}
	if w.WithdrawalHash != wdHash {
		t.Errorf("WithdrawalHash = %s, want %s", w.WithdrawalHash.Hex(), wdHash.Hex())
	}
	if w.L2BlockNumber.Int64() != 44917575 {
		t.Errorf("L2BlockNumber = %s", w.L2BlockNumber)
	}
	if w.L2TxHash != common.HexToHash("0xabc") {
		t.Errorf("L2TxHash = %s", w.L2TxHash.Hex())
	}
}

func TestParseMessagePassedEmptyData(t *testing.T) {
	w, err := ParseMessagePassed(messagePassedReceipt(1, 1, 1, nil))
	if err != nil {
		t.Fatalf("ParseMessagePassed: %v", err)
	}
	if len(w.Data) != 0 {
		t.Errorf("Data = %x, want empty", w.Data)
	}
}

func TestParseMessagePassedErrors(t *testing.T) {
	valid := messagePassedReceipt(1, 1, 1, []byte{0xaa})

	tests := []struct {
		name    string
		rcpt    *types.Receipt
		wantErr error
	}{
		{"no logs", &types.Receipt{}, ErrNoMessagePassedLog},
		{
			name: "no MessagePassed log",
			rcpt: &types.Receipt{Logs: []*types.Log{
				{Topics: []common.Hash{common.HexToHash("0x1234")}},
			}},
			wantErr: ErrNoMessagePassedLog,
		},
		{
			name: "wrong topic count",
			rcpt: &types.Receipt{Logs: []*types.Log{{
				Topics: []common.Hash{MessagePassedTopic, {}, {}},
				Data:   valid.Logs[0].Data,
			}}},
			wantErr: ErrBadMessagePassedLog,
		},
		{
			name: "data section too short",
			rcpt: &types.Receipt{Logs: []*types.Log{{
				Topics: valid.Logs[0].Topics,
				Data:   make([]byte, 96),
			}}},
			wantErr: ErrBadMessagePassedLog,
		},
		{
			name: "data offset points past the payload",
			rcpt: &types.Receipt{Logs: []*types.Log{{
				Topics: valid.Logs[0].Topics,
				Data:   append(append(append(word(1), word(1)...), word(4096)...), wdHash.Bytes()...),
			}}},
			wantErr: ErrBadMessagePassedLog,
		},
		{
			name: "declared data length exceeds the payload",
			rcpt: &types.Receipt{Logs: []*types.Log{{
				Topics: valid.Logs[0].Topics,
				Data: append(append(append(append(append(
					word(1), word(1)...), word(128)...), wdHash.Bytes()...), word(9999)...),
					make([]byte, 32)...),
			}}},
			wantErr: ErrBadMessagePassedLog,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseMessagePassed(tc.rcpt); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// The offset and length words are attacker-influenced in the general case, so
// values that do not fit an int64 must be rejected rather than converted.
func TestDynamicBytesAtRejectsOversizedWords(t *testing.T) {
	huge := bytes.Repeat([]byte{0xff}, 32)

	if _, err := dynamicBytesAt(make([]byte, 160), huge); !errors.Is(err, ErrBadMessagePassedLog) {
		t.Errorf("oversized offset: error = %v, want ErrBadMessagePassedLog", err)
	}

	blob := append(word(0), huge...)
	if _, err := dynamicBytesAt(blob, word(32)); !errors.Is(err, ErrBadMessagePassedLog) {
		t.Errorf("oversized length: error = %v, want ErrBadMessagePassedLog", err)
	}
}
