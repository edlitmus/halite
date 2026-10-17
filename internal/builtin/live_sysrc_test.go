package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// **Against the real sysrc, a broken rc.conf is an error, not an unset
// variable** (issue 321, section 5). A scratch file, never /etc/rc.conf:
// one that sets foo_enable and then breaks a quote. FreeBSD only; no root
// needed, since the file is the test's own.
func TestLiveSysrcTellsUnsetFromABrokenFile(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to run the real sysrc")
	}
	if runtime.GOOS != "freebsd" {
		t.Skipf("sysrc is FreeBSD's, and this is %s", runtime.GOOS)
	}
	c := &exec.Context{Runner: &exec.OSRunner{}}
	dir := t.TempDir()
	good := filepath.Join(dir, "rc.conf")
	if err := os.WriteFile(good, []byte("foo_enable=\"YES\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, set, err := sysrcGet(c, "foo_enable", good); err != nil || !set || v != "YES" {
		t.Errorf("foo_enable in a good file: %q set=%v err=%v", v, set, err)
	}
	if _, set, err := sysrcGet(c, "bar_enable", good); err != nil || set {
		t.Errorf("an unset variable: set=%v err=%v", set, err)
	}
	broken := filepath.Join(dir, "broken.conf")
	if err := os.WriteFile(broken, []byte("foo_enable=\"YES\"\nbad=\"unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, set, err := sysrcGet(c, "foo_enable", broken)
	if err == nil {
		t.Fatalf("a broken file was read as foo_enable unset (set=%v)", set)
	}
	if !strings.Contains(err.Error(), "Syntax error") {
		t.Errorf("the error does not carry sysrc's reason: %v", err)
	}
}
