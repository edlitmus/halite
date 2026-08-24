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

// runWithStdin is the same with something on standard input, which is
// how the password reaches `account hash`: never as an argument, which
// reaches the process table and the shell history.
func runWithStdin(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), reexec+"=1")
	cmd.Stdin = strings.NewReader(stdin)
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

// The subcommands this binary has must describe themselves rather than
// fail as typos. `serve` and `token` need state this test has none of,
// so they are asked what they are; `policy` is the hub's and says so.
func TestTheSubcommandsDescribeThemselves(t *testing.T) {
	out, _, code := run(t, "serve", "--help")
	if code != 0 || !strings.Contains(out, "halite-api serve") {
		t.Errorf("serve --help = %q %d", out, code)
	}

	// Bare, each needs an argument and says which.
	_, errOut, code := run(t, "token")
	if code == 0 || !strings.Contains(errOut, "subcommand") {
		t.Errorf("token = %q %d", errOut, code)
	}
	_, errOut, code = run(t, "account")
	if code == 0 || !strings.Contains(errOut, "hash") {
		t.Errorf("account = %q %d", errOut, code)
	}

	// The policy is one file, read by the hub and by this service, and
	// there is one command for it.
	_, errOut, code = run(t, "policy")
	if code == 0 || !strings.Contains(errOut, "halite-hub policy") {
		t.Errorf("policy = %q %d", errOut, code)
	}
}

// The password never becomes an argument, and the hash it produces
// carries its own parameters so the cost can be raised later.
func TestAccountHashReadsThePasswordFromStdin(t *testing.T) {
	out, _, code := runWithStdin(t, "hunter2\n", "account", "hash")
	if code != 0 {
		t.Fatalf("account hash exited %d: %q", code, out)
	}
	hash := strings.TrimSpace(out)
	if !strings.HasPrefix(hash, "pbkdf2-sha512$") {
		t.Fatalf("the hash is %q", hash)
	}
	if strings.Contains(hash, "hunter2") {
		t.Error("the password is in the hash output")
	}
	// Four fields: the algorithm, the cost, the salt, and the key.
	if n := strings.Count(hash, "$"); n != 3 {
		t.Errorf("the hash has %d separators: %q", n, hash)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	_, errOut, code := run(t, "nosuchthing")
	if code != 2 || !strings.Contains(errOut, "unknown subcommand") {
		t.Errorf("got %q %d", errOut, code)
	}
}
