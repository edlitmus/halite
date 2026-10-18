//go:build unix

package builtin

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **The file a check_cmd reads is not world-readable** (issue 321,
// section 5). It was chmod 0644 before the checker ran, so the pending
// contents of a 0600 target -- a sudoers fragment, a key, a password file
// -- could be read from /tmp by any account while the checker ran. The
// checker here prints the mode it was handed and fails, so the comment
// carries what it saw.
func TestTheCheckCmdFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	res := runCheck(t, New(), "file.managed", value.MapOf(
		"name", path, "contents", "hunter2\n", "mode", "0600",
	), []string{`sh -c 'ls -ln "$1"; exit 1' check`}, false)
	if res.Succeeded() {
		t.Fatalf("the checker was meant to fail: %q", res.Comment)
	}
	if !strings.Contains(res.Comment, "-rw-------") {
		t.Errorf("the checker saw a file that is not 0600:\n%s", res.Comment)
	}
}
