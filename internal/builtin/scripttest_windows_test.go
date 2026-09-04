package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// writeEchoScript writes a script that prints "hello " and its first
// argument.
//
// @echo off, or cmd.exe echoes each line of the batch file before
// running it and the test reads the script back as its own output.
func writeEchoScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "s.cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\necho hello %1\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeFailingScript writes a script that prints to standard error and
// exits 3.
func writeFailingScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "s.cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\necho oops 1>&2\r\nexit /b 3\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// shellLine joins commands into one line for a Shell command. cmd.exe
// separates with `&`; `;` is an ordinary character there.
func shellLine(a, b string) string { return a + " & " + b }

// trueProgram is a program that exits 0, and its arguments.
func trueProgram() (name string, args []any) {
	return comspecForTest(), []any{"/c", "exit 0"}
}

func comspecForTest() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return `C:\Windows\system32\cmd.exe`
}
