package exec

import (
	"context"
	osexec "os/exec"
	"strings"
	"testing"
)

// cmd.exe pairs quotes itself and does not treat a backslash before one
// as an escape. os/exec's normal argument escaping assumes the opposite
// -- the Microsoft C runtime convention -- so a script holding a double
// quote arrived with stray backslashes where the quotes had been. This
// ran on the unchanged code and printed `\"hello world\"`.
func TestAQuotedShellScriptIsNotMangledByCmdExe(t *testing.T) {
	r := &OSRunner{}
	res, err := r.Run(context.Background(), Command{
		Argv:  []string{`echo "hello world"`},
		Shell: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(res.Stdout, "\r\n")
	if got != `"hello world"` {
		t.Errorf(`cmd.exe ran echo "hello world" and returned %q, want the literal %q`, got, `"hello world"`)
	}
}

// bash reads an unrecognised switch as a path, and /c happens to be the
// drive Git mounts C:\ on, so shell: bash failed every run on the
// unchanged code with "/c: Is a directory" instead of running the
// script at all.
func TestShellPicksTheRightSwitchForEachInterpreter(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"bash", `C:\Program Files\Git\usr\bin\bash.exe`},
		{"powershell", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := osexec.LookPath(c.path); err != nil {
				t.Skipf("this host has no %s at %s: %v", c.name, c.path, err)
			}
			r := &OSRunner{}
			res, err := r.Run(context.Background(), Command{
				Argv:      []string{`echo hello`},
				Shell:     true,
				ShellPath: c.path,
			})
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			got := strings.TrimRight(res.Stdout, "\r\n")
			if got != "hello" {
				t.Errorf("%s ran echo hello and returned %q, want %q", c.name, got, "hello")
			}
		})
	}
}
