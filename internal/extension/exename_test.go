package extension

import (
	"os"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm"
)

// exeName is what a test binary must be called for this platform to run
// it.
//
// Windows decides what a file is by its extension: CreateProcess will
// not start `echoext`, and Go reports that as `executable file not found
// in %PATH%` naming an absolute path — which reads as a bug in halite
// rather than as a file that was named wrong. A manifest naming such a
// file is now refused when the bundle is built, so these fixtures have
// to name one the platform can start.
func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// assertBundleExecutable checks that an installed executable carries the
// permissions the signed manifest asked for.
//
// 0700 asks for two things: runnable, and reachable by nobody else. Only
// the first has a counterpart on every platform, so each is checked
// where it means something.
func assertBundleExecutable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0o100 == 0 {
			t.Errorf("the executable is mode %o", info.Mode().Perm())
		}
		return
	}
	// Runnable here is decided by the name, which Manifest.Check
	// enforces at build time. What is left to check is that the binary
	// a node is about to run is not one any account on the machine
	// could have rewritten.
	others, err := fileperm.Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("the installed executable can be reached by %v", others)
	}
}
