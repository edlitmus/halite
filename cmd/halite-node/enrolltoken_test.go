package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Issue 321, section 5: the enrollment token was accepted only as
// `--token <secret>`, so for as long as `halite-node enroll` ran it was in
// the process table, readable by every account on the node. Measured on
// this Linux host, `ps -eo pid,user,args` printed
// `halite-node enroll --hub … --token=SECRETTOKENVALUE123` to an ordinary
// user. A file the node reads is not in the table.

func tokenFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestATokenFileIsReadAndTrimmed(t *testing.T) {
	path := tokenFile(t, "vh9exVfcVbIBGlTzLZKNPdVqqlMqYkvW613SUZjaQCE\n", 0o600)
	got, warning, err := readTokenFile(path)
	if err != nil || got != "vh9exVfcVbIBGlTzLZKNPdVqqlMqYkvW613SUZjaQCE" {
		t.Fatalf("token = %q, %v", got, err)
	}
	if warning != "" {
		t.Errorf("a 0600 file was warned about: %s", warning)
	}
}

// A token is random base64 and can begin with `-`; in a file that is no
// problem, which is one of the reasons to prefer one.
func TestATokenFileMayHoldATokenThatBeginsWithAHyphen(t *testing.T) {
	got, _, err := readTokenFile(tokenFile(t, "-abc123\n", 0o600))
	if err != nil || got != "-abc123" {
		t.Fatalf("token = %q, %v", got, err)
	}
}

func TestAnEmptyTokenFileIsRefused(t *testing.T) {
	for _, body := range []string{"", "\n", "  \n\t\n"} {
		if _, _, err := readTokenFile(tokenFile(t, body, 0o600)); err == nil ||
			!strings.Contains(err.Error(), "empty") {
			t.Errorf("a token file holding %q gave %v, want a refusal saying it is empty", body, err)
		}
	}
}

func TestAMissingTokenFileNamesThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if _, _, err := readTokenFile(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want one naming %s", err, path)
	}
}

// Readable by others is the exposure the file was meant to avoid, moved
// from the process table to the disk. It is a warning and not a refusal:
// a token is single-use and short-lived (SPEC 7.3), and an rc.conf-style
// file made by a provisioning tool is often 0644.
func TestAWorldReadableTokenFileIsWarnedAbout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a mode has no meaning on Windows")
	}
	_, warning, err := readTokenFile(tokenFile(t, "abc\n", 0o644))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "readable") {
		t.Errorf("no warning for a 0644 token file: %q", warning)
	}
}
