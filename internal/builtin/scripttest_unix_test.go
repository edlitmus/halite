//go:build !windows

package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// The cmd tests have to run a real script, and a script is written in
// the platform's own language. These wrote `#!/bin/sh` and named
// /bin/sh, so on Windows they failed on the shebang line rather than on
// anything halite does.

// writeEchoScript writes a script that prints "hello " and its first
// argument.
func writeEchoScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "s.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"hello $1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFailingScript writes a script that prints to standard error and
// exits 3.
func writeFailingScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "s.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho oops >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// shellLine joins commands into one line for a Shell command.
func shellLine(a, b string) string { return a + "; " + b }

// trueProgram is a program that exits 0, and its arguments.
func trueProgram() (name string, args []any) { return "/bin/sh", []any{"-c", "exit 0"} }
