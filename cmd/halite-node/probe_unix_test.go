//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeProbe writes a program that prints "probe-ran", named so that
// nothing else on the machine could resolve it, in the form this
// platform runs.
func writeProbe(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "halite-exec-path-probe")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho probe-ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "halite-exec-path-probe"
}

// searchPath joins directories the way this platform's PATH does.
func searchPath(dirs ...string) string {
	out := ""
	for i, d := range dirs {
		if i > 0 {
			out += ":"
		}
		out += d
	}
	return out
}

// systemDirs are the platform's own program directories, which the
// probe's path is prepended to.
func systemDirs() []string { return []string{"/usr/bin", "/bin"} }
