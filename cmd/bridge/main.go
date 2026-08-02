// Command bridge moves testnet ETH within one EVM chain, or between an L1 and
// an OP Stack L2 anchored to it.
//
// This file holds no logic on purpose. Everything is in run, which takes its
// context, its arguments and its two output streams as parameters and returns
// an exit code instead of calling os.Exit, so that the whole command is
// reachable from a test. osExit is a variable for the same reason.
//
// The one thing this file does own is the root context: a process has exactly
// one, it is created at the entrypoint, and everything below threads it rather
// than minting its own.
package main

import (
	"context"
	"os"
)

var osExit = os.Exit

func main() { osExit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
