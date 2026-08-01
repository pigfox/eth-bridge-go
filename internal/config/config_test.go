package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// A throwaway secp256k1 key. It funds nothing and exists only so the tests can
// assert that a key derives to the address the configuration names.
const (
	testPK   = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	otherPK  = "8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a"
	destAddr = "0x000000000000000000000000000000000000dEaD"
)

// testAddr is the address testPK derives to.
func testAddr(t *testing.T) string {
	t.Helper()
	key, err := crypto.HexToECDSA(testPK)
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return crypto.PubkeyToAddress(key.PublicKey).Hex()
}

// env builds a getenv function over a map, so a test can state exactly the
// environment it wants and nothing else.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// baseEnv is a valid same-chain-on-L2 environment.
func baseEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		EnvSourceAddr:    testAddr(t),
		EnvSourcePK:      testPK,
		EnvDestAddr:      destAddr,
		EnvSourceChainID: "84532",
		EnvDestChainID:   "84532",
		EnvL2RPCURL:      "https://base-sepolia.example/v1/abc123",
	}
}

func TestLoadSameChainL2(t *testing.T) {
	cfg, err := Load(env(baseEnv(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourceAddr.Hex() != testAddr(t) {
		t.Errorf("SourceAddr = %s, want %s", cfg.SourceAddr.Hex(), testAddr(t))
	}
	if cfg.DestAddr.Hex() != destAddr {
		t.Errorf("DestAddr = %s, want %s", cfg.DestAddr.Hex(), destAddr)
	}
	if cfg.SourceChainID != ChainIDBaseSepolia || cfg.DestChainID != ChainIDBaseSepolia {
		t.Errorf("chain IDs = %d/%d", cfg.SourceChainID, cfg.DestChainID)
	}
	// A same-chain L2 route must not demand an L1 endpoint.
	if cfg.L1RPCURL != "" {
		t.Errorf("L1RPCURL = %q, want empty", cfg.L1RPCURL)
	}
	if cfg.SourceKey() == nil {
		t.Fatal("SourceKey is nil")
	}
	if got := crypto.PubkeyToAddress(cfg.SourceKey().PublicKey); got != cfg.SourceAddr {
		t.Errorf("key derives to %s, want %s", got.Hex(), cfg.SourceAddr.Hex())
	}
}

func TestLoadDepositRouteNeedsBothRPCs(t *testing.T) {
	m := baseEnv(t)
	m[EnvSourceChainID] = "11155111"
	m[EnvL1RPCURL] = "https://eth-sepolia.example/v1/xyz"

	cfg, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.L1RPCURL == "" || cfg.L2RPCURL == "" {
		t.Errorf("both RPC URLs should be set, got %q / %q", cfg.L1RPCURL, cfg.L2RPCURL)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr error
		wantIn  string
	}{
		{"missing source addr", func(m map[string]string) { delete(m, EnvSourceAddr) }, ErrMissing, EnvSourceAddr},
		{"blank source addr", func(m map[string]string) { m[EnvSourceAddr] = "   " }, ErrMissing, EnvSourceAddr},
		{"invalid source addr", func(m map[string]string) { m[EnvSourceAddr] = "0xnope" }, ErrInvalid, EnvSourceAddr},
		{"missing dest addr", func(m map[string]string) { delete(m, EnvDestAddr) }, ErrMissing, EnvDestAddr},
		{"invalid dest addr", func(m map[string]string) { m[EnvDestAddr] = "not-an-address" }, ErrInvalid, EnvDestAddr},
		{"missing source chain", func(m map[string]string) { delete(m, EnvSourceChainID) }, ErrMissing, EnvSourceChainID},
		{"unparseable source chain", func(m map[string]string) { m[EnvSourceChainID] = "eleven" }, ErrInvalid, EnvSourceChainID},
		{"unsupported source chain", func(m map[string]string) { m[EnvSourceChainID] = "1" }, ErrInvalid, "not a supported chain"},
		{"missing dest chain", func(m map[string]string) { delete(m, EnvDestChainID) }, ErrMissing, EnvDestChainID},
		{"missing pk", func(m map[string]string) { delete(m, EnvSourcePK) }, ErrMissing, EnvSourcePK},
		{"invalid pk", func(m map[string]string) { m[EnvSourcePK] = "0xzzzz" }, ErrInvalid, EnvSourcePK},
		{"pk mismatch", func(m map[string]string) { m[EnvSourcePK] = otherPK }, ErrInvalid, "derives to"},
		{"missing l2 rpc", func(m map[string]string) { delete(m, EnvL2RPCURL) }, ErrMissing, EnvL2RPCURL},
		{"missing l1 rpc", func(m map[string]string) {
			m[EnvSourceChainID] = "11155111"
		}, ErrMissing, EnvL1RPCURL},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := baseEnv(t)
			tc.mutate(m)

			_, err := Load(env(m))
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want errors.Is %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
			// No error path may ever quote the private key.
			assertNoSecret(t, err.Error())
		})
	}
}

// TestLoadPKIsAcceptedWithOrWithoutPrefix pins the 0x tolerance, because the
// two sources an operator copies a key from disagree about it.
func TestLoadPKIsAcceptedWithOrWithoutPrefix(t *testing.T) {
	for _, pk := range []string{testPK, "0x" + testPK} {
		m := baseEnv(t)
		m[EnvSourcePK] = pk
		if _, err := Load(env(m)); err != nil {
			t.Errorf("Load with %q...: %v", pk[:4], err)
		}
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	cfg, err := Load(env(baseEnv(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.String()
	assertNoSecret(t, s)
	if !strings.Contains(s, redacted) {
		t.Errorf("String() = %q, want it to contain %q", s, redacted)
	}
	if strings.Contains(s, "abc123") {
		t.Errorf("String() = %q leaks the RPC URL path", s)
	}
	if !strings.Contains(s, "https://base-sepolia.example/") {
		t.Errorf("String() = %q dropped the RPC host, which is not a secret", s)
	}
}

// assertNoSecret fails if s contains the private key in any of its spellings.
func assertNoSecret(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{testPK, "0x" + testPK, strings.ToUpper(testPK), otherPK} {
		if strings.Contains(s, secret) {
			t.Fatalf("output leaks a private key: %q", s)
		}
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"no-scheme-here", redacted},
		{"https://", redacted},
		{"https://host.example", "https://host.example/" + redacted},
		{"https://host.example/v1/key", "https://host.example/" + redacted},
		{"wss://host.example/ws?key=1", "wss://host.example/" + redacted},
	}
	for _, tc := range tests {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRPCFor(t *testing.T) {
	cfg := Config{L1RPCURL: "l1", L2RPCURL: "l2"}

	if got, err := cfg.RPCFor(ChainIDEthSepolia); err != nil || got != "l1" {
		t.Errorf("RPCFor(eth) = %q, %v", got, err)
	}
	if got, err := cfg.RPCFor(ChainIDBaseSepolia); err != nil || got != "l2" {
		t.Errorf("RPCFor(base) = %q, %v", got, err)
	}
	got, err := cfg.RPCFor(1)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("RPCFor(1) error = %v, want ErrInvalid", err)
	}
	if got != "" {
		t.Errorf("RPCFor(1) = %q, want empty", got)
	}
}

func TestSupportedChainID(t *testing.T) {
	for _, id := range []uint64{ChainIDEthSepolia, ChainIDBaseSepolia} {
		if !SupportedChainID(id) {
			t.Errorf("SupportedChainID(%d) = false, want true", id)
		}
	}
	for _, id := range []uint64{0, 1, 10, 8453} {
		if SupportedChainID(id) {
			t.Errorf("SupportedChainID(%d) = true, want false", id)
		}
	}
}

// The withdrawals directory is optional, because most routes never write one.
func TestWithdrawalsDirDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load(env(baseEnv(t)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WithdrawalsDir != DefaultWithdrawalsDir {
		t.Errorf("WithdrawalsDir = %q, want the default %q", cfg.WithdrawalsDir, DefaultWithdrawalsDir)
	}

	m := baseEnv(t)
	m[EnvWithdrawalsDir] = "  /var/withdrawals  "
	cfg, err = Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WithdrawalsDir != "/var/withdrawals" {
		t.Errorf("WithdrawalsDir = %q, want it trimmed", cfg.WithdrawalsDir)
	}

	// Whitespace is not an override.
	m[EnvWithdrawalsDir] = "   "
	cfg, err = Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WithdrawalsDir != DefaultWithdrawalsDir {
		t.Errorf("WithdrawalsDir = %q, want the default", cfg.WithdrawalsDir)
	}
}
