package builtin

import (
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// sudoFixture is a context whose sudo and visudo answer from a table.
//
// `Lookup` reports both tools present, because every function here
// checks for them first and a fixture that did not would exercise the
// "no sudo on this node" branch by accident.
func sudoFixture(t *testing.T, responses map[string]hexec.Result) (*hexec.Context, *hexec.RecordingRunner) {
	t.Helper()
	if responses == nil {
		responses = map[string]hexec.Result{}
	}
	runner := &hexec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "sudo" || name == "visudo" {
			return "/usr/local/bin/" + name
		}
		return ""
	}
	return c, runner
}

// sudoVersionOutput is what `sudo -V` really printed on this fleet's
// FreeBSD 15.1 host, as root, with sudo 1.9.17p2. Captured rather than
// composed: the line this module reads is buried among the plugin's own
// configuration, and the blank line above it is real.
const sudoVersionOutput = `Sudo version 1.9.17p2
Configure options: --mandir=/usr/local/share/man --sysconfdir=/usr/local/etc --with-pam
Sudoers policy plugin version 1.9.17p2
Sudoers file grammar version 50

Sudoers path: /usr/local/etc/sudoers
Authentication methods: 'pam'
Syslog facility if syslog is being used for logging: authpriv
`

// What an unprivileged caller gets instead: the version lines and
// nothing else. sudo shows the plugin's configuration only to root, so
// the path this module wants is simply absent. Also captured.
const sudoVersionUnprivileged = `Sudo version 1.9.17p2
Sudoers policy plugin version 1.9.17p2
Sudoers file grammar version 50
`

// The sudoers path is read from sudo's own report, not guessed at.
func TestTheSudoersPathComesFromSudoItself(t *testing.T) {
	got, ok := sudoPathFromVersion(sudoVersionOutput)
	if !ok {
		t.Fatal("a real `sudo -V` reported a path and this did not find it")
	}
	if got != "/usr/local/etc/sudoers" {
		t.Errorf("read %q", got)
	}
}

// An unprivileged `sudo -V` has no path in it, and that is reported as
// absent rather than as an empty path.
//
// The distinction matters: an empty path returned as though it were an
// answer would have a tree writing its sudoers to "".
func TestAnUnprivilegedSudoReportsNoPathRatherThanAnEmptyOne(t *testing.T) {
	if got, ok := sudoPathFromVersion(sudoVersionUnprivileged); ok {
		t.Errorf("found a path %q where sudo printed none", got)
	}
}

// The conventional fallback is a real location on every platform this
// module runs on, and nowhere else.
//
// Checked from any host, because a path table written on one platform
// and never exercised on the others is the shape this project keeps
// finding defects in.
func TestEveryPlatformsConventionalSudoersPathIsKnown(t *testing.T) {
	for _, tc := range []struct {
		goos, want string
	}{
		{"freebsd", "/usr/local/etc/sudoers"},
		{"openbsd", "/usr/local/etc/sudoers"},
		{"netbsd", "/usr/local/etc/sudoers"},
		{"dragonfly", "/usr/local/etc/sudoers"},
		{"darwin", "/etc/sudoers"},
		{"linux", "/etc/sudoers"},
	} {
		got, ok := sudoDefaultPath(tc.goos)
		if !ok || got != tc.want {
			t.Errorf("%s: %q (known=%v), want %q", tc.goos, got, ok, tc.want)
		}
	}
	if _, ok := sudoDefaultPath("windows"); ok {
		t.Error("windows was given a sudoers path; this module does not run there")
	}
}

// `sudo -l -U` is read for its rules, and the sentence above them is
// ignored because it is localised and they are not.
func TestTheRulesAreReadAndTheSentenceAboveThemIsNot(t *testing.T) {
	// Captured from this host, as root.
	const out = `User ed may run the following commands on beastie:
    (ALL) ALL
    (ALL) NOPASSWD: /bin/ls
`
	rules := sudoParseList(out)
	if len(rules) != 2 {
		t.Fatalf("read %d rules, want 2: %v", len(rules), rules)
	}
	if rules[0] != "(ALL) ALL" || rules[1] != "(ALL) NOPASSWD: /bin/ls" {
		t.Errorf("read %v", rules)
	}
	for _, r := range rules {
		if strings.HasPrefix(r.(string), "User ") {
			t.Errorf("the sentence was read as a rule: %q", r)
		}
	}
}

// An account with no rules reads as no rules, not as a failure.
func TestAnAccountWithNoRulesReadsAsNone(t *testing.T) {
	if rules := sudoParseList("User nobody is not allowed to run sudo on beastie.\n"); len(rules) != 0 {
		t.Errorf("read %v, want none -- the sentence is not a rule", rules)
	}
}

// An invalid sudoers is an answer, not an error.
//
// A tree asking "is this file valid" needs to branch on the reply. If
// this returned an error the caller could not tell "it is invalid" from
// "visudo could not be run", and those need different responses: one is
// a broken template and the other is a broken node.
func TestAnInvalidSudoersIsAnAnswerRatherThanAFailure(t *testing.T) {
	c, _ := sudoFixture(t, map[string]hexec.Result{
		"visudo -c -f /tmp/bad": {Code: 1, Stderr: "/tmp/bad:1:9: syntax error\n"},
	})
	got, err := sudoValidate(c, "/tmp/bad")
	if err != nil {
		t.Fatalf("an invalid file was reported as a failure: %v", err)
	}
	m := got.(*value.Map)
	valid, _ := m.GetString("valid")
	if valid != false {
		t.Errorf("an invalid file reported valid=%v", valid)
	}
	comment, _ := m.GetString("comment")
	if !strings.Contains(comment.(string), "syntax error") {
		t.Errorf("the reply does not carry visudo's own words: %v", comment)
	}
}

// A valid one says so, and names what it checked.
func TestAValidSudoersSaysWhatWasChecked(t *testing.T) {
	c, _ := sudoFixture(t, map[string]hexec.Result{
		"visudo -c -f /tmp/ok": {Stdout: "/tmp/ok: parsed OK\n"},
	})
	got, err := sudoValidate(c, "/tmp/ok")
	if err != nil {
		t.Fatal(err)
	}
	m := got.(*value.Map)
	if valid, _ := m.GetString("valid"); valid != true {
		t.Errorf("a valid file reported valid=%v", valid)
	}
	if checked, _ := m.GetString("checked"); checked != "/tmp/ok" {
		t.Errorf("checked=%v", checked)
	}
}

// With no path given it checks the node's own sudoers and says so,
// rather than leaving `checked` empty for a caller to misread as "no
// file".
func TestValidatingTheNodesOwnSudoersSaysSo(t *testing.T) {
	c, _ := sudoFixture(t, map[string]hexec.Result{
		"visudo -c": {Stdout: "/usr/local/etc/sudoers: parsed OK\n"},
	})
	got, err := sudoValidate(c, "")
	if err != nil {
		t.Fatal(err)
	}
	checked, _ := got.(*value.Map).GetString("checked")
	if checked != "this node's own sudoers" {
		t.Errorf("checked=%v", checked)
	}
}

// An unknown account is an error, because "may run nothing" and "does
// not exist" are different facts and confusing them would report a
// missing account as a correctly locked-down one.
func TestAnUnknownAccountIsAnErrorNotAnEmptyRuleList(t *testing.T) {
	c, _ := sudoFixture(t, map[string]hexec.Result{
		"sudo -l -U nosuchuser": {Code: 1, Stderr: "sudo: unknown user nosuchuser\n"},
	})
	if _, err := sudoList(c, "nosuchuser"); err == nil {
		t.Fatal("an unknown account returned a rule list")
	} else if !strings.Contains(err.Error(), "no account") {
		t.Errorf("the error does not say the account is missing: %v", err)
	}
}

// A node without sudo reports no version rather than failing.
//
// sudo is not in a base FreeBSD or a minimal Linux, and a tree asking
// "is sudo here" should get "no".
func TestANodeWithoutSudoReportsNoVersion(t *testing.T) {
	c, _ := sudoFixture(t, map[string]hexec.Result{})
	c.Lookup = func(string) string { return "" }
	got, err := sudoVersion(c)
	if err != nil {
		t.Fatalf("a node without sudo failed instead of answering: %v", err)
	}
	if got != "" {
		t.Errorf("version=%q, want empty", got)
	}
}
