package route

import (
	"errors"
	"strings"
	"testing"

	"github.com/pigfox/eth-bridge-go/internal/config"
)

func TestResolve(t *testing.T) {
	const (
		eth  = config.ChainIDEthSepolia
		base = config.ChainIDBaseSepolia
	)

	tests := []struct {
		name     string
		src, dst uint64
		want     Kind
	}{
		{"eth to base is a deposit", eth, base, KindDeposit},
		{"base to eth initiates a withdrawal", base, eth, KindWithdrawInitiate},
		{"eth to eth is same-chain", eth, eth, KindSameChain},
		{"base to base is same-chain", base, base, KindSameChain},
		// Same-chain needs no bridge contract, so it is not restricted to the
		// chains this tool knows how to bridge between.
		{"mainnet to mainnet is same-chain", 1, 1, KindSameChain},
		{"optimism to optimism is same-chain", 10, 10, KindSameChain},
		{"polygon to polygon is same-chain", 137, 137, KindSameChain},
		{"arbitrum to arbitrum is same-chain", 42161, 42161, KindSameChain},
		{"a local devnet is same-chain", 31337, 31337, KindSameChain},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.src, tc.dst)
			if err != nil {
				t.Fatalf("Resolve(%d, %d): %v", tc.src, tc.dst, err)
			}
			if got != tc.want {
				t.Errorf("Resolve(%d, %d) = %v, want %v", tc.src, tc.dst, got, tc.want)
			}
		})
	}
}

func TestResolveUnsupported(t *testing.T) {
	tests := []struct {
		name     string
		src, dst uint64
	}{
		{"mainnet to base sepolia", 1, config.ChainIDBaseSepolia},
		{"base sepolia to mainnet", config.ChainIDBaseSepolia, 1},
		{"two unrelated chains", 137, 42161},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.src, tc.dst)
			if !errors.Is(err, ErrUnsupportedRoute) {
				t.Fatalf("Resolve(%d, %d) error = %v, want ErrUnsupportedRoute", tc.src, tc.dst, err)
			}
			if got != KindUnknown {
				t.Errorf("Resolve(%d, %d) = %v, want KindUnknown", tc.src, tc.dst, got)
			}
			if !strings.Contains(err.Error(), "->") {
				t.Errorf("error %q should name the offending pair", err)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindSameChain, "same-chain"},
		{KindDeposit, "deposit"},
		{KindWithdrawInitiate, "withdraw-initiate"},
		{KindUnknown, "unknown"},
		{Kind(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestErrNotImplementedIsDistinct(t *testing.T) {
	if errors.Is(ErrNotImplemented, ErrUnsupportedRoute) {
		t.Error("ErrNotImplemented must not match ErrUnsupportedRoute: an unimplemented route is recognised, an unsupported one is not")
	}
}
