package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

func TestArgvIsNotParsedByAShell(t *testing.T) {
	// The whole point of defaulting to argv: a value that looks like shell
	// syntax is one argument, not a second command.
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Argv: []string{"/bin/echo", "a; touch /tmp/halite-should-not-exist", "$HOME", "`id`"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(res.Stdout)
	want := "a; touch /tmp/halite-should-not-exist $HOME `id`"
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestShellIsOptIn(t *testing.T) {
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{Argv: []string{"echo one; echo two"}, Shell: true})
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(res.Stdout); len(lines) != 2 {
		t.Errorf("the shell did not run the second command: %q", res.Stdout)
	}
}

// SPEC section 15.2: umask reaches the state file, so it has to reach the
// child. The bug this covers was a silent drop, which produced a file with
// the wrong mode and a state that reported success.
func TestUmaskReachesTheChild(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "created")
	r := &OSRunner{}
	_, err := r.Run(context.Background(), Command{
		Argv:  []string{"/usr/bin/touch", target},
		Umask: "077",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600; the umask did not reach the child", perm)
	}
}

func TestUmaskStillPassesArgvUnparsed(t *testing.T) {
	// The umask rewrite goes through a shell, so the argument vector has
	// to survive it intact.
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Argv:  []string{"/bin/echo", "a; echo INJECTED", "$HOME", "it's quoted"},
		Umask: "022",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exact equality is the assertion: echo prints its arguments and
	// nothing else, so anything the shell added or expanded shows up here.
	want := "a; echo INJECTED $HOME it's quoted\n"
	if res.Stdout != want {
		t.Errorf("stdout = %q, want %q", res.Stdout, want)
	}
}

func TestUmaskAppliesToAShellCommandToo(t *testing.T) {
	dir := t.TempDir()
	r := &OSRunner{}
	_, err := r.Run(context.Background(), Command{
		Argv:  []string{"touch " + filepath.Join(dir, "f")},
		Shell: true,
		Umask: "077",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600", perm)
	}
}

func TestUmaskRefusesAnythingButAnOctalMask(t *testing.T) {
	for _, bad := range []string{"0777; rm -rf /", "abc", "099", "", "  ", "77777"} {
		cmd := Command{Argv: []string{"/bin/echo"}, Umask: bad}
		if bad == "" {
			// An unset umask is not an error, it is simply no umask.
			out, err := applyUmask(cmd)
			if err != nil || out.Shell {
				t.Errorf("an empty umask should be a no-op, got %v %v", out, err)
			}
			continue
		}
		if _, err := applyUmask(cmd); err == nil {
			t.Errorf("umask %q should be refused", bad)
		}
	}
	if _, err := applyUmask(Command{Argv: []string{"/bin/echo"}, Umask: "022"}); err != nil {
		t.Errorf("a valid mask was refused: %v", err)
	}
}

func TestExitCodeIsAnErrorUnlessIgnored(t *testing.T) {
	r := &OSRunner{}
	cmd := Command{Argv: []string{"/bin/sh", "-c", "echo out; echo problem >&2; exit 3"}}

	res, err := r.Run(context.Background(), cmd)
	if err == nil {
		t.Error("a non-zero exit should be an error by default")
	}
	if res.Code != 3 {
		t.Errorf("code = %d", res.Code)
	}
	if !strings.Contains(err.Error(), "problem") {
		t.Errorf("the error should carry stderr: %v", err)
	}

	// `unless` and `onlyif` read the code, so for them it is data.
	cmd.IgnoreExitCode = true
	res, err = r.Run(context.Background(), cmd)
	if err != nil {
		t.Errorf("IgnoreExitCode did not suppress the error: %v", err)
	}
	if res.Code != 3 || strings.TrimSpace(res.Stdout) != "out" {
		t.Errorf("result = %+v", res)
	}
}

func TestMissingProgramIsAnError(t *testing.T) {
	r := &OSRunner{}
	if _, err := r.Run(context.Background(), Command{Argv: []string{"/nonexistent/halite-test"}}); err == nil {
		t.Error("a missing program should be an error")
	}
	if _, err := r.Run(context.Background(), Command{}); err == nil {
		t.Error("an empty argv should be an error")
	}
}

func TestTimeoutStopsALongCommand(t *testing.T) {
	r := &OSRunner{}
	start := time.Now()
	_, err := r.Run(context.Background(), Command{
		Argv:    []string{"/bin/sh", "-c", "sleep 30"},
		Timeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Error("the timeout did not fire")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the timeout took %v to fire", elapsed)
	}
}

func TestStdinAndDir(t *testing.T) {
	dir := t.TempDir()
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Argv:  []string{"/bin/sh", "-c", "cat; pwd"},
		Stdin: "fed in\n",
		Dir:   dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "fed in") {
		t.Errorf("stdin did not reach the child: %q", res.Stdout)
	}
	// The temp dir may be reached through a symlink, so compare the base.
	if !strings.Contains(res.Stdout, filepath.Base(dir)) {
		t.Errorf("Dir did not take effect: %q", res.Stdout)
	}
}

// SPEC section 25.4: a child gets a clean environment, so that a hub
// credential or a pillar value in halite's own environment cannot leak
// into an arbitrary command.
func TestChildGetsACleanEnvironment(t *testing.T) {
	t.Setenv("HALITE_TEST_SECRET", "do-not-leak")
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{Argv: []string{"/usr/bin/env"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "do-not-leak") {
		t.Errorf("halite's environment leaked into the child:\n%s", res.Stdout)
	}
	for _, want := range []string{"PATH=", "LC_ALL=C", "HALITE=1"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("the clean environment is missing %s:\n%s", want, res.Stdout)
		}
	}
}

func TestExplicitEnvReplacesTheCleanOne(t *testing.T) {
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Argv: []string{"/usr/bin/env"},
		Env:  []string{"ONLY=this"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "HALITE=1") {
		t.Errorf("an explicit Env should replace the default entirely:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "ONLY=this") {
		t.Errorf("the explicit Env did not take effect:\n%s", res.Stdout)
	}
}

func TestCleanEnvFallsBackToAnExplicitPath(t *testing.T) {
	t.Setenv("PATH", "")
	found := false
	for _, e := range CleanEnv() {
		if strings.HasPrefix(e, "PATH=") {
			found = strings.Contains(e, "/usr/bin")
		}
	}
	if !found {
		t.Errorf("an empty PATH should fall back to a usable one: %v", CleanEnv())
	}
}

func TestCommandStringQuotesWhatNeedsIt(t *testing.T) {
	cases := []struct {
		cmd  Command
		want string
	}{
		{Command{Argv: []string{"ls", "-la", "/tmp"}}, "ls -la /tmp"},
		{Command{Argv: []string{"echo", "two words"}}, `echo "two words"`},
		{Command{Argv: []string{"echo hi"}, Shell: true}, "echo hi"},
	}
	for _, c := range cases {
		if got := c.cmd.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

func TestRunAsRefusesAnUnknownAccount(t *testing.T) {
	r := &OSRunner{}
	_, err := r.Run(context.Background(), Command{
		Argv:  []string{"/bin/echo"},
		RunAs: "halite-no-such-account",
	})
	if err == nil {
		t.Fatal("an unknown account should be refused rather than silently ignored")
	}
	if !strings.Contains(err.Error(), "halite-no-such-account") {
		t.Errorf("the error should name the account: %v", err)
	}
}

// ---- registry ----

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	r.Add(
		Module{
			Sig: signature.Signature{Module: "test", Function: "echo", Params: []signature.Param{
				{Name: "text", Type: signature.String, Required: true},
			}},
			Fn: func(c *Context, args *value.Map) (any, error) {
				v, _ := args.Get("text")
				return v, nil
			},
		},
		Module{
			Sig: signature.Signature{Module: "test", Function: "fail"},
			Fn: func(c *Context, args *value.Map) (any, error) {
				return nil, os.ErrPermission
			},
		},
	)
	return r
}

func TestRegistryCallsAndReportsMisses(t *testing.T) {
	r := testRegistry(t)
	ctx := &Context{}

	out, err := r.Call(ctx, "test.echo", value.MapOf("text", "hello"))
	if err != nil || out != "hello" {
		t.Fatalf("call = %#v, %v", out, err)
	}
	if !r.Has("test.echo") || r.Has("test.nope") {
		t.Error("Has disagrees with the registry")
	}

	_, err = r.Call(ctx, "test.nope", value.NewMap(0))
	var unknown *UnknownFunctionError
	if !asUnknown(err, &unknown) {
		t.Fatalf("an unknown function should be an UnknownFunctionError, got %T: %v", err, err)
	}
}

// A typo should suggest the function it is one edit from, because the
// alternative is an operator rereading the module list by hand.
func TestUnknownFunctionSuggestsANearMiss(t *testing.T) {
	r := testRegistry(t)
	_, err := r.Call(&Context{}, "test.eco", value.NewMap(0))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "test.echo") {
		t.Errorf("the error should suggest test.echo: %v", err)
	}
}

func TestCallPositionalBindsByParameterOrder(t *testing.T) {
	r := testRegistry(t)
	out, err := r.CallPositional(&Context{}, "test.echo", []any{"positional"}, value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if out != "positional" {
		t.Errorf("out = %#v", out)
	}

	// More positional arguments than parameters is an error rather than a
	// silent drop.
	if _, err := r.CallPositional(&Context{}, "test.echo", []any{"a", "b", "c"}, value.NewMap(0)); err == nil {
		t.Error("surplus positional arguments should be refused")
	}
}

func TestRequiredArgumentIsEnforced(t *testing.T) {
	r := testRegistry(t)
	_, err := r.Call(&Context{}, "test.echo", value.NewMap(0))
	if err == nil {
		t.Fatal("a missing required argument should be an error")
	}
	if !strings.Contains(err.Error(), "text") {
		t.Errorf("the error should name the parameter: %v", err)
	}
}

func TestNamesAreSorted(t *testing.T) {
	names := testRegistry(t).Names()
	if len(names) != 2 {
		t.Fatalf("names = %v", names)
	}
	if names[0] > names[1] {
		t.Errorf("names should be sorted: %v", names)
	}
}

// ---- the recorder, which every module test depends on ----

func TestRecordingRunner(t *testing.T) {
	r := &RecordingRunner{
		Default:   Result{Stdout: "default"},
		Responses: map[string]Result{"id -u": {Stdout: "0"}},
	}
	c := &Context{Runner: r}

	res, _ := c.RunArgv("id", "-u")
	if strings.TrimSpace(res.Stdout) != "0" {
		t.Errorf("a keyed response did not match: %#v", res)
	}
	res, _ = c.RunArgv("something", "else")
	if res.Stdout != "default" {
		t.Errorf("the default was not used: %#v", res)
	}
	if got := r.RanCommands(); len(got) != 2 || got[0] != "id -u" {
		t.Errorf("RanCommands = %v", got)
	}
}

func TestContextLogging(t *testing.T) {
	var lines []string
	c := &Context{Log: func(level, msg string) { lines = append(lines, level+": "+msg) }}
	c.Logf("warning", "%d problems", 2)
	if len(lines) != 1 || !strings.Contains(lines[0], "2 problems") {
		t.Errorf("lines = %v", lines)
	}
	// A context with no logger must not panic; a module logs freely.
	(&Context{}).Logf("info", "dropped")
}

func asUnknown(err error, target **UnknownFunctionError) bool {
	e, ok := err.(*UnknownFunctionError)
	if ok {
		*target = e
	}
	return ok
}
