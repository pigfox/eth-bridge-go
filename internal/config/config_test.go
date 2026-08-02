package config

import (
	"errors"
	"strconv"
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

// baseEnv is a valid same-chain environment.
func baseEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		EnvSourceAddr:    testAddr(t),
		EnvSourcePK:      testPK,
		EnvDestAddr:      destAddr,
		EnvSourceChainID: "84532",
		EnvDestChainID:   "84532",
		EnvSourceRPCURL:  "https://base-sepolia.example/v1/abc123",
	}
}

func TestLoadSameChain(t *testing.T) {
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
	if cfg.SourceChainID != 84532 || cfg.DestChainID != 84532 {
		t.Errorf("chain IDs = %d/%d", cfg.SourceChainID, cfg.DestChainID)
	}
	// A same-chain route has one endpoint, and it stands in for both sides.
	if cfg.DestRPCURL != cfg.SourceRPCURL {
		t.Errorf("DestRPCURL = %q, want it to mirror SourceRPCURL %q", cfg.DestRPCURL, cfg.SourceRPCURL)
	}
	if cfg.SourceKey() == nil {
		t.Fatal("SourceKey is nil")
	}
	if got := crypto.PubkeyToAddress(cfg.SourceKey().PublicKey); got != cfg.SourceAddr {
		t.Errorf("key derives to %s, want %s", got.Hex(), cfg.SourceAddr.Hex())
	}
}

// TestLoadSameChainAcceptsAnyEVMChain is the P1 claim in one test: a same-chain
// transfer needs no bridge contract and no pairing, so no chain ID is special.
func TestLoadSameChainAcceptsAnyEVMChain(t *testing.T) {
	for _, id := range []uint64{1, 10, 137, 42161, 84532, 11155111, 31337} {
		m := baseEnv(t)
		m[EnvSourceChainID] = strconv.FormatUint(id, 10)
		m[EnvDestChainID] = strconv.FormatUint(id, 10)

		cfg, err := Load(env(m))
		if err != nil {
			t.Errorf("Load for chain %d: %v", id, err)
			continue
		}
		if cfg.SourceChainID != id || cfg.DestChainID != id {
			t.Errorf("chain IDs = %d/%d, want %d", cfg.SourceChainID, cfg.DestChainID, id)
		}
		rpc, err := cfg.RPCFor(id)
		if err != nil || rpc != m[EnvSourceRPCURL] {
			t.Errorf("RPCFor(%d) = %q, %v", id, rpc, err)
		}
	}
}

func TestLoadCrossChainRouteNeedsBothRPCs(t *testing.T) {
	m := baseEnv(t)
	m[EnvSourceChainID] = "11155111"
	m[EnvDestRPCURL] = "https://l2.example/v1/xyz"

	cfg, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourceRPCURL == "" || cfg.DestRPCURL == "" {
		t.Errorf("both RPC URLs should be set, got %q / %q", cfg.SourceRPCURL, cfg.DestRPCURL)
	}
	if cfg.SourceRPCURL == cfg.DestRPCURL {
		t.Error("a cross-chain route must not collapse its two endpoints")
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
		{"zero source chain", func(m map[string]string) { m[EnvSourceChainID] = "0" }, ErrInvalid, "is not a chain ID"},
		{"missing dest chain", func(m map[string]string) { delete(m, EnvDestChainID) }, ErrMissing, EnvDestChainID},
		{"missing pk", func(m map[string]string) { delete(m, EnvSourcePK) }, ErrMissing, EnvSourcePK},
		{"invalid pk", func(m map[string]string) { m[EnvSourcePK] = "0xzzzz" }, ErrInvalid, EnvSourcePK},
		{"pk mismatch", func(m map[string]string) { m[EnvSourcePK] = otherPK }, ErrInvalid, "derives to"},
		{"missing source rpc", func(m map[string]string) { delete(m, EnvSourceRPCURL) }, ErrMissing, EnvSourceRPCURL},
		{"missing dest rpc on a cross-chain route", func(m map[string]string) {
			m[EnvSourceChainID] = "11155111"
		}, ErrMissing, EnvDestRPCURL},
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
	cfg := Config{SourceChainID: 42161, DestChainID: 137, SourceRPCURL: "src", DestRPCURL: "dst"}

	if got, err := cfg.RPCFor(42161); err != nil || got != "src" {
		t.Errorf("RPCFor(source) = %q, %v", got, err)
	}
	if got, err := cfg.RPCFor(137); err != nil || got != "dst" {
		t.Errorf("RPCFor(dest) = %q, %v", got, err)
	}
	got, err := cfg.RPCFor(1)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("RPCFor(1) error = %v, want ErrInvalid", err)
	}
	if got != "" {
		t.Errorf("RPCFor(1) = %q, want empty", got)
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
