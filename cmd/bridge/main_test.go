package main

import (
	"os"
	"testing"
)

// TestMain_Covered runs main itself.
//
// main is three tokens of glue, but "three tokens of glue" is also how a
// command ends up passing the wrong argv or swallowing an exit code. Swapping
// osExit and os.Args makes it observable without spawning a subprocess.
func TestMain_Covered(t *testing.T) {
	var got int
	called := false

	prevExit, prevArgs := osExit, os.Args
	t.Cleanup(func() { osExit, os.Args = prevExit, prevArgs })

	osExit = func(code int) { got, called = code, true }
	os.Args = []string{"bridge", "version"}

	main()

	if !called {
		t.Fatal("main did not call osExit")
	}
	if got != exitOK {
		t.Errorf("main exited %d, want %d", got, exitOK)
	}
}

// A failing subcommand must reach osExit with a non-zero code, so that a shell
// or CI step actually sees the failure.
func TestMain_PropagatesFailure(t *testing.T) {
	var got int

	prevExit, prevArgs := osExit, os.Args
	t.Cleanup(func() { osExit, os.Args = prevExit, prevArgs })

	osExit = func(code int) { got = code }
	os.Args = []string{"bridge", "teleport"}

	main()

	if got != exitUsage {
		t.Errorf("main exited %d, want %d", got, exitUsage)
	}
}
