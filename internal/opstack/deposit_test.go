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
	l1Block  = common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	fromAddr = common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	toAddr   = common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
)

// depositLogOpts describes a synthetic TransactionDeposited event.
type depositLogOpts struct {
	mint       *big.Int
	value      *big.Int
	gas        uint64
	isCreation bool
	data       []byte
	logIndex   uint
}

// makeReceipt builds a receipt carrying one TransactionDeposited log, encoded
// the way the portal encodes it: the opaque payload is packed without padding,
// then wrapped as an ABI dynamic bytes argument.
func makeReceipt(o depositLogOpts) *types.Receipt {
	opaque := make([]byte, 0, opaqueDataMinLen+len(o.data))
	opaque = append(opaque, common.LeftPadBytes(o.mint.Bytes(), 32)...)
	opaque = append(opaque, common.LeftPadBytes(o.value.Bytes(), 32)...)
	opaque = append(opaque, common.LeftPadBytes(new(big.Int).SetUint64(o.gas).Bytes(), 8)...)
	if o.isCreation {
		opaque = append(opaque, 1)
	} else {
		opaque = append(opaque, 0)
	}
	opaque = append(opaque, o.data...)

	return &types.Receipt{Logs: []*types.Log{{
		Topics: []common.Hash{
			TransactionDepositedTopic,
			common.BytesToHash(fromAddr.Bytes()),
			common.BytesToHash(toAddr.Bytes()),
			common.BigToHash(big.NewInt(0)),
		},
		Data:      wrapBytes(opaque),
		BlockHash: l1Block,
		Index:     o.logIndex,
	}}}
}

// wrapBytes ABI-encodes a byte slice as the sole dynamic argument of an event.
func wrapBytes(b []byte) []byte {
	out := make([]byte, 0, 64+((len(b)+31)/32)*32)
	out = append(out, common.LeftPadBytes(big.NewInt(32).Bytes(), 32)...)
	out = append(out, common.LeftPadBytes(big.NewInt(int64(len(b))).Bytes(), 32)...)
	out = append(out, b...)
	if pad := (32 - len(b)%32) % 32; pad > 0 {
		out = append(out, make([]byte, pad)...)
	}
	return out
}

func TestDepositETHToCalldata(t *testing.T) {
	got, err := DepositETHToCalldata(toAddr, 200000)
	if err != nil {
		t.Fatalf("DepositETHToCalldata: %v", err)
	}

	// The selector is checked against the signature independently, rather than
	// against a value copied out of this package's own ABI constant.
	wantSelector := crypto.Keccak256([]byte("depositETHTo(address,uint32,bytes)"))[:4]
	if !bytes.Equal(got[:4], wantSelector) {
		t.Errorf("selector = %x, want %x", got[:4], wantSelector)
	}

	// address, uint32, offset, length — four words after the selector, with
	// the empty extraData contributing only its length.
	if len(got) != 4+32*4 {
		t.Fatalf("calldata is %d bytes, want %d", len(got), 4+32*4)
	}
	if gotTo := common.BytesToAddress(got[4+12 : 4+32]); gotTo != toAddr {
		t.Errorf("_to = %s, want %s", gotTo.Hex(), toAddr.Hex())
	}
	if gasArg := new(big.Int).SetBytes(got[4+32 : 4+64]).Uint64(); gasArg != 200000 {
		t.Errorf("_minGasLimit = %d, want 200000", gasArg)
	}
	if extraLen := new(big.Int).SetBytes(got[4+96 : 4+128]).Uint64(); extraLen != 0 {
		t.Errorf("_extraData length = %d, want 0", extraLen)
	}
}

func TestPackDepositETHToRejectsBadABI(t *testing.T) {
	_, err := packDepositETHTo(`{not json`, toAddr, 1)
	if err == nil {
		t.Fatal("packDepositETHTo accepted malformed ABI JSON")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("parse deposit ABI")) {
		t.Errorf("error = %v, want it to say the ABI failed to parse", err)
	}
}

func TestParseDeposit(t *testing.T) {
	rcpt := makeReceipt(depositLogOpts{
		mint:     big.NewInt(500_000_000_000_000),
		value:    big.NewInt(500_000_000_000_000),
		gas:      200000,
		data:     []byte{0xde, 0xad, 0xbe, 0xef},
		logIndex: 3,
	})

	dep, err := ParseDeposit(rcpt)
	if err != nil {
		t.Fatalf("ParseDeposit: %v", err)
	}
	if dep.From != fromAddr {
		t.Errorf("From = %s, want %s", dep.From.Hex(), fromAddr.Hex())
	}
	if dep.To == nil || *dep.To != toAddr {
		t.Errorf("To = %v, want %s", dep.To, toAddr.Hex())
	}
	if dep.Mint.Int64() != 500_000_000_000_000 || dep.Value.Int64() != 500_000_000_000_000 {
		t.Errorf("Mint/Value = %s/%s", dep.Mint, dep.Value)
	}
	if dep.Gas != 200000 {
		t.Errorf("Gas = %d, want 200000", dep.Gas)
	}
	if !bytes.Equal(dep.Data, []byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Errorf("Data = %x, want deadbeef", dep.Data)
	}
	if dep.SourceHash != userDepositSourceHash(l1Block, 3) {
		t.Errorf("SourceHash = %s", dep.SourceHash.Hex())
	}
}

func TestParseDepositContractCreationHasNoTo(t *testing.T) {
	rcpt := makeReceipt(depositLogOpts{
		mint: big.NewInt(1), value: big.NewInt(1), gas: 1, isCreation: true,
	})
	dep, err := ParseDeposit(rcpt)
	if err != nil {
		t.Fatalf("ParseDeposit: %v", err)
	}
	if dep.To != nil {
		t.Errorf("To = %s, want nil for a creation", dep.To.Hex())
	}
}

func TestParseDepositErrors(t *testing.T) {
	valid := makeReceipt(depositLogOpts{mint: big.NewInt(1), value: big.NewInt(1), gas: 1})

	tests := []struct {
		name    string
		rcpt    *types.Receipt
		wantErr error
	}{
		{
			name:    "no logs at all",
			rcpt:    &types.Receipt{},
			wantErr: ErrNoDepositLog,
		},
		{
			name: "logs, but none from the portal",
			rcpt: &types.Receipt{Logs: []*types.Log{
				{Topics: []common.Hash{common.HexToHash("0xfeed")}},
				{Topics: nil},
			}},
			wantErr: ErrNoDepositLog,
		},
		{
			name: "wrong topic count",
			rcpt: &types.Receipt{Logs: []*types.Log{{
				Topics: []common.Hash{TransactionDepositedTopic, {}},
				Data:   valid.Logs[0].Data,
			}}},
			wantErr: ErrBadDepositLog,
		},
		{
			name: "truncated opaque payload",
			rcpt: &types.Receipt{Logs: []*types.Log{{
				Topics: valid.Logs[0].Topics,
				Data:   wrapBytes([]byte{0x01, 0x02}),
			}}},
			wantErr: ErrBadDepositLog,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDeposit(tc.rcpt); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestUnwrapOpaqueData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"shorter than the ABI header", make([]byte, 63)},
		{
			// A declared length that no amount of data could satisfy.
			name: "declared length does not fit an int64",
			data: append(bytes.Repeat([]byte{0x00}, 32), bytes.Repeat([]byte{0xff}, 32)...),
		},
		{
			name: "declared length exceeds the bytes present",
			data: append(append(bytes.Repeat([]byte{0x00}, 32),
				common.LeftPadBytes(big.NewInt(500).Bytes(), 32)...), make([]byte, 32)...),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := unwrapOpaqueData(tc.data); !errors.Is(err, ErrBadDepositLog) {
				t.Fatalf("error = %v, want ErrBadDepositLog", err)
			}
		})
	}
}

func TestDepositHash(t *testing.T) {
	dep := Deposit{
		SourceHash: userDepositSourceHash(l1Block, 0),
		From:       fromAddr,
		To:         &toAddr,
		Mint:       big.NewInt(1000),
		Value:      big.NewInt(1000),
		Gas:        200000,
		Data:       []byte{0x01},
	}

	h1, err := dep.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h1 == (common.Hash{}) {
		t.Fatal("Hash returned the zero hash")
	}

	// Deterministic.
	h2, err := dep.Hash()
	if err != nil || h1 != h2 {
		t.Errorf("Hash is not deterministic: %s vs %s (%v)", h1.Hex(), h2.Hex(), err)
	}

	// A different source log must give a different L2 transaction.
	other := dep
	other.SourceHash = userDepositSourceHash(l1Block, 1)
	h3, err := other.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h3 == h1 {
		t.Error("two deposits from different logs hashed the same")
	}

	// A creation has no To, and must also hash differently.
	creation := dep
	creation.To = nil
	h4, err := creation.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if h4 == h1 {
		t.Error("a creation hashed the same as a call")
	}
}

func TestDepositHashRejectsUnencodableFields(t *testing.T) {
	dep := Deposit{
		SourceHash: common.Hash{},
		From:       fromAddr,
		To:         &toAddr,
		Mint:       big.NewInt(-1), // RLP has no representation for this.
		Value:      big.NewInt(0),
	}
	got, err := dep.Hash()
	if err == nil {
		t.Fatal("Hash accepted a negative mint")
	}
	if got != (common.Hash{}) {
		t.Errorf("Hash = %s alongside an error, want the zero hash", got.Hex())
	}
}

// The source hash must depend on both of its inputs, or two deposits in the
// same L1 block would collide.
func TestUserDepositSourceHashIsDistinctPerLog(t *testing.T) {
	a := userDepositSourceHash(l1Block, 0)
	b := userDepositSourceHash(l1Block, 1)
	c := userDepositSourceHash(common.HexToHash("0x22"), 0)

	if a == b {
		t.Error("log index does not affect the source hash")
	}
	if a == c {
		t.Error("block hash does not affect the source hash")
	}
	if a != userDepositSourceHash(l1Block, 0) {
		t.Error("source hash is not deterministic")
	}
}

func TestTransactionDepositedTopic(t *testing.T) {
	want := crypto.Keccak256Hash([]byte("TransactionDeposited(address,address,uint256,bytes)"))
	if TransactionDepositedTopic != want {
		t.Errorf("topic = %s, want %s", TransactionDepositedTopic.Hex(), want.Hex())
	}
}

func TestL2TxHash(t *testing.T) {
	rcpt := makeReceipt(depositLogOpts{
		mint: big.NewInt(7), value: big.NewInt(7), gas: 200000, logIndex: 2,
	})

	got, err := L2TxHash(rcpt)
	if err != nil {
		t.Fatalf("L2TxHash: %v", err)
	}

	// It must agree with going the long way round.
	dep, err := ParseDeposit(rcpt)
	if err != nil {
		t.Fatalf("ParseDeposit: %v", err)
	}
	want, err := dep.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if got != want {
		t.Errorf("L2TxHash = %s, want %s", got.Hex(), want.Hex())
	}
}

func TestL2TxHashPropagatesParseFailure(t *testing.T) {
	got, err := L2TxHash(&types.Receipt{})
	if !errors.Is(err, ErrNoDepositLog) {
		t.Fatalf("error = %v, want ErrNoDepositLog", err)
	}
	if got != (common.Hash{}) {
		t.Errorf("L2TxHash = %s alongside an error, want the zero hash", got.Hex())
	}
}
