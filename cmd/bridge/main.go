// Command bridge moves testnet ETH between Ethereum Sepolia and Base Sepolia.
//
// This file holds no logic on purpose. Everything is in run, which takes its
// arguments and its two output streams as parameters and returns an exit code
// instead of calling os.Exit, so that the whole command is reachable from a
// test. osExit is a variable for the same reason.
package main

import "os"

var osExit = os.Exit

func main() { osExit(run(os.Args[1:], os.Stdout, os.Stderr)) }
