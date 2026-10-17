//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **setuid, setgid and sticky are a mode's too** (issue 321, M-23).
// parseMode returned the raw number, and Go keeps those three as flags
// outside the permission bits, so `mode: '4755'` set 0755 and reported
// it done; and since only Perm() was compared, a file that had setuid was
// "already in the requested state" at '0755' and kept the bit.
func TestSetuidIsSetAndCleared(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	apply := func(mode string) bool {
		t.Helper()
		res, err := r.States.Call(newCtx(false), "file.managed",
			value.MapOf("name", path, "contents", "#!/bin/sh\n", "mode", mode))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || !*res.Result {
			t.Fatalf("mode %s: %s", mode, res.Comment)
		}
		return res.HasChanges()
	}
	modeOf := func() os.FileMode {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode()
	}

	if !apply("4755") {
		t.Fatal("'4755' on a 0755 file changed nothing")
	}
	if m := modeOf(); m&os.ModeSetuid == 0 || m.Perm() != 0o755 {
		t.Fatalf("after '4755' the mode is %s", m)
	}
	if apply("4755") {
		t.Error("a second '4755' changed something again")
	}

	if !apply("0755") {
		t.Fatal("'0755' on a setuid file changed nothing, so the bit stays")
	}
	if m := modeOf(); m&os.ModeSetuid != 0 {
		t.Errorf("after '0755' the file still has setuid: %s", m)
	}
}

// The sticky bit, on the directory where it means something.
func TestStickyIsSetOnADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "drop")
	res, err := New().States.Call(newCtx(false), "file.directory", value.MapOf("name", dir, "mode", "1777"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("file.directory: %s", res.Comment)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0o777 {
		t.Errorf("'1777' made %s", info.Mode())
	}
}

func TestParseModeTranslatesTheSpecialDigit(t *testing.T) {
	for _, s := range []string{"4755", "2750", "1777", "6755", "0644", "7777"} {
		m, err := parseMode(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatMode(m); got != s {
			t.Errorf("%s round-tripped as %s", s, got)
		}
	}
	if _, err := parseMode("17777"); err == nil {
		t.Error("17777 was taken as a mode")
	}
}

// **A created directory has the mode asked for, whatever the umask**
// (issue 321, M-31). os.Mkdir's mode is less the umask, and nothing set
// it afterwards: under a service manager's 027, `mode: '0775'` came out
// 0750 on the run that created it and was "updated" by the next. The
// parents makedirs creates get dir_mode exactly too.
func TestADirectoryIsCreatedWithItsModeUnderAUmask(t *testing.T) {
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)
	base := t.TempDir()
	dir := filepath.Join(base, "a", "b", "leaf")
	args := value.MapOf("name", dir, "mode", "0775", "makedirs", true, "dir_mode", "0755")
	r := New()
	res, err := r.States.Call(newCtx(false), "file.directory", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("file.directory: %s", res.Comment)
	}
	for path, want := range map[string]os.FileMode{dir: 0o775, filepath.Join(base, "a"): 0o755, filepath.Join(base, "a", "b"): 0o755} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s is %o under umask 027, want %o", path, info.Mode().Perm(), want)
		}
	}
	res, err = r.States.Call(newCtx(false), "file.directory", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run changed something: %s", res.Comment)
	}
}
