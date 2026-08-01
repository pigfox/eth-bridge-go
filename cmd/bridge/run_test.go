package main

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/chain/fake"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/opstack"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

// A throwaway key, and the address it derives to. It funds nothing.
const (
	testPK   = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
	testAddr = "0x2c7536E3605D9C16a7a3D7b1898e529396a65c23"
	destAddr = "0x000000000000000000000000000000000000dEaD"
)

var errBoom = errors.New("boom")

// baseEnv is a valid same-chain Base Sepolia environment.
func baseEnv() map[string]string {
	return map[string]string{
		config.EnvSourceAddr:    testAddr,
		config.EnvSourcePK:      testPK,
		config.EnvDestAddr:      destAddr,
		config.EnvSourceChainID: "84532",
		config.EnvDestChainID:   "84532",
		config.EnvL2RPCURL:      "https://base-sepolia.example",
	}
}

// withEnv installs a getenv over m for the duration of the test.
func withEnv(t *testing.T, m map[string]string) {
	t.Helper()
	prev := getenv
	getenv = func(k string) string { return m[k] }
	t.Cleanup(func() { getenv = prev })
}

// withDial installs a dialer for the duration of the test.
func withDial(t *testing.T, d func(context.Context, string) (chain.Client, error)) {
	t.Helper()
	prev := dial
	dial = d
	t.Cleanup(func() { dial = prev })
}

// minedClient is a fake scripted for one successful same-chain send.
func minedClient() *fake.Client {
	c := &fake.Client{}
	c.PushChainID(big.NewInt(84532), nil)
	c.PushNonce(1, nil)
	c.PushTipCap(big.NewInt(1_000_000), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(1_000_000)}, nil)
	c.PushGas(21000, nil)
	c.PushSend(nil)
	c.PushReceipt(&types.Receipt{Status: types.ReceiptStatusSuccessful}, nil)
	return c
}

// exec runs the command and returns its exit code and streams.
func exec(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunNoArgsPrintsUsage(t *testing.T) {
	code, stdout, stderr := exec()
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr = %q, want usage", stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestRunVersion(t *testing.T) {
	code, stdout, stderr := exec("version")
	if code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	if strings.TrimSpace(stdout) != version {
		t.Errorf("stdout = %q, want %q", stdout, version)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		code, stdout, _ := exec(arg)
		if code != exitOK {
			t.Errorf("%s: exit code = %d, want %d", arg, code, exitOK)
		}
		if !strings.Contains(stdout, "usage:") {
			t.Errorf("%s: stdout = %q, want usage", arg, stdout)
		}
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	code, _, stderr := exec("teleport")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, `unknown subcommand "teleport"`) {
		t.Errorf("stderr = %q, want it to name the subcommand", stderr)
	}
}

func TestSendSuccess(t *testing.T) {
	withEnv(t, baseEnv())
	c := minedClient()

	var dialled string
	withDial(t, func(_ context.Context, rpc string) (chain.Client, error) {
		dialled = rpc
		return c, nil
	})

	code, stdout, stderr := exec("send", "--amount", "0.001")
	if code != exitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	if dialled != "https://base-sepolia.example" {
		t.Errorf("dialled %q, want the configured L2 endpoint", dialled)
	}
	for _, want := range []string{"route:  same-chain", "1000000000000000 wei", "src tx: 0x", "https://sepolia.basescan.org/tx/0x"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout %q does not contain %q", stdout, want)
		}
	}
	if !c.Closed() {
		t.Error("the client was not closed")
	}
}

func TestSendFlagErrors(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		wantIn string
	}{
		{"unknown flag", []string{"send", "--bogus"}, "flag provided but not defined"},
		{"missing amount", []string{"send"}, "--amount is required"},
		{"empty amount", []string{"send", "--amount", ""}, "--amount is required"},
		{"unparseable amount", []string{"send", "--amount", "banana"}, "not a decimal number"},
		{"zero amount", []string{"send", "--amount", "0"}, "not greater than zero"},
		{"negative amount", []string{"send", "--amount", "-1"}, "not greater than zero"},
		{"amount rounds to zero", []string{"send", "--amount", "0.0000000000000000001"}, "rounds to zero wei"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// No environment and no dialer: these must fail before either is
			// touched.
			withEnv(t, map[string]string{})
			withDial(t, func(context.Context, string) (chain.Client, error) {
				t.Error("dialled a node for a bad flag")
				return nil, errBoom
			})

			code, _, stderr := exec(tc.args...)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr, tc.wantIn) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantIn)
			}
		})
	}
}

func TestSendConfigErrorIsAUsageError(t *testing.T) {
	m := baseEnv()
	delete(m, config.EnvSourcePK)
	withEnv(t, m)

	code, _, stderr := exec("send", "--amount", "0.001")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, config.EnvSourcePK) {
		t.Errorf("stderr = %q, want it to name the missing variable", stderr)
	}
	if strings.Contains(stderr, testPK) {
		t.Fatalf("stderr leaks the private key: %q", stderr)
	}
}

func TestSendRuntimeErrorExitsOne(t *testing.T) {
	withEnv(t, baseEnv())
	withDial(t, func(context.Context, string) (chain.Client, error) { return nil, errBoom })

	code, _, stderr := exec("send", "--amount", "0.001")
	if code != exitRuntime {
		t.Errorf("exit code = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("stderr = %q, want it to report the dial failure", stderr)
	}
}

// Withdrawal is resolved but not implemented in this version. The CLI must say
// so rather than pretend.
func TestDispatchWithdrawalIsNotImplemented(t *testing.T) {
	withDial(t, func(context.Context, string) (chain.Client, error) {
		t.Error("dialled a node for an unimplemented route")
		return nil, errBoom
	})

	cfg := config.Config{
		SourceChainID: config.ChainIDBaseSepolia,
		DestChainID:   config.ChainIDEthSepolia,
	}
	if _, err := dispatch(context.Background(), cfg, big.NewInt(1)); !errors.Is(err, route.ErrNotImplemented) {
		t.Fatalf("error = %v, want route.ErrNotImplemented", err)
	}
}

func TestDispatchUnsupportedRoute(t *testing.T) {
	cfg := config.Config{SourceChainID: 1, DestChainID: 137}
	_, err := dispatch(context.Background(), cfg, big.NewInt(1))
	if !errors.Is(err, route.ErrUnsupportedRoute) {
		t.Fatalf("error = %v, want route.ErrUnsupportedRoute", err)
	}
}

func TestDialChainRejectsAnUnconfiguredChain(t *testing.T) {
	withDial(t, func(context.Context, string) (chain.Client, error) {
		t.Error("dialled despite having no endpoint for the chain")
		return nil, errBoom
	})

	c, err := dialChain(context.Background(), config.Config{}, 999)
	if !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("error = %v, want config.ErrInvalid", err)
	}
	if c != nil {
		t.Error("dialChain returned a client alongside an error")
	}
}

func TestParseEther(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"1", "1000000000000000000"},
		{"0.001", "1000000000000000"},
		{"0.00001", "10000000000000"},
		{"0.0005", "500000000000000"},
		{"123.456789012345678", "123456789012345678000"},
		{"1e-9", "1000000000"},
	}
	for _, tc := range tests {
		got, err := parseEther(tc.in)
		if err != nil {
			t.Errorf("parseEther(%q): %v", tc.in, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("parseEther(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestParseEtherErrors(t *testing.T) {
	// "1e99999999999" overflows big.Float's exponent, which is the one way a
	// string can pass the decimal check and still fail to parse.
	for _, in := range []string{"", "banana", "0x10", "1_000", "0b11", "0", "-1", "-0.5", "0.0000000000000000001", "1e99999999999"} {
		if got, err := parseEther(in); !errors.Is(err, ErrBadAmount) {
			t.Errorf("parseEther(%q) = %v, %v; want ErrBadAmount", in, got, err)
		}
	}
}

func TestExplorerURL(t *testing.T) {
	const hash = "0xdeadbeef"
	tests := []struct {
		chainID uint64
		want    string
	}{
		{config.ChainIDEthSepolia, "https://sepolia.etherscan.io/tx/" + hash},
		{config.ChainIDBaseSepolia, "https://sepolia.basescan.org/tx/" + hash},
		{1, hash},
	}
	for _, tc := range tests {
		if got := explorerURL(tc.chainID, hash); got != tc.want {
			t.Errorf("explorerURL(%d) = %q, want %q", tc.chainID, got, tc.want)
		}
	}
}

// The test key must derive to the address the other tests configure, or every
// config.Load in this file would be failing for a reason unrelated to what it
// is testing.
func TestTestAddrMatchesTestPK(t *testing.T) {
	withEnv(t, baseEnv())
	if _, err := config.Load(getenv); err != nil {
		t.Fatalf("the test fixture is inconsistent: %v", err)
	}
}

// depositEnv is a valid Eth Sepolia -> Base Sepolia environment.
func depositEnv() map[string]string {
	m := baseEnv()
	m[config.EnvSourceChainID] = "11155111"
	m[config.EnvL1RPCURL] = "https://eth-sepolia.example"
	return m
}

// depositL1Client is a fake scripted for one successful L1 deposit, including
// the TransactionDeposited log the L2 hash is derived from.
func depositL1Client(amount *big.Int) *fake.Client {
	opaque := make([]byte, 0, 73)
	opaque = append(opaque, common.LeftPadBytes(amount.Bytes(), 32)...)
	opaque = append(opaque, common.LeftPadBytes(amount.Bytes(), 32)...)
	opaque = append(opaque, common.LeftPadBytes(big.NewInt(200000).Bytes(), 8)...)
	opaque = append(opaque, 0)

	data := append(common.LeftPadBytes(big.NewInt(32).Bytes(), 32),
		common.LeftPadBytes(big.NewInt(int64(len(opaque))).Bytes(), 32)...)
	data = append(data, opaque...)
	data = append(data, make([]byte, (32-len(opaque)%32)%32)...)

	c := &fake.Client{}
	c.PushChainID(big.NewInt(11155111), nil)
	c.PushNonce(1, nil)
	c.PushTipCap(big.NewInt(1_000_000), nil)
	c.PushHeader(&types.Header{BaseFee: big.NewInt(1_000_000)}, nil)
	c.PushGas(120_000, nil)
	c.PushSend(nil)
	c.PushReceipt(&types.Receipt{
		Status:      types.ReceiptStatusSuccessful,
		BlockNumber: big.NewInt(1),
		Logs: []*types.Log{{
			Topics: []common.Hash{
				opstack.TransactionDepositedTopic,
				common.HexToHash("0xaaaa"),
				common.HexToHash("0xbbbb"),
				common.BigToHash(big.NewInt(0)),
			},
			Data:      data,
			BlockHash: common.HexToHash("0xf00d"),
			Index:     0,
		}},
	}, nil)
	return c
}

func TestSendDepositReportsBothHashesAndTheCredit(t *testing.T) {
	withEnv(t, depositEnv())
	amount := big.NewInt(500_000_000_000_000) // 0.0005 ETH

	l1 := depositL1Client(amount)
	l2 := &fake.Client{}
	l2.PushChainID(big.NewInt(84532), nil)
	l2.PushBalance(big.NewInt(0), nil)            // before
	l2.PushBalance(new(big.Int).Set(amount), nil) // credited

	withDial(t, func(_ context.Context, rpc string) (chain.Client, error) {
		if rpc == "https://eth-sepolia.example" {
			return l1, nil
		}
		return l2, nil
	})

	code, stdout, stderr := exec("send", "--amount", "0.0005")
	if code != exitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr)
	}
	for _, want := range []string{
		"route:  deposit",
		"500000000000000 wei",
		"src tx: 0x",
		"https://sepolia.etherscan.io/tx/0x",
		"dst tx: 0x",
		"https://sepolia.basescan.org/tx/0x",
		"credit: 500000000000000 wei on chain 84532",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout %q does not contain %q", stdout, want)
		}
	}
	if !l1.Closed() || !l2.Closed() {
		t.Errorf("clients closed: l1=%v l2=%v", l1.Closed(), l2.Closed())
	}
}

// A deposit that reaches L1 and then fails must still print the L1 hash, and
// must still exit non-zero.
func TestSendDepositReportsPartialResultOnFailure(t *testing.T) {
	withEnv(t, depositEnv())

	l1 := depositL1Client(big.NewInt(1))
	l2 := &fake.Client{}
	l2.PushChainID(big.NewInt(84532), nil)
	l2.PushBalance(big.NewInt(0), nil)
	l2.PushBalance(nil, errBoom) // the credit poll fails

	withDial(t, func(_ context.Context, rpc string) (chain.Client, error) {
		if rpc == "https://eth-sepolia.example" {
			return l1, nil
		}
		return l2, nil
	})

	code, stdout, stderr := exec("send", "--amount", "0.000000000000000001")
	if code != exitRuntime {
		t.Fatalf("exit code = %d, want %d", code, exitRuntime)
	}
	if !strings.Contains(stdout, "src tx: 0x") {
		t.Errorf("stdout %q should still report the L1 hash", stdout)
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("stderr %q should report the failure", stderr)
	}
}

func TestDispatchDepositDialFailures(t *testing.T) {
	cfg := config.Config{
		SourceChainID: config.ChainIDEthSepolia,
		DestChainID:   config.ChainIDBaseSepolia,
		L1RPCURL:      "l1",
		L2RPCURL:      "l2",
	}

	tests := []struct {
		name   string
		failOn string
	}{
		{"L1 dial fails", "l1"},
		{"L2 dial fails", "l2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withDial(t, func(_ context.Context, rpc string) (chain.Client, error) {
				if rpc == tc.failOn {
					return nil, errBoom
				}
				return &fake.Client{}, nil
			})
			if _, err := dispatch(context.Background(), cfg, big.NewInt(1)); !errors.Is(err, errBoom) {
				t.Fatalf("error = %v, want errBoom", err)
			}
		})
	}
}

// report prints nothing at all when there is no transaction to report, so that
// a pure configuration failure does not emit a half-filled block.
func TestReportIsSilentWithoutATransaction(t *testing.T) {
	var buf bytes.Buffer
	report(&buf, config.Config{}, bridge.Result{})
	if buf.Len() != 0 {
		t.Errorf("report wrote %q, want nothing", buf.String())
	}
}
