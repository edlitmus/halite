package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every lsattr line here is real output of `lsattr -d -- <path>`, captured
// on 2026-09-29/30 as root on ext4 on two lab instances: Rocky Linux 9.8
// with e2fsprogs 1.46.5, whose flag column is 22 characters, and AlmaLinux
// 8.10 with e2fsprogs 1.45.6, whose column is 20 -- for the same file, on
// the same filesystem type, in the same state.
const (
	lsattrPlainRocky9     = "--------------e------- /root/rpmchattr-cap/f\n"
	lsattrPlainAlma8      = "--------------e----- /root/rpmchattr-cap/f\n"
	lsattrImmutableRocky9 = "----ia--------e------- /root/rpmchattr-cap/f\n"
	lsattrImmutableAlma8  = "----ia--------e----- /root/rpmchattr-cap/f\n"
	// `chattr +dsuAS` on each, then `lsattr`.
	lsattrManyRocky9 = "suS---dA------e------- /root/rcap/plain\n"
	lsattrManyAlma8  = "suS---dA------e----- /root/rcap/plain\n"
	// `chattr +c` then `+x` on Rocky 9.8: both exit 0 and lsattr shows
	// both set. The `-e` form is chattr -e having really cleared extents.
	lsattrCompressDaxRocky9 = "--------c-----e-x----- /root/rcap/plain\n"
	lsattrNoExtentsAlma8    = "-------------------- /root/rcap/plain\n"
	lsattrSpaceAlma8        = "--------------e----- /root/rcap/with space\n"
)

func TestParseLsattrIgnoresTheColumnWidth(t *testing.T) {
	cases := []struct {
		name, path, out, want string
	}{
		{"plain 1.46.5", "/root/rpmchattr-cap/f", lsattrPlainRocky9, "e"},
		{"plain 1.45.6", "/root/rpmchattr-cap/f", lsattrPlainAlma8, "e"},
		{"+ia 1.46.5", "/root/rpmchattr-cap/f", lsattrImmutableRocky9, "aei"},
		{"+ia 1.45.6", "/root/rpmchattr-cap/f", lsattrImmutableAlma8, "aei"},
		{"+dsuAS 1.46.5", "/root/rcap/plain", lsattrManyRocky9, "ASdesu"},
		{"+dsuAS 1.45.6", "/root/rcap/plain", lsattrManyAlma8, "ASdesu"},
		{"+c +x 1.46.5", "/root/rcap/plain", lsattrCompressDaxRocky9, "cex"},
		{"-e 1.45.6", "/root/rcap/plain", lsattrNoExtentsAlma8, ""},
		{"a space in the path", "/root/rcap/with space", lsattrSpaceAlma8, "e"},
	}
	for _, tc := range cases {
		got, err := parseLsattr(tc.path, tc.out)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: letters = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseLsattrRefusesALineForAnotherPath(t *testing.T) {
	if _, err := parseLsattr("/root/rcap", lsattrSpaceAlma8); err == nil {
		t.Error("an answer about a different path was accepted")
	}
}

func TestChattrLettersAreValidated(t *testing.T) {
	if got, err := validateChattrLetters("iai"); err != nil || got != "ia" {
		t.Errorf("iai = %q, %v", got, err)
	}
	for _, bad := range []string{"", "e", "ie", "Q", "i "} {
		if _, err := validateChattrLetters(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := validateChattrLetters("e"); err == nil || !strings.Contains(err.Error(), "extents") {
		t.Errorf("e refused with %v, want the reason", err)
	}
}

func chattrTestContext(responses map[string]exec.Result, test bool) (*exec.Context, *exec.RecordingRunner) {
	runner := &exec.RecordingRunner{Responses: responses}
	return &exec.Context{
		Runner: runner,
		Lookup: func(name string) string { return "/usr/bin/" + name },
		Test:   test,
	}, runner
}

func lsattrKey(path string) string {
	return exec.Command{Argv: []string{"lsattr", "-d", "--", path}}.String()
}

func TestChattrAddPredictsUnderTestModeAndRunsNothing(t *testing.T) {
	path := "/root/rpmchattr-cap/f"
	c, runner := chattrTestContext(map[string]exec.Result{lsattrKey(path): {Stdout: lsattrPlainAlma8}}, true)
	got, err := chattrChange(c, []string{path}, "ia", true)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := got.Get(path)
	if !ok {
		t.Fatalf("no change predicted: %v", got)
	}
	change := raw.(*value.Map)
	newVal, _ := change.Get("new")
	if list, _ := newVal.([]any); len(list) != 3 {
		t.Errorf("predicted new = %#v, want a, e, i", newVal)
	}
	for _, ran := range runner.RanCommands() {
		if strings.HasPrefix(ran, "chattr") {
			t.Errorf("test mode ran %s", ran)
		}
	}
}

func TestChattrAddIsANoOpWhenAlreadySet(t *testing.T) {
	path := "/root/rpmchattr-cap/f"
	c, runner := chattrTestContext(map[string]exec.Result{lsattrKey(path): {Stdout: lsattrImmutableRocky9}}, false)
	got, err := chattrChange(c, []string{path}, "i", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 0 {
		t.Errorf("changes = %v, want none", got)
	}
	if len(runner.Ran) != 1 {
		t.Errorf("ran %v, want only the read", runner.RanCommands())
	}
}

// The recorder answers every lsattr with the same line, so after the
// chattr the read-back still says the attribute is absent -- which is
// exactly the case the read-back exists for: chattr exiting 0 without
// the filesystem agreeing.
func TestChattrRefusesAChangeTheFilesystemDidNotMake(t *testing.T) {
	path := "/root/rpmchattr-cap/f"
	c, _ := chattrTestContext(map[string]exec.Result{lsattrKey(path): {Stdout: lsattrPlainRocky9}}, false)
	_, err := chattrChange(c, []string{path}, "i", true)
	if err == nil || !strings.Contains(err.Error(), "exited 0 but lsattr now reports") {
		t.Errorf("err = %v", err)
	}
}

// The failure text chattr really prints for a letter the installed
// e2fsprogs does not know (`+x` on AlmaLinux 8.10's 1.45.6): its usage
// line and nothing else, on stderr, exit 1.
func TestChattrCarriesChattrsOwnRefusal(t *testing.T) {
	path := "/root/rpmchattr-cap/f"
	c, _ := chattrTestContext(map[string]exec.Result{
		lsattrKey(path): {Stdout: lsattrPlainAlma8},
		exec.Command{Argv: []string{"chattr", "+x", "--", path}}.String(): {
			Stderr: "Usage: chattr [-pRVf] [-+=aAcCdDeijPsStTuF] [-v version] files...\n", Code: 1},
	}, false)
	_, err := chattrChange(c, []string{path}, "x", true)
	if err == nil || !strings.Contains(err.Error(), "Usage: chattr") {
		t.Errorf("err = %v", err)
	}
}

func TestChattrGetCarriesLsattrsOwnRefusal(t *testing.T) {
	c, _ := chattrTestContext(map[string]exec.Result{
		lsattrKey("/proc"): {Stderr: "lsattr: Inappropriate ioctl for device While reading flags on /proc\n", Code: 1},
	}, false)
	_, err := chattrGet(c, "/proc")
	if err == nil || !strings.Contains(err.Error(), "Inappropriate ioctl") {
		t.Errorf("err = %v", err)
	}
}

func TestChattrCallsAskForTheirExitCode(t *testing.T) {
	path := "/root/rpmchattr-cap/f"
	c, runner := chattrTestContext(map[string]exec.Result{lsattrKey(path): {Stdout: lsattrPlainRocky9}}, false)
	_, _ = chattrChange(c, []string{path}, "i", true)
	if len(runner.Ran) < 2 {
		t.Fatalf("ran %v", runner.RanCommands())
	}
	for _, cmd := range runner.Ran {
		if !cmd.IgnoreExitCode {
			t.Errorf("%s does not set IgnoreExitCode", cmd.String())
		}
	}
}
