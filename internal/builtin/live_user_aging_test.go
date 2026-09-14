//go:build linux

package builtin

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Password ageing, set through `user.present` and read back out of the
// tool that owns it.
//
// The unit tests pin the mapping from argument to chage flag. What they
// cannot say is whether chage accepts the command, or whether the
// column it writes is the one the next run reads -- a writer and a
// reader that agree with each other and with nothing else is the shape
// DIVERGENCE 5.31 is about. So this creates a throwaway account, sets
// all five, and asks `chage -l` what happened.
//
// It needs root and it edits the shadow file, so it is gated on
// HALITE_SYSTEM_LIVE like the other mutating live tests, and it removes
// the account it made.
func TestAgeingIsWhatChageReportsBack(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set and this is not root; user.present declares it needs root")
	}
	for _, tool := range []string{"useradd", "userdel", "chage"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("this host has no %s", tool)
		}
	}

	const name = "halite-ageing-probe"
	_ = exec.Command("userdel", "-r", name).Run()
	t.Cleanup(func() { _ = exec.Command("userdel", "-r", name).Run() })

	c := &hexec.Context{}
	args := value.MapOf(
		"name", name,
		"createhome", false,
		"mindays", int64(2),
		"maxdays", int64(90),
		"warndays", int64(14),
		"inactdays", int64(30),
	)

	res, err := New().States.Call(c, "user.present", args)
	if err != nil {
		t.Fatalf("user.present: %v", err)
	}
	if !stateSucceeded(res) {
		t.Fatalf("user.present did not succeed: %v", res)
	}

	// `chage -l` is the tool's own report. Its labels are prose and its
	// dates are locale-dependent, which is why the module reads the
	// shadow columns instead -- but for an assertion, prose the tool
	// chose is better evidence than the file this code already parses.
	out, err := exec.Command("chage", "-l", name).Output()
	if err != nil {
		t.Fatalf("chage -l: %v", err)
	}
	report := string(out)
	for _, want := range []struct{ label, value string }{
		{"Minimum number of days between password change", "2"},
		{"Maximum number of days between password change", "90"},
		{"Number of days of warning before password expires", "14"},
		{"Password inactive", ""}, // presence only; its line is a date
	} {
		line := lineContaining(report, want.label)
		if line == "" {
			t.Errorf("chage -l did not report %q:\n%s", want.label, report)
			continue
		}
		if want.value != "" && !strings.Contains(line, want.value) {
			t.Errorf("%s = %q, want %s", want.label, line, want.value)
		}
	}

	// And the module's own reader agrees with the tool, which is the
	// half that keeps the next run from reporting a change forever.
	have, found, err := readAging(name)
	if err != nil || !found {
		t.Fatalf("reading the ageing back: found=%v err=%v", found, err)
	}
	for _, c := range []struct {
		what string
		got  *int64
		want int64
	}{
		{"mindays", have.Min, 2},
		{"maxdays", have.Max, 90},
		{"warndays", have.Warn, 14},
		{"inactdays", have.Inact, 30},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("readAging %s = %v, want %d", c.what, deref(c.got), c.want)
		}
	}

	// Applying the same declaration again is no change at all, which is
	// convergence and the thing a policy state is judged on.
	again, err := New().States.Call(c, "user.present", args)
	if err != nil {
		t.Fatalf("second user.present: %v", err)
	}
	if again.Changes != nil && again.Changes.Len() != 0 {
		t.Errorf("a second identical apply reported changes: %v", again.Changes.Entries())
	}
}

func deref(p *int64) any {
	if p == nil {
		return "(unset)"
	}
	return *p
}

func lineContaining(text, sub string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}
