package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The probe is a batch file, and it is named as one.
//
// A shell script mode 0755 is not a program here: the mode does nothing
// and CreateProcess will not start a file whose extension it does not
// recognise, so this test could not have passed on Windows whatever the
// setting under test did. The name the state calls stays extensionless
// — PATHEXT is what makes `halite-exec-path-probe` resolve to
// `halite-exec-path-probe.cmd`, and that resolution is part of what the
// test is checking.
func writeProbe(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "halite-exec-path-probe.cmd")
	body := "@echo off\r\necho probe-ran\r\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return "halite-exec-path-probe"
}

// searchPath joins directories the way this platform's PATH does.
func searchPath(dirs ...string) string { return strings.Join(dirs, ";") }

// systemDirs are the platform's own program directories, which the
// probe's path is prepended to.
func systemDirs() []string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return []string{root + `\system32`, root}
}
