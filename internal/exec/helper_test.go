package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests in this package have to run real programs, and until now
// they named /bin/echo, /bin/sh and /usr/bin/env. None of those exists
// on Windows, so eleven of them failed there with "executable file not
// found in %PATH%" — which says nothing about halite and meant the
// package was, on tier 1's first platform, testing nothing.
//
// The program they run is this test binary, re-invoked with a sentinel
// argument. That is portable by construction: whatever built the tests
// can run them.

// helperSentinel marks the helper's arguments. It follows the flag
// package's own terminator, "--", because the test binary parses its
// flags before main and rejects anything it does not recognise.
const helperSentinel = "halite-helper"

// TestHelperHarness is the helper program, not a test.
//
// It is spelled as a test because that is the only entry point a test
// binary has. A run without the sentinel is a no-op, so `go test` sees
// a test that passes and does nothing.
func TestHelperHarness(t *testing.T) {
	args := helperArgs()
	if args == nil {
		return
	}
	os.Exit(runHelper(args))
}

// helperArgs returns the helper's arguments, or nil when this process
// was not started as one.
func helperArgs() []string {
	for i, a := range os.Args {
		if a == helperSentinel {
			return os.Args[i+1:]
		}
	}
	return nil
}

// runHelper is the helper's body. Each mode stands in for one unix
// program the tests used to name.
func runHelper(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "the helper needs a mode")
		return 2
	}
	switch args[0] {
	case "echo":
		// One argument per line rather than space-separated, so a test
		// can tell "one argument containing a space" from "two
		// arguments" — which is the whole subject of the argv tests.
		for _, a := range args[1:] {
			fmt.Println(a)
		}
	case "env":
		for _, e := range os.Environ() {
			fmt.Println(e)
		}
	case "pwd":
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println(wd)
	case "cat-then-pwd":
		buf := make([]byte, 4096)
		n, _ := os.Stdin.Read(buf)
		os.Stdout.Write(buf[:n])
		wd, _ := os.Getwd()
		fmt.Println(wd)
	case "sleep":
		d, err := time.ParseDuration(args[1])
		if err != nil {
			return 2
		}
		time.Sleep(d)
	case "say-and-exit":
		// stdout, stderr, and an exit code, which is what the exit-code
		// test needs and what `sh -c 'echo out; echo problem >&2; exit 3'`
		// was there for.
		fmt.Println(args[1])
		fmt.Fprintln(os.Stderr, args[2])
		code, _ := strconv.Atoi(args[3])
		return code
	case "spawn-then-sleep":
		// Starts a grandchild that waits and then creates a marker, and
		// then sleeps itself. A timeout that kills only the direct child
		// leaves the grandchild to create the marker; one that kills the
		// whole tree does not.
		child := helperArgv("delayed-touch", args[1])
		if err := startDetached(child); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		time.Sleep(30 * time.Second)
	case "delayed-touch":
		time.Sleep(2 * time.Second)
		f, err := os.Create(filepath.Clean(args[1]))
		if err != nil {
			return 1
		}
		f.Close()
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", args[0])
		return 2
	}
	return 0
}

// helperArgv is the argument vector that runs this test binary as the
// helper.
//
// -test.run selects a test that returns immediately, so nothing else in
// the package runs; the sentinel and what follows are the helper's.
func helperArgv(args ...string) []string {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	argv := []string{self, "-test.run=^TestHelperHarness$", "--", helperSentinel}
	return append(argv, args...)
}

// helperCommand is helperArgv as a Command.
func helperCommand(args ...string) Command {
	return Command{Argv: helperArgv(args...)}
}

// helperEnv is the environment the helper needs on top of whatever the
// test is checking. The test binary's own flag parsing needs nothing,
// so this is empty; it exists so a reader does not go looking.
func helperEnv() []string { return nil }

var _ = strings.TrimSpace
var _ = testing.Verbose
