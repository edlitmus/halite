package builtin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// sudo.present and sudo.absent against recorded visudo answers. The
// directory is a real temporary one, given as `dir`, because the state
// reads and writes the drop-in itself; only sudo and visudo are recorded.
//
// The visudo outputs are the shapes captured on the two lab hosts on
// 2026-09-30 (FreeBSD 15.1, sudo 1.9.17p2; Debian 13, sudo 1.9.16p2):
// "stdin: parsed OK" for a good `visudo -c -f -`, "stdin:1:30: syntax
// error" and exit 1 for a bad one, and one "<path>: parsed OK" line per
// file for `visudo -c`.

const sudoRule = "nobody ALL=(root) NOPASSWD: /nonexistent/halite-unit\n"

func sudoStateCall(t *testing.T, c *exec.Context, name string, args *value.Map) states.Result {
	t.Helper()
	mod, ok := New().States.Lookup(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	res, err := mod.Fn(c, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// sudoContext records visudo: the text check passes unless told
// otherwise, and the whole-policy check lists the main file and,
// when listed is set, the drop-in.
func sudoContext(t *testing.T, dir, name string, listed bool) *exec.Context {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sudo states are unix-only, and a Windows path is quoted in the recorded command line")
	}
	policy := "/etc/sudoers: parsed OK\n"
	if listed {
		policy += filepath.Join(dir, name) + ": parsed OK\n"
	}
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: map[string]exec.Result{
			"visudo -c -f -": {Stdout: "stdin: parsed OK\n"},
			"visudo -c":      {Stdout: policy},
		}},
		Lookup: func(name string) string { return "/usr/bin/" + name },
	}
}

func TestADropInNameSudoWouldSkipIsRefused(t *testing.T) {
	for _, name := range []string{"admins.conf", "admins~", "../sudoers", "a/b", "", ".."} {
		if err := sudoCheckDropInName(name, true); err == nil {
			t.Errorf("%q was accepted as a drop-in name", name)
		} else if err := states.CommentIsASentence(err.Error()); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	if err := sudoCheckDropInName("halite-admins", true); err != nil {
		t.Errorf("a plain name was refused: %v", err)
	}
	// A name with a dot may still be removed: sudo ignores it, and
	// cleaning one up is a legitimate thing to ask.
	if err := sudoCheckDropInName("old.bak", false); err != nil {
		t.Errorf("removing a dotted name was refused: %v", err)
	}
}

func TestTheIncludeDirIsReadFromBothHostsSudoers(t *testing.T) {
	// The last lines of each host's sudoers, as captured.
	freebsd := "## Read drop-in files from /usr/local/etc/sudoers.d\n@includedir /usr/local/etc/sudoers.d\n"
	debian := "# See sudoers(5) for more information on \"@include\" directives:\n\n@includedir /etc/sudoers.d\n"
	if got := sudoIncludeDirs(freebsd, "/usr/local/etc"); strings.Join(got, ",") != "/usr/local/etc/sudoers.d" {
		t.Errorf("FreeBSD: %v", got)
	}
	if got := sudoIncludeDirs(debian, "/etc"); strings.Join(got, ",") != "/etc/sudoers.d" {
		t.Errorf("Debian: %v", got)
	}
	// The older spelling, a relative directory, and a comment that only
	// mentions the word.
	got := sudoIncludeDirs("#includedir sudoers.d\n# @includedir is how drop-ins work\n#includedirx /no\n", "/etc")
	if strings.Join(got, ",") != "/etc/sudoers.d" {
		t.Errorf("legacy and relative: %v", got)
	}
}

func TestPresentInTestModeChecksTheTextAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := sudoContext(t, dir, "halite-unit", true)
	c.Test = true
	res := sudoStateCall(t, c, "sudo.present", value.MapOf("name", "halite-unit", "contents", sudoRule, "dir", dir))
	if res.Result != nil || !res.HasChanges() {
		t.Fatalf("test mode = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "halite-unit")); err == nil {
		t.Error("test mode wrote the drop-in")
	}
	ran := c.Runner.(*exec.RecordingRunner).Ran
	if len(ran) == 0 || ran[0].String() != "visudo -c -f -" || ran[0].Stdin != sudoRule {
		t.Errorf("the text was not handed to visudo on standard input first: %v", ran)
	}
}

func TestPresentRefusesTextVisudoRejectsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := sudoContext(t, dir, "halite-unit", true)
	c.Runner.(*exec.RecordingRunner).Responses["visudo -c -f -"] = exec.Result{
		Code: 1, Stderr: "stdin:1:30: syntax error\nnobody ALL=(root) NOPASSWD /x\n                             ^\n"}
	res := sudoStateCall(t, c, "sudo.present", value.MapOf("name", "halite-unit", "contents", "nobody ALL=(root) NOPASSWD /x", "dir", dir))
	if !res.Failed() || !strings.Contains(res.Comment, "syntax error") {
		t.Fatalf("result = %+v, want a refusal carrying visudo's words", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "halite-unit")); err == nil {
		t.Error("rejected text was written")
	}
}

// What the remaining tests need is a chown to root, so they run where
// the suite does as root: the lab hosts, and the fleet legs.
func sudoNeedsRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() != 0 {
		t.Skip("sudo.present makes its drop-in root's, which needs root")
	}
}

func TestPresentWritesARootOwned0440DropInAndThenLeavesItAlone(t *testing.T) {
	sudoNeedsRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "halite-unit")
	c := sudoContext(t, dir, "halite-unit", true)
	res := sudoStateCall(t, c, "sudo.present", value.MapOf("name", "halite-unit", "contents", strings.TrimSuffix(sudoRule, "\n"), "dir", dir))
	if res.Failed() || res.Result == nil || !res.HasChanges() {
		t.Fatalf("apply = %+v", res)
	}
	data, mode, existed, err := sudoReadDropIn(path)
	if err != nil || !existed || string(data) != sudoRule || mode != "root:0:0440" {
		t.Fatalf("drop-in = %q %s %v %v, want the rule with its newline, root:0:0440", data, mode, existed, err)
	}
	again := sudoStateCall(t, sudoContext(t, dir, "halite-unit", true), "sudo.present",
		value.MapOf("name", "halite-unit", "contents", sudoRule, "dir", dir))
	if again.Result == nil || !*again.Result || again.HasChanges() {
		t.Errorf("a second run = %+v, want success with no change", again)
	}
}

// visudo -c not listing the file means sudo does not read it, and the
// state takes it back out rather than report a rule that has no effect.
func TestPresentTakesTheDropInBackOutWhenVisudoDoesNotReadIt(t *testing.T) {
	sudoNeedsRoot(t)
	dir := t.TempDir()
	c := sudoContext(t, dir, "halite-unit", false)
	res := sudoStateCall(t, c, "sudo.present", value.MapOf("name", "halite-unit", "contents", sudoRule, "dir", dir))
	if !res.Failed() || !strings.Contains(res.Comment, "did not list it") {
		t.Fatalf("result = %+v, want a failure saying sudo does not read it", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "halite-unit")); err == nil {
		t.Error("the drop-in was left behind")
	}
}

func TestPresentPutsThePreviousTextBackWhenThePolicyFails(t *testing.T) {
	sudoNeedsRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "halite-unit")
	if err := sudoWriteDropIn(path, []byte(sudoRule)); err != nil {
		t.Fatal(err)
	}
	c := sudoContext(t, dir, "halite-unit", true)
	rec := c.Runner.(*exec.RecordingRunner)
	// The policy passes before the write and fails, about another file,
	// after it. A recorder answers by command line alone, so the two
	// `visudo -c` calls are told apart by order here.
	calls := 0
	listing := filepath.Join(dir, "halite-unit") + ": parsed OK\n"
	c.Runner = runnerFor(func(cmd exec.Command) (exec.Result, error) {
		if cmd.String() == "visudo -c" {
			calls++
			if calls == 2 {
				return exec.Result{Code: 1, Stdout: listing, Stderr: "/etc/sudoers.d/other:1:7: syntax error"}, nil
			}
		}
		return rec.Run(context.Background(), cmd)
	})
	res := sudoStateCall(t, c, "sudo.present", value.MapOf("name", "halite-unit", "contents", "Defaults:nobody !lecture\n", "dir", dir))
	if !res.Failed() || !strings.Contains(res.Comment, "taken back out") {
		t.Fatalf("result = %+v", res)
	}
	if calls != 2 {
		t.Errorf("visudo -c ran %d times, want twice: before the write and after it", calls)
	}
	if data, _, _, _ := sudoReadDropIn(path); string(data) != sudoRule {
		t.Errorf("the previous text was not put back: %q", data)
	}
}

// A policy visudo already rejects for another file's reason is not made
// worse by a drop-in it parses, and the write stands, with a warning. The
// other file's line is the one a GitHub ubuntu-24.04 runner's `visudo -c`
// printed in the first fleet run: its /etc/sudoers.d/runner has a mode
// visudo rejects and sudo reads.
func TestPresentStandsWhenThePolicyFailsExactlyAsBeforeForAnotherFile(t *testing.T) {
	sudoNeedsRoot(t)
	dir := t.TempDir()
	runner := "/etc/sudoers.d/runner: bad permissions, should be mode 0440"
	before := "/etc/sudoers: parsed OK\n/etc/sudoers.d/README: parsed OK\n" + runner
	after := "/etc/sudoers: parsed OK\n/etc/sudoers.d/README: parsed OK\n" +
		filepath.Join(dir, "halite-unit") + ": parsed OK\n" + runner
	worse := after + "\n/etc/sudoers.d/zz: bad permissions, should be mode 0440"
	for label, tc := range map[string]struct {
		afterSaid string
		stands    bool
	}{
		"the same complaint, about the same file": {after, true},
		"a new complaint as well":                 {worse, false},
	} {
		calls := 0
		c := sudoContext(t, dir, "halite-unit", true)
		rec := c.Runner.(*exec.RecordingRunner)
		c.Runner = runnerFor(func(cmd exec.Command) (exec.Result, error) {
			if cmd.String() == "visudo -c" {
				calls++
				if calls == 1 {
					return exec.Result{Code: 1, Stdout: before}, nil
				}
				return exec.Result{Code: 1, Stdout: tc.afterSaid}, nil
			}
			return rec.Run(context.Background(), cmd)
		})
		res := sudoStateCall(t, c, "sudo.present", value.MapOf("name", "halite-unit", "contents", sudoRule, "dir", dir))
		_, statErr := os.Stat(filepath.Join(dir, "halite-unit"))
		if tc.stands {
			if res.Failed() || !res.HasChanges() || len(res.Warnings) == 0 || statErr != nil {
				t.Errorf("%s: %+v (file: %v), want the write to stand with a warning", label, res, statErr)
			}
		} else if !res.Failed() || statErr == nil {
			t.Errorf("%s: %+v (file: %v), want it taken back out", label, res, statErr)
		}
		_ = os.Remove(filepath.Join(dir, "halite-unit"))
	}
}

func TestAbsentRemovesTheDropInAndIsThenSatisfied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halite-unit")
	if err := os.WriteFile(path, []byte(sudoRule), 0o440); err != nil {
		t.Fatal(err)
	}
	c := sudoContext(t, dir, "halite-unit", true)
	c.Test = true
	res := sudoStateCall(t, c, "sudo.absent", value.MapOf("name", "halite-unit", "dir", dir))
	if res.Result != nil || !res.HasChanges() {
		t.Fatalf("test mode = %+v", res)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("test mode removed the drop-in")
	}
	c.Test = false
	if res = sudoStateCall(t, c, "sudo.absent", value.MapOf("name", "halite-unit", "dir", dir)); !res.HasChanges() || res.Failed() {
		t.Fatalf("apply = %+v", res)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the drop-in is still there")
	}
	if res = sudoStateCall(t, c, "sudo.absent", value.MapOf("name", "halite-unit", "dir", dir)); res.HasChanges() || res.Failed() {
		t.Errorf("a second run = %+v", res)
	}
}

func TestSudoListsFileReadsVisudosOwnListing(t *testing.T) {
	// Debian 13's `visudo -c` with a drop-in in place, as captured.
	said := "/etc/sudoers: parsed OK\n/etc/sudoers.d/README: parsed OK\n/etc/sudoers.d/halite-cap-a: parsed OK"
	if !sudoListsFile(said, "/etc/sudoers.d/halite-cap-a") {
		t.Error("a listed drop-in was not found")
	}
	if sudoListsFile(said, "/etc/sudoers.d/halite-cap") {
		t.Error("a prefix of a listed name was taken for it")
	}
}
