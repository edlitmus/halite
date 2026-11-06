package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// A `run` command line that is not understood exits 64, before anything
// reaches the hub.
//
// It exited 1, through cli.Fatalf -- which is also what `run` exits when a
// node's job failed, so a script could not tell "the deploy failed on
// web3" from "I typed the command wrong", and an operator alerting on 1
// was paged about a typo. DIVERGENCE 5.210 moved the hub's other usage
// errors to cli.ExitUsage; `run`'s were not among them.
//
// And two of its numbers were read with fmt.Sscanf, which reports a
// failure that nothing checked. `--subset abc` left the subset at 0 and
// `--subset=-1` set it to -1, both of which mean no subset at all: a job
// meant for two nodes of a target went to every one of them. And
// `--subset 2x` read as 2 and `1.5` as 1, the number ending wherever the
// digits did. Those are refused now, like any other malformed flag. So is a flag `run` does not take, which is
// cli.RejectUnknownFlags's refusal and exited 1 in all three programs.
func TestRunRefusesAMalformedCommandLineAsAUsageError(t *testing.T) {
	root := t.TempDir()

	// The control: a well-formed command line in this empty root gets as
	// far as the operator certificate, and fails there with 1. So a 64
	// below is the argument check, not a refusal from further in.
	control := run(t, "run", "--root", root, "*", "test.ping")
	if control.code != 1 || !strings.Contains(control.stderr, "operator certificate") {
		t.Fatalf("a well-formed run in an empty root exited %d (%s); this test assumes it "+
			"reaches the operator certificate and fails there", control.code, control.stderr)
	}

	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"no target", nil, "needs a target"},
		{"no function", []string{"-G", "os:FreeBSD"}, "needs a function"},
		{"a subset that is not a number", []string{"--subset", "2x", "*", "test.ping"}, "--subset"},
		// With `=`: `--subset -1` is refused by the flag parser as an
		// unknown flag `-1`, which is a usage error too but not the check
		// this case is about.
		{"a negative subset", []string{"--subset=-1", "*", "test.ping"}, "--subset"},
		{"a subset of zero", []string{"--subset", "0", "*", "test.ping"}, "--subset"},
		{"an unknown flag", []string{"--subest", "2", "*", "test.ping"}, "did you mean --subset"},
		{"a safe limit that is not a number", []string{"--batch-safe-limit", "ten", "*", "test.ping"}, "--batch-safe-limit"},
		{"a bad ttl", []string{"--ttl", "soon", "*", "test.ping"}, "--ttl"},
		{"a bad timeout", []string{"--timeout", "soon", "*", "test.ping"}, "--timeout"},
		{"a bad batch wait", []string{"--batch-wait", "soon", "*", "test.ping"}, "--batch-wait"},
		// The wire carries whole seconds, and 0 or less is the hub's
		// default: 500ms went as 0 and became fifteen minutes, and -5s
		// went as -5 and became the same. DIVERGENCE 5.603.
		{"a ttl under a second", []string{"--ttl", "500ms", "*", "test.ping"}, "--ttl"},
		{"a ttl that is not whole seconds", []string{"--ttl", "1500ms", "*", "test.ping"}, "--ttl"},
		{"a negative ttl", []string{"--ttl=-5s", "*", "test.ping"}, "--ttl"},
		{"a ttl of zero", []string{"--ttl", "0s", "*", "test.ping"}, "--ttl"},
		{"a negative batch wait", []string{"--batch-wait=-1s", "*", "test.ping"}, "--batch-wait"},
		{"a batch timeout of zero", []string{"--batch-timeout", "0", "*", "test.ping"}, "--batch-timeout"},
		{"a batch timeout under a second", []string{"--batch-timeout", "200ms", "*", "test.ping"}, "--batch-timeout"},
		{"an argument that looks like JSON and is not", []string{"*", "test.ping", "x={nope"}, "did not parse"},
		{"an output format that does not exist", []string{"--out", "table", "*", "test.ping"}, "table"},
		{"two signers", []string{"--sign-key", "k", "--sign-extension", "e", "*", "test.ping"}, "two different signers"},
	} {
		res := run(t, append([]string{"run", "--root", root}, tc.args...)...)
		if res.code != cli.ExitUsage {
			t.Errorf("%s: exited %d, want %d: %s", tc.name, res.code, cli.ExitUsage, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%s: the message does not say %q: %s", tc.name, tc.says, res.stderr)
		}
		if strings.Contains(res.stderr, "operator certificate") {
			t.Errorf("%s: reached for the hub before the command line was understood: %s",
				tc.name, res.stderr)
		}
	}
}

// The other side of the cases above: a duration the wire can carry goes
// through, and so does `--batch-wait 0`, which asks for no settle time
// and is the one zero that means what it says. Each reaches the operator
// certificate, which in this empty root is as far as `run` gets.
// DIVERGENCE 5.603.
func TestRunTakesADurationTheWireCanCarry(t *testing.T) {
	root := t.TempDir()
	for _, flags := range [][]string{
		{"--ttl", "90s"},
		{"--ttl", "2m"},
		{"--batch-wait", "0"},
		{"--batch-wait", "1s", "--batch-timeout", "30s"},
	} {
		argv := append(append([]string{"run", "--root", root}, flags...), "*", "test.ping")
		res := run(t, argv...)
		if res.code != 1 || !strings.Contains(res.stderr, "operator certificate") {
			t.Errorf("%v: exited %d (%s); a well-formed duration should reach the operator certificate",
				flags, res.code, res.stderr)
		}
	}
	if got := seconds("90s", "ttl", false); got != 90 {
		t.Errorf("seconds(90s) = %d, want 90", got)
	}
}
