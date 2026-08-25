package sshexec

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/roster"
)

// What else arrives on a target's stdout: a login banner, a motd, a
// sudo lecture, a shell profile that echoes something.
func TestTheReturnIsFoundAmongTheTargetsOwnOutput(t *testing.T) {
	stdout := strings.Join([]string{
		"Welcome to production. Unauthorised access is prohibited.",
		"You have mail.",
		begin,
		`{"success":true}`,
		end,
	}, "\n")
	got, ok := Unframe(stdout)
	if !ok {
		t.Fatal("the return was not found")
	}
	if got != `{"success":true}` {
		t.Errorf("it read %q", got)
	}
}

// A banner that happens to contain the marker is a banner; the return
// is what the binary wrote last.
func TestTheLastFrameWins(t *testing.T) {
	stdout := strings.Join([]string{
		begin,
		"a banner quoting the marker",
		end,
		begin,
		`{"success":true}`,
		end,
	}, "\n")
	got, ok := Unframe(stdout)
	if !ok {
		t.Fatal("the return was not found")
	}
	if got != `{"success":true}` {
		t.Errorf("it read %q", got)
	}
}

// A begin with no end is a run that was cut off — killed, or the
// connection dropped. Reporting the partial text would be reporting a
// truncated object as an answer.
func TestATruncatedFrameIsNotAReturn(t *testing.T) {
	if _, ok := Unframe(begin + "\n{\"succ"); ok {
		t.Error("a truncated frame was read as a return")
	}
	if _, ok := Unframe("no frame at all"); ok {
		t.Error("output with no frame was read as a return")
	}
	if _, ok := Unframe(""); ok {
		t.Error("empty output was read as a return")
	}
}

// The one command that cannot go over stdin carries these values, and
// the POSIX escape for a quote does not survive every login shell.
func TestAnUnsafeValueIsRefusedRatherThanEscaped(t *testing.T) {
	safe := []string{"/var/tmp/halite-thin", "root", "/usr/local/bin:/usr/bin", "a.b-c_d"}
	for _, v := range safe {
		if err := shellSafe("thin_dir", v); err != nil {
			t.Errorf("%q was refused: %v", v, err)
		}
	}
	unsafe := []string{"/var/tmp/a dir", "root; rm -rf /", "$(whoami)", "`id`", "a'b", `a"b`, "a\nb"}
	for _, v := range unsafe {
		if err := shellSafe("thin_dir", v); err == nil {
			t.Errorf("%q was accepted", v)
		}
	}
}

// The invocation is what runs on the target, and it is wrapped so the
// script is POSIX sh whatever the login shell is.
func TestTheInvocationIsWrappedInPOSIXShell(t *testing.T) {
	o := &Options{}
	target := roster.Target{ID: "t", ThinDir: "/var/tmp/halite-thin"}

	command, err := o.invocation(target, "/var/tmp/halite-thin/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(command, "/bin/sh -c '") {
		t.Errorf("the invocation is %q", command)
	}
	if !strings.Contains(command, "oneshot") {
		t.Errorf("the invocation does not run oneshot: %q", command)
	}

	// sudo never prompts: a run that hangs waiting for a password
	// nobody will type is worse than one that fails and says so.
	target.Sudo, target.SudoUser = true, "postgres"
	command, err = o.invocation(target, "/var/tmp/halite-thin/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "sudo -n -u postgres") {
		t.Errorf("the sudo invocation is %q", command)
	}

	// And an unsafe value is refused rather than escaped.
	target.SudoUser = "post gres"
	if _, err := o.invocation(target, "/var/tmp/halite-thin/abc"); err == nil {
		t.Error("an unsafe sudo_user was accepted")
	}
}

// `exit status 255` is ssh's way of saying anything at all went wrong,
// and on its own it sends an operator nowhere.
func TestATargetsOwnWordsReachTheError(t *testing.T) {
	err := enrich(errString("exit status 255"), "ssh: connect to host db1 port 22: No route to host")
	if !strings.Contains(err.Error(), "No route to host") {
		t.Errorf("the error is %v", err)
	}
	// And an empty stderr leaves the original alone rather than adding
	// an empty parenthesis.
	bare := enrich(errString("exit status 1"), "   \n ")
	if bare.Error() != "exit status 1" {
		t.Errorf("the error is %v", bare)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
