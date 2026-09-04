package bridge

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// exeName is what a test binary must be called for this platform to run
// it. Windows decides what a file is by its extension.
func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// systemRootForTest is the one variable a Windows process cannot start
// without. A test that sets Env explicitly has to include it.
func systemRootForTest() string {
	if v := os.Getenv("SystemRoot"); v != "" {
		return v
	}
	return `C:\Windows`
}

// startEchoErr is startEcho for a case where the failure is the subject.
func startEchoErr(t *testing.T, adjust func(*Options)) (*Process, error) {
	t.Helper()
	opts := Options{
		Path: echoExtension(t), Kind: "module",
		WorkDir: t.TempDir(), Timeout: 10 * time.Second,
	}
	if adjust != nil {
		adjust(&opts)
	}
	return Start(context.Background(), opts)
}
