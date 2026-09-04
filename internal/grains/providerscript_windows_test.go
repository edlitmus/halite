package grains

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A grain provider on Windows is a file the platform can run, which
// means a file with an extension it recognises. The tests wrote
// `20-dynamic` holding `#!/bin/sh`, mode 0755, which here is neither
// runnable nor YAML: the mode did nothing, the file was parsed as a
// document, and the operator got a YAML error about a shell script.

// writeProvider writes a grain provider that emits the given JSON, in
// the form this platform runs.
func writeProvider(t *testing.T, dir, name, json string) string {
	t.Helper()
	path := filepath.Join(dir, name+".cmd")
	// @echo off, or cmd.exe echoes the script before running it and the
	// JSON arrives with the batch file in front of it. The percent sign
	// has no special meaning here and the JSON is quoted so that the
	// redirection characters in it are not read as redirection.
	body := "@echo off\r\necho " + batchEscape(json) + "\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// batchEscape quotes the characters cmd.exe would otherwise act on.
func batchEscape(s string) string {
	r := strings.NewReplacer(
		"^", "^^", "&", "^&", "<", "^<", ">", "^>", "|", "^|", "%", "%%",
	)
	return r.Replace(s)
}

// writeBadProvider writes one that exits non-zero having said nothing
// useful.
func writeBadProvider(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+".cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 1\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSlowProvider writes one that does not return for a long time.
//
// There is no sleep in cmd.exe; a ping to a reserved address with a
// long count is the usual stand-in, and it is what a Windows
// administrator writes.
func writeSlowProvider(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+".cmd")
	body := "@echo off\r\nping -n 60 127.0.0.1 >nul\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
