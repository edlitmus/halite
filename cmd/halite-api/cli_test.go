package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// See cmd/halite-node/cli_test.go for the re-execution pattern.
const reexec = "HALITE_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexec) != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), reexec+"=1")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

func TestVersion(t *testing.T) {
	out, _, code := run(t, "version")
	if code != 0 || !strings.HasPrefix(out, "halite-api ") {
		t.Errorf("version = %q %d", out, code)
	}
}

// Everything else this binary names is phase 4. Each must say so: an
// operator who runs `halite-api serve` today needs to learn that it is
// not built rather than that the argument was wrong.
func TestPhaseFourSubcommandsSayWhy(t *testing.T) {
	for _, sub := range []string{"serve", "token", "policy"} {
		_, errOut, code := run(t, sub)
		if code == 0 {
			t.Errorf("%s exited 0", sub)
		}
		if !strings.Contains(errOut, "phase 4") {
			t.Errorf("%s should name the phase: %q", sub, errOut)
		}
	}
}

func TestUnknownSubcommand(t *testing.T) {
	_, errOut, code := run(t, "nosuchthing")
	if code != 2 || !strings.Contains(errOut, "unknown subcommand") {
		t.Errorf("got %q %d", errOut, code)
	}
}
