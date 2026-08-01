package chain

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/ethclient"
)

// The point of declaring Client with go-ethereum's own signatures is that the
// real client satisfies it with no adapter in between. If go-ethereum changes a
// signature, this line is what fails, and it fails at compile time.
var _ Client = (*ethclient.Client)(nil)

func TestDial(t *testing.T) {
	// An HTTP endpoint is not contacted at dial time, so this succeeds
	// without a server and without a network round trip.
	c, err := Dial(context.Background(), "http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if c == nil {
		t.Fatal("Dial returned a nil client with a nil error")
	}
	c.Close()
}

func TestDialRejectsUnusableURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"unknown scheme", "ftp://example.invalid"},
		{"no scheme", "example.invalid"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Dial(context.Background(), tc.url)
			if err == nil {
				c.Close()
				t.Fatalf("Dial(%q) succeeded, want an error", tc.url)
			}
			if c != nil {
				t.Errorf("Dial(%q) returned a non-nil client alongside an error", tc.url)
			}
		})
	}
}
