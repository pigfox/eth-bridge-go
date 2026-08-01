package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"

	"github.com/pigfox/eth-bridge-go/internal/bridge"
	"github.com/pigfox/eth-bridge-go/internal/chain"
	"github.com/pigfox/eth-bridge-go/internal/config"
	"github.com/pigfox/eth-bridge-go/internal/route"
)

// version is the released version of this command.
const version = "0.1.0"

// Exit codes. Anything the operator can fix by retyping the command is a usage
// error; anything that needed the network or the chain to cooperate is a
// runtime error.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

// weiPerEth is 1e18 as a big.Float, for the ETH-to-wei conversion.
var weiPerEth = new(big.Float).SetPrec(amountPrec).SetInt(
	new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil),
)

// amountPrec is the mantissa precision used for amount arithmetic. Enough bits
// to hold 1e18 wei exactly and then some, so that a decimal ETH amount does not
// round on its way to an integer.
const amountPrec = 256

// Seams. Tests replace these to run the command without an environment or a
// node; nothing else in the package reaches for os or dials anything.
var (
	getenv = os.Getenv
	dial   = chain.Dial
)

const usage = `bridge — move testnet ETH between Ethereum Sepolia and Base Sepolia.

usage:
  bridge send --amount <eth>   send the configured route
  bridge version               print the version

configuration is read from the environment:
  BRIDGE_SOURCE_ADDR       funding account
  BRIDGE_SOURCE_PK         its private key (must derive to BRIDGE_SOURCE_ADDR)
  BRIDGE_DEST_ADDR         recipient on the destination chain
  BRIDGE_SOURCE_CHAIN_ID   11155111 (Eth Sepolia) or 84532 (Base Sepolia)
  BRIDGE_DEST_CHAIN_ID     11155111 (Eth Sepolia) or 84532 (Base Sepolia)
  BRIDGE_L1_RPC_URL        Ethereum Sepolia endpoint, if the route touches L1
  BRIDGE_L2_RPC_URL        Base Sepolia endpoint, if the route touches L2
`

// run is the whole command. It returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}

	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return exitOK
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
}

// runSend parses the send flags and performs the configured route.
func runSend(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	amountFlag := fs.String("amount", "", "amount to send, in ETH (for example 0.001)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *amountFlag == "" {
		fmt.Fprintln(stderr, "send: --amount is required")
		return exitUsage
	}
	amount, err := parseEther(*amountFlag)
	if err != nil {
		fmt.Fprintf(stderr, "send: %v\n", err)
		return exitUsage
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "send: %v\n", err)
		return exitUsage
	}

	res, err := dispatch(context.Background(), cfg, amount)
	if err != nil {
		fmt.Fprintf(stderr, "send: %v\n", err)
		return exitRuntime
	}

	fmt.Fprintf(stdout, "route:  %s\n", res.Kind)
	fmt.Fprintf(stdout, "amount: %s wei\n", res.Amount)
	fmt.Fprintf(stdout, "tx:     %s\n", res.SrcTxHash.Hex())
	fmt.Fprintf(stdout, "%s\n", explorerURL(cfg.SourceChainID, res.SrcTxHash.Hex()))
	return exitOK
}

// dispatch resolves the configured route and runs it.
//
// The route is resolved here rather than in runSend so that this function is
// the single place that turns a configuration into an operation, and so that
// the unsupported-route and unimplemented-route paths are reachable from a test
// that hands it a Config directly.
func dispatch(ctx context.Context, cfg config.Config, amount *big.Int) (bridge.Result, error) {
	kind, err := route.Resolve(cfg.SourceChainID, cfg.DestChainID)
	if err != nil {
		return bridge.Result{}, err
	}

	// Only the same-chain route ships in this version. Deposit and
	// withdraw-initiate are recognised by the resolver and fall to the default
	// arm, which says so plainly rather than half-doing them.
	switch kind {
	case route.KindSameChain:
		c, err := dialChain(ctx, cfg, cfg.SourceChainID)
		if err != nil {
			return bridge.Result{}, err
		}
		defer c.Close()
		return bridge.New(cfg, c, c).SameChain(ctx, amount)
	default:
		return bridge.Result{}, fmt.Errorf("%w: %s", route.ErrNotImplemented, kind)
	}
}

// dialChain opens a client for one of the configured chains.
func dialChain(ctx context.Context, cfg config.Config, chainID uint64) (chain.Client, error) {
	rpc, err := cfg.RPCFor(chainID)
	if err != nil {
		return nil, err
	}
	c, err := dial(ctx, rpc)
	if err != nil {
		return nil, fmt.Errorf("dial chain %d endpoint: %w", chainID, err)
	}
	return c, nil
}

// ErrBadAmount is returned when --amount cannot be turned into a positive
// number of wei.
var ErrBadAmount = errors.New("invalid amount")

// decimalAmount is what --amount is allowed to look like.
//
// big.Float.SetString is more generous than anyone typing an ETH amount
// intends: it accepts "0x10" as sixteen, along with binary and underscore
// forms. An operator who typed a hex-looking amount did not mean sixteen ETH,
// so the input is pinned to plain decimal, with an exponent, before it is
// parsed at all.
var decimalAmount = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// parseEther converts a decimal ETH amount into wei.
func parseEther(s string) (*big.Int, error) {
	if !decimalAmount.MatchString(s) {
		return nil, fmt.Errorf("%w: %q is not a decimal number", ErrBadAmount, s)
	}
	f, ok := new(big.Float).SetPrec(amountPrec).SetString(s)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a decimal number", ErrBadAmount, s)
	}
	if f.Sign() <= 0 {
		return nil, fmt.Errorf("%w: %q is not greater than zero", ErrBadAmount, s)
	}
	wei, _ := new(big.Float).SetPrec(amountPrec).Mul(f, weiPerEth).Int(nil)
	if wei.Sign() <= 0 {
		return nil, fmt.Errorf("%w: %q rounds to zero wei", ErrBadAmount, s)
	}
	return wei, nil
}

// explorerURL returns a block explorer link for a transaction hash.
func explorerURL(chainID uint64, hash string) string {
	switch chainID {
	case config.ChainIDEthSepolia:
		return "https://sepolia.etherscan.io/tx/" + hash
	case config.ChainIDBaseSepolia:
		return "https://sepolia.basescan.org/tx/" + hash
	default:
		return hash
	}
}
