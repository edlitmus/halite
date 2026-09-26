package builtin

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// These states rewrite the whole file to change one line in it, so what
// matters most about them is what they leave alone.
//
// Nothing tested that. Every `ssh_auth` and `ssh_known_hosts` test in this
// package started from a file that did not exist, so no test ever had
// another account's key to lose -- and `ssh_auth.present` rewritten to
// replace the file rather than append to it passed the entire package,
// conformance harness included. Found by breaking it on purpose while
// writing the conformance cases: the harness cannot see it, because a state
// that destroys the file once is then perfectly idempotent about it.
//
// The consequence is not abstract. `ssh_auth.present` is how this estate's
// accounts get their keys, and a state that drops the others locks every
// other operator out of four hosts on the next highstate. DIVERGENCE 5.157.

// The other key, and the furniture around it that a rewrite can also lose:
// `authKey.Raw` exists to carry a comment or a blank line verbatim, which is
// a claim worth holding.
const (
	otherAuthKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG90aGVya2V5dGhhdG11c3RzdXJ2aXZl other@host"
	newAuthBlob  = "AAAAC3NzaC1lZDI1NTE5AAAAIG5ld2tleWJlaW5nYWRkZWRieXRoaXNzdGF0ZQ"
)

func TestSSHAuthPresentKeepsTheOtherKeys(t *testing.T) {
	r := New()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	before := "# the ops team's keys\n" + otherAuthKey + "\n\n"
	writeFileForTest(t, keys, before, 0o600)

	res := run(t, r, "ssh_auth.present", value.MapOf(
		"name", newAuthBlob, "enc", "ssh-ed25519", "comment", "new@host", "config", keys), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("adding a key should be a change: %+v", res)
	}

	got := readFile(t, keys)
	if !strings.Contains(got, otherAuthKey) {
		t.Errorf("the other account's key was lost, which is a lockout:\n%s", got)
	}
	if !strings.Contains(got, newAuthBlob) {
		t.Errorf("the new key was not added:\n%s", got)
	}
	if !strings.Contains(got, "# the ops team's keys") {
		t.Errorf("the comment line was lost, and `authKey.Raw` exists to keep it:\n%s", got)
	}
}

func TestSSHAuthAbsentRemovesOnlyTheNamedKey(t *testing.T) {
	r := New()
	keys := filepath.Join(t.TempDir(), "authorized_keys")
	writeFileForTest(t, keys,
		"ssh-ed25519 "+newAuthBlob+" new@host\n"+otherAuthKey+"\n", 0o600)

	res := run(t, r, "ssh_auth.absent", value.MapOf("name", newAuthBlob, "config", keys), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("removing a present key should be a change: %+v", res)
	}

	got := readFile(t, keys)
	if strings.Contains(got, newAuthBlob) {
		t.Errorf("the named key survived:\n%s", got)
	}
	if !strings.Contains(got, otherAuthKey) {
		t.Errorf("absent took the other account's key with it:\n%s", got)
	}
}

const (
	otherKnownHost = "other.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG90aGVyaG9zdGtleXRoYXRtdXN0bGl2ZQ"
	newHostKey     = "AAAAC3NzaC1lZDI1NTE5AAAAIG5ld2hvc3RrZXliZWluZ2FkZGVkbm93AAAAAAA"
)

func TestSSHKnownHostsPresentKeepsTheOtherHosts(t *testing.T) {
	r := New()
	known := filepath.Join(t.TempDir(), "known_hosts")
	// A hashed entry as well, because the code says one "is not readable,
	// and rewriting the file must not drop it" -- a claim nothing checked.
	hashed := "|1|SGFzaGVkSG9zdE5hbWVIZXJlPT0=|SGFzaGVkU2FsdEhlcmU9PQ== ssh-rsa AAAAB3NzaC1yc2FoYXNoZWRlbnRyeQ"
	writeFileForTest(t, known, otherKnownHost+"\n"+hashed+"\n", 0o644)

	res := run(t, r, "ssh_known_hosts.present", value.MapOf(
		"name", "new.example.com", "key", newHostKey, "enc", "ssh-ed25519", "config", known), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("adding a host key should be a change: %+v", res)
	}

	got := readFile(t, known)
	if !strings.Contains(got, otherKnownHost) {
		t.Errorf("the other host was lost:\n%s", got)
	}
	if !strings.Contains(got, hashed) {
		t.Errorf("the hashed entry was dropped, which the code says it must not be:\n%s", got)
	}
	if !strings.Contains(got, newHostKey) {
		t.Errorf("the new host key was not added:\n%s", got)
	}
}

func TestSSHKnownHostsAbsentRemovesOnlyTheNamedHost(t *testing.T) {
	r := New()
	known := filepath.Join(t.TempDir(), "known_hosts")
	writeFileForTest(t, known,
		"new.example.com ssh-ed25519 "+newHostKey+"\n"+otherKnownHost+"\n", 0o644)

	res := run(t, r, "ssh_known_hosts.absent", value.MapOf(
		"name", "new.example.com", "config", known), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("removing a present host should be a change: %+v", res)
	}

	got := readFile(t, known)
	if strings.Contains(got, newHostKey) {
		t.Errorf("the named host survived:\n%s", got)
	}
	if !strings.Contains(got, otherKnownHost) {
		t.Errorf("absent took the other host with it:\n%s", got)
	}
}

// writeFileForTest puts a starting file in place, failing the test rather
// than the state if it cannot.
func writeFileForTest(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// The git binary is a requirement, not a hope.
//
// Five tests in this package skip without it -- four `TestGitLatest*` and
// the `git.latest` conformance case -- and a skip is invisible: no CI leg
// here runs `go test -v`, so `ok internal/builtin` is printed whether those
// five ran or silently did not. On the FreeBSD leg, which is tier 1 and
// carries about 80% of this estate, the virtual machine installs a pinned Go
// toolchain and nothing else, so whether git is there was a question nobody
// could answer from the output.
//
// This makes it answerable. It is not a new requirement: `VERSION` comes
// from `git describe` in the Makefile, so a machine that can build halite
// has git, and one that cannot should be told which tests it is not running
// rather than left to assume they passed.
func TestTheGitBinaryIsPresentSoTheGitTestsRun(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Fatalf("no git on this machine, so five tests in this package skipped "+
			"silently -- four TestGitLatest* and the git.latest conformance case. "+
			"The Makefile derives VERSION from `git describe`, so this is already a "+
			"build requirement; install git, or know that git.latest is unexercised "+
			"here. LookPath said: %v", err)
	}
}
