package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/value"
)

// realCtx runs commands for real, which these need: a script that is
// never executed proves nothing about running scripts.
func realCtx(t *testing.T) *exec.Context {
	t.Helper()
	c := newCtx(false)
	c.Runner = &exec.OSRunner{}
	return c
}

func TestCmdScriptRunsAndCleansUp(t *testing.T) {
	r := New()
	script := writeEchoScript(t, t.TempDir())

	c := realCtx(t)
	out, err := r.Exec.Call(c, "cmd.script",
		value.MapOf("source", script, "args", []any{"world"}))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if stdout, _ := m.Get("stdout"); stdout != "hello world" {
		t.Errorf("stdout = %#v", stdout)
	}
	if code, _ := m.Get("retcode"); code != int64(0) {
		t.Errorf("retcode = %#v", code)
	}

	// The copy the script ran from is gone. A script often carries a
	// credential, and leaving one in the temporary directory outlives the
	// reason it was there.
	entries, _ := filepath.Glob(filepath.Join(os.TempDir(), "halite-script-*"))
	if len(entries) > 0 {
		t.Errorf("script copies were left behind: %v", entries)
	}
}

func TestCmdScriptReportsAFailure(t *testing.T) {
	r := New()
	script := writeFailingScript(t, t.TempDir())

	c := realCtx(t)
	_, err := r.Exec.Call(c, "cmd.script", value.MapOf("source", script))
	if err == nil {
		t.Fatal("a script exiting non-zero should be an error")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("the error should carry stderr: %v", err)
	}

	// script_retcode returns the code instead of failing, which is what
	// makes it usable in an `unless`.
	code, err := r.Exec.Call(realCtx(t), "cmd.script_retcode", value.MapOf("source", script))
	if err != nil {
		t.Fatal(err)
	}
	if code != int64(3) {
		t.Errorf("retcode = %#v", code)
	}
}

func TestCmdExecCode(t *testing.T) {
	r := New()
	c := realCtx(t)
	if c.Which("sh") == "" {
		t.Skip("no shell")
	}
	out, err := r.Exec.Call(c, "cmd.exec_code",
		value.MapOf("lang", "sh", "code", "echo from-the-snippet\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "from-the-snippet" {
		t.Errorf("out = %#v", out)
	}
	if _, err := r.Exec.Call(realCtx(t), "cmd.exec_code",
		value.MapOf("lang", "", "code", "x")); err == nil {
		t.Error("exec_code with no interpreter should be refused")
	}
}

// A script is written where only its owner can read it, because many
// carry a credential and the temporary directory is world-readable.
func TestTempScriptIsPrivate(t *testing.T) {
	path, cleanup, err := tempScript("#!/bin/sh\necho x\n", 0o700, "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// Asked in the platform's own terms. This read the mode, which on
	// Windows is synthesised from the read-only attribute: it reported
	// 0666 for a script that nothing was protecting.
	others, err := fileperm.Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("the script can be read by %v", others)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cleanup did not remove the script")
	}
}

func TestCmdShellSaysItIsUsingAShell(t *testing.T) {
	r := New()
	var logged []string
	c := realCtx(t)
	c.Log = func(level, msg string) { logged = append(logged, level+": "+msg) }

	out, err := r.Exec.Call(c, "cmd.shell", value.MapOf("cmd", shellLine("echo a", "echo b")))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(out.(string))) != 2 {
		t.Errorf("the shell did not run both commands: %#v", out)
	}
	// SPEC 15.2 inverts Salt's default and asks that opting back in be
	// visible. A silent shell is the thing the inversion exists to stop.
	found := false
	for _, l := range logged {
		if strings.Contains(l, "shell") {
			found = true
		}
	}
	if !found {
		t.Errorf("cmd.shell did not log that it used a shell: %v", logged)
	}
}

func TestCmdRunBgReturnsAPid(t *testing.T) {
	r := New()
	c := realCtx(t)
	name, args := trueProgram()
	out, err := r.Exec.Call(c, "cmd.run_bg", value.MapOf("name", name, "args", args))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := out.(*value.Map).Get("pid")
	n, ok := pid.(int64)
	if !ok || n <= 0 {
		t.Errorf("pid = %#v, want a positive process id", pid)
	}

	// Under a recorder nothing is started, and the command is still
	// visible to the test.
	rec := newCtx(false)
	out, err = r.Exec.Call(rec, "cmd.run_bg", value.MapOf("name", "/bin/true"))
	if err != nil {
		t.Fatal(err)
	}
	if recorded, _ := out.(*value.Map).Get("recorded"); recorded != true {
		t.Errorf("a recorded run should say so: %#v", out)
	}
	if ran := rec.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 1 {
		t.Errorf("the recorder should have seen the command: %v", ran)
	}
}

func TestServiceKnownIgnoresTheSystemdSuffix(t *testing.T) {
	names := []string{"nginx.service", "sshd", "cron.service"}
	for _, want := range []string{"nginx", "nginx.service", "sshd", "sshd.service", "cron"} {
		if !serviceKnown(names, want) {
			t.Errorf("%q should be found in %v", want, names)
		}
	}
	if serviceKnown(names, "postgresql") {
		t.Error("a service that is not there should not be found")
	}
}
