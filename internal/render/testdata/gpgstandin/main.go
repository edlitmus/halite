// Command gpgstandin stands in for gpg in the renderer's tests.
//
// It records the argument vector it was given and copies standard input
// to standard output, which is enough to check the property SPEC 12.6
// actually asks for: the ciphertext reaches gpg on standard input and
// never in the argument vector, where any account on the machine can
// read it out of the process table while the command runs.
//
// A compiled program rather than a shell script, for the reason the
// bridge's test extension is one: the script was `#!/bin/sh`, which is
// not a program on every platform this ships to, and a test that cannot
// run is not a test.
package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	// The file to record into is named in the environment rather than
	// in the vector, so that recording does not change what is being
	// recorded.
	if path := os.Getenv("HALITE_STANDIN_ARGV"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(os.Args[1:], "\n")+"\n"), 0o600)
	}
	_, _ = io.Copy(os.Stdout, os.Stdin)
}
