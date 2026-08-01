package store

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/pigfox/eth-bridge-go/internal/opstack"
)

var txHash = common.HexToHash("0xabc123")

// sample is a complete withdrawal, with values chosen above 2^53 so that a
// reader which decoded them as JSON numbers would visibly corrupt them.
func sample() opstack.Withdrawal {
	nonce, _ := new(big.Int).SetString("1766847064778384329583297500742918515827483896875618958121606201292619776", 10)
	return opstack.Withdrawal{
		Nonce:          nonce,
		Sender:         common.HexToAddress("0x4200000000000000000000000000000000000007"),
		Target:         common.HexToAddress("0xC34855F4De64F1840e5686e64278da901e261f20"),
		Value:          big.NewInt(500_000_000_000_000),
		GasLimit:       big.NewInt(200000),
		Data:           []byte{0xde, 0xad},
		WithdrawalHash: common.HexToHash("0xfeed"),
		L2BlockNumber:  big.NewInt(44917575),
		L2TxHash:       txHash,
	}
}

func TestSaveWithdrawal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "withdrawals")

	path, err := SaveWithdrawal(dir, txHash, sample())
	if err != nil {
		t.Fatalf("SaveWithdrawal: %v", err)
	}
	if want := filepath.Join(dir, txHash.Hex()+".json"); path != want {
		t.Errorf("path = %s, want %s", path, want)
	}

	// The file must be private: it describes an unclaimed withdrawal.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var got document
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	w := sample()
	checks := []struct{ field, got, want string }{
		{"nonce", got.Nonce, w.Nonce.String()},
		{"sender", got.Sender, w.Sender.Hex()},
		{"target", got.Target, w.Target.Hex()},
		{"value", got.Value, "500000000000000"},
		{"gasLimit", got.GasLimit, "200000"},
		{"data", got.Data, "0xdead"},
		{"withdrawalHash", got.WithdrawalHash, w.WithdrawalHash.Hex()},
		{"l2BlockNumber", got.L2BlockNumber, "44917575"},
		{"l2TxHash", got.L2TxHash, txHash.Hex()},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if !strings.Contains(got.Note, "NOT performed") {
		t.Errorf("note = %q, want it to say prove/finalize are not performed", got.Note)
	}

	// Large integers must survive as strings, not as JSON numbers.
	if !strings.Contains(string(raw), `"nonce": "`+w.Nonce.String()+`"`) {
		t.Error("the nonce was not written as a decimal string")
	}
}

func TestSaveWithdrawalRejectsIncompleteRecords(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name   string
		mutate func(*opstack.Withdrawal)
	}{
		{"no nonce", func(w *opstack.Withdrawal) { w.Nonce = nil }},
		{"no value", func(w *opstack.Withdrawal) { w.Value = nil }},
		{"no gas limit", func(w *opstack.Withdrawal) { w.GasLimit = nil }},
		{"no L2 block", func(w *opstack.Withdrawal) { w.L2BlockNumber = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := sample()
			tc.mutate(&w)

			path, err := SaveWithdrawal(dir, txHash, w)
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("error = %v, want ErrIncomplete", err)
			}
			if path != "" {
				t.Errorf("path = %q, want empty", path)
			}
			// A file that cannot be used must not be left behind.
			if _, err := os.Stat(filepath.Join(dir, txHash.Hex()+".json")); !os.IsNotExist(err) {
				t.Error("an unusable record was written anyway")
			}
		})
	}
}

func TestSaveWithdrawalReportsFilesystemFailures(t *testing.T) {
	t.Run("directory cannot be created", func(t *testing.T) {
		// A regular file where the directory should go.
		base := t.TempDir()
		blocker := filepath.Join(base, "withdrawals")
		if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if _, err := SaveWithdrawal(blocker, txHash, sample()); err == nil {
			t.Fatal("SaveWithdrawal succeeded with a file in the directory's place")
		} else if !strings.Contains(err.Error(), "create withdrawal directory") {
			t.Errorf("error = %v, want it to name the directory failure", err)
		}
	})

	t.Run("file cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		// A directory where the JSON file should go.
		if err := os.Mkdir(filepath.Join(dir, txHash.Hex()+".json"), 0o750); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if _, err := SaveWithdrawal(dir, txHash, sample()); err == nil {
			t.Fatal("SaveWithdrawal succeeded with a directory in the file's place")
		} else if !strings.Contains(err.Error(), "write") {
			t.Errorf("error = %v, want it to name the write failure", err)
		}
	})
}
