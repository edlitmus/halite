package builtin

import (
	osexec "os/exec"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Three arguments an estate's real tree gave and this build refused.
//
// They were found by compiling that tree over a hub and reported in one
// row of DIVERGENCE 5.81 as "argument types Salt coerces" -- which was
// wrong about two of the three. Checked against Salt's own source they
// are three different problems, and each is tested for the thing it
// actually is:
//
//   - `user.present`'s `gid` was a missing lookup. Salt's docstring says
//     "Either a group name or gid can be used".
//   - `cmd.run`'s `shell` is the same name meaning two things. Salt's is
//     the shell to use, a path; SPEC 15.2's is a boolean opting in.
//   - `mount.mounted`'s `opts` is the only real coercion, and the
//     refusal was the wrong way round: Salt takes a list or a string.

// Salt's `shell: /bin/bash` names an interpreter, and naming one is
// also asking for it. A shell line written for bash is not the same
// program under /bin/sh, so the path has to reach the command rather
// than merely be accepted -- accepting and dropping it is the shape of
// the `cloud_grains` defect in 5.78.
func TestCmdShellTakesSaltsPathAndSPEC152sBoolean(t *testing.T) {
	r := New()
	c := &exec.Context{}

	// Both spellings run the line through a shell, so `echo hi` is a
	// command rather than the name of a program.
	for _, tc := range []struct {
		what  string
		shell any
	}{
		{"Salt's path form", "/bin/bash"},
		{"SPEC 15.2's boolean", true},
	} {
		got, err := r.Exec.Call(c, "cmd.run_all",
			value.MapOf("name", "echo hi", "shell", tc.shell))
		if err != nil {
			t.Errorf("%s: %v", tc.what, err)
			continue
		}
		m, ok := got.(*value.Map)
		if !ok {
			t.Errorf("%s: returned %T", tc.what, got)
			continue
		}
		if out, _ := m.GetString("stdout"); strings.TrimSpace(value.KeyString(out)) != "hi" {
			t.Errorf("%s: stdout = %v, want \"hi\"", tc.what, out)
		}
	}

	// And `shell: false` still means no shell, which is SPEC 15.2's
	// whole point: `echo hi` is then the name of a program, and there is
	// no such program. Asserting this beside the two above is what keeps
	// the path form from having quietly turned the shell on for
	// everybody.
	if _, err := r.Exec.Call(c, "cmd.run_all",
		value.MapOf("name", "echo hi", "shell", false)); err == nil {
		t.Error("`shell: false` ran a shell line as if it were a program name")
	}

	// A `shell` that is neither is refused by name rather than being
	// coerced into truth, which is what `value.Truthy` would have done
	// with, say, a list.
	if _, err := r.Exec.Call(c, "cmd.run_all",
		value.MapOf("name", "echo hi", "shell", []any{"/bin/bash"})); err == nil {
		t.Error("a `shell` that is neither a boolean nor a path was accepted")
	}
}

// The named interpreter has to be the one that runs the line. `$BASH_VERSION`
// is set by bash and empty under dash, which is what /bin/sh is on Debian,
// so the two are told apart by the shell itself rather than by this test's
// opinion of what should have happened.
func TestCmdShellPathIsTheShellThatRuns(t *testing.T) {
	if _, err := osexec.LookPath("bash"); err != nil {
		t.Skip("no bash here to tell the two shells apart")
	}
	r := New()
	c := &exec.Context{}

	out, err := r.Exec.Call(c, "cmd.run_all",
		value.MapOf("name", `echo "${BASH_VERSION:-none}"`, "shell", "/bin/bash"))
	if err != nil {
		t.Fatalf("running under bash: %v", err)
	}
	m := out.(*value.Map)
	stdout, _ := m.GetString("stdout")
	if got := strings.TrimSpace(value.KeyString(stdout)); got == "none" || got == "" {
		t.Errorf("`shell: /bin/bash` did not run under bash: BASH_VERSION = %q", got)
	}
}

// Salt: "Either a group name or gid can be used."
func TestUserGidAcceptsAGroupName(t *testing.T) {
	// A group this machine really has, whichever it is.
	g, err := user.LookupGroupId("0")
	if err != nil {
		t.Skipf("no group 0 to look up here: %v", err)
	}
	want, err := strconv.ParseInt(g.Gid, 10, 64)
	if err != nil {
		t.Skipf("group 0 has a non-numeric gid %q", g.Gid)
	}

	got, err := resolveGID(value.MapOf("gid", g.Name))
	if err != nil {
		t.Fatalf("resolving the group name %q: %v", g.Name, err)
	}
	if got != want {
		t.Errorf("gid %q resolved to %d, want %d", g.Name, got, want)
	}

	// A number is still a number, given either as one or as digits.
	if got, err := resolveGID(value.MapOf("gid", int64(1234))); err != nil || got != 1234 {
		t.Errorf("a numeric gid resolved to %d (%v), want 1234", got, err)
	}
	if got, err := resolveGID(value.MapOf("gid", "1234")); err != nil || got != 1234 {
		t.Errorf("a gid given as digits resolved to %d (%v), want 1234", got, err)
	}

	// A name no group has is an error naming it. Falling back to zero
	// would hand the account root's group, which is the one wrong answer
	// that looks like it worked.
	if got, err := resolveGID(value.MapOf("gid", "no-such-group-here-4f2a")); err == nil {
		t.Errorf("an unknown group name resolved to %d instead of failing", got)
	}
}

// Salt takes a list or a comma-separated string, and the list is the
// form in its own first documented example.
func TestMountOptsTakesAListOrAString(t *testing.T) {
	for _, tc := range []struct {
		what string
		opts any
		want string
	}{
		{"Salt's list form", []any{"defaults", "noexec", "nosuid", "nodev"}, "defaults,noexec,nosuid,nodev"},
		{"the comma-separated string", "defaults,noexec", "defaults,noexec"},
		{"an omitted opts", nil, "defaults"},
		{"an empty list", []any{}, "defaults"},
	} {
		args := value.NewMap(1)
		if tc.opts != nil {
			args.Set("opts", tc.opts)
		}
		if got := optsArg(args); got != tc.want {
			t.Errorf("%s: optsArg = %q, want %q", tc.what, got, tc.want)
		}
	}
}
