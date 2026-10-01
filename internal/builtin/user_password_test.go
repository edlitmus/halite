package builtin

import (
	"runtime"
	"strings"
	"testing"
)

// The hash must never reach an argument vector. `usermod -p <hash>` puts
// it in the process table, where any unprivileged account on the machine
// can read it for as long as the command runs, and a state that sets a
// password is by definition running somewhere that matters.
func TestAPasswordHashNeverReachesTheArgumentVector(t *testing.T) {
	const hash = "$6$rounds=5000$abcdefgh$SeCrEtHaShVaLuE"

	for _, platform := range []string{"freebsd", "linux"} {
		cmd, err := passwordCommand(platform, "ed", hash)
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
		for _, arg := range cmd.Argv {
			if strings.Contains(arg, hash) {
				t.Errorf("%s: the hash is in the argument vector: %v", platform, cmd.Argv)
			}
		}
		if !strings.Contains(cmd.Stdin, hash) {
			t.Errorf("%s: the hash should be written to standard input, got %q", platform, cmd.Stdin)
		}
		if !strings.HasSuffix(cmd.Stdin, "\n") {
			t.Errorf("%s: the input needs a terminating newline, got %q", platform, cmd.Stdin)
		}
	}

	// The two tools take it differently, and getting either wrong writes
	// the wrong thing into the password database.
	bsd, _ := passwordCommand("freebsd", "ed", hash)
	if strings.Join(bsd.Argv, " ") != "pw usermod -n ed -H 0" {
		t.Errorf("freebsd argv = %v", bsd.Argv)
	}
	if bsd.Stdin != hash+"\n" {
		t.Errorf("pw reads the bare hash, got %q", bsd.Stdin)
	}

	linux, _ := passwordCommand("linux", "ed", hash)
	if strings.Join(linux.Argv, " ") != "chpasswd -e" {
		t.Errorf("linux argv = %v", linux.Argv)
	}
	if linux.Stdin != "ed:"+hash+"\n" {
		t.Errorf("chpasswd reads name:hash, got %q", linux.Stdin)
	}

	// A platform this build has no answer for says so rather than
	// silently doing nothing, which would leave an account with no
	// password and a state reporting success.
	if _, err := passwordCommand("windows", "ed", hash); err == nil {
		t.Error("an unsupported platform should be an error")
	}
}

// currentHash reads a root-only file. Unprivileged, it must say that
// rather than report the account as having no password, which would make
// every run set the password again and never converge.
func TestCurrentHashSaysWhenItCannotRead(t *testing.T) {
	_, _, err := currentHash("root")
	if err == nil {
		t.Skip("this test runs unprivileged; as root the read succeeds")
	}
	// What is needed differs by platform: root on unix, and on Windows
	// nothing, because there is no readable hash at any privilege. Both
	// have to say which, or the caller cannot tell "you need to be root"
	// from "this cannot be answered here".
	want := "root"
	if runtime.GOOS == "windows" {
		want = "SAM"
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("the error should say what is needed: %v", err)
	}
}

// chpasswd reads `name:hash` records, one per line, so a hash or a name
// carrying a line break is a second record — for any account the text
// names — and a colon is a field boundary inside the first. passwordCommand
// is the one path `user.present` and `shadow.set_password` both reach, so
// it is where that is refused: `shadow` had its own check and
// `user.present` had none, and a `password:` written as
// "<hash>\nroot:<hash>" set root's password from a state about another
// account.
func TestAPasswordRecordCannotCarryASecondRecord(t *testing.T) {
	const hash = "$6$rounds=5000$abcdefgh$SeCrEtHaShVaLuE"
	cases := map[string][2]string{
		"a line break in the hash": {"alice", hash + "\nroot:" + hash},
		"a carriage return":        {"alice", hash + "\rroot:" + hash},
		"a colon in the hash":      {"alice", "$6$salt$x:0:99999"},
		"a control character":      {"alice", hash + "\x00"},
		"a line break in the name": {"alice\nroot", hash},
		"a colon in the name":      {"root:alice", hash},
	}
	for _, platform := range []string{"linux", "freebsd"} {
		for what, c := range cases {
			if cmd, err := passwordCommand(platform, c[0], c[1]); err == nil {
				t.Errorf("%s, %s: accepted, and would send %q", platform, what, cmd.Stdin)
			}
		}
		if _, err := passwordCommand(platform, "alice", hash); err != nil {
			t.Errorf("%s: an ordinary hash was refused: %v", platform, err)
		}
	}
}
