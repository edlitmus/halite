package builtin

import (
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// checkCmdParams are the arguments the file states that own `check_cmd`
// take for it.
//
// `check_cmd` itself is declared here and never read out of the
// arguments: the state compiler strips it into the chunk's options with
// `unless` and `onlyif`, and it reaches the state on the context. The
// parameter exists because that is how Salt decides which form of
// `check_cmd` a state gets -- `salt/state.py` asks whether the state
// function has the parameter -- and because an operator reading the
// argument table should find it where Salt documents it.
func checkCmdParams() []signature.Param {
	return []signature.Param{
		{
			Name: "check_cmd", Type: signature.Any,
			Doc: "Validate the contents before installing them. The path of a temporary file " +
				"holding what would be written is appended to each command, and the file is " +
				"installed only if every command exits 0.",
		},
		{
			Name: "tmp_dir", Type: signature.Path,
			Doc: "Directory for the temporary file check_cmd is given. For a checker that is " +
				"confined to a directory by an AppArmor or SELinux policy.",
		},
		{
			Name: "tmp_ext", Type: signature.String,
			Doc: "Suffix for the temporary file check_cmd is given. For a checker that insists " +
				"on an extension.",
		},
	}
}

// runFileCheckCmd validates pending contents and reports whether they
// may be installed.
//
// Salt writes what it is about to install to a temporary file and hands
// that to the command, which is the whole point of the feature: a
// sudoers file that fails `visudo` must never reach `/etc/sudoers.d`,
// and a check that runs after the write has already lost. The runner's
// generic `check_cmd` does run after, and that is the right shape for
// the states that have no file to validate -- see DIVERGENCE 5.108.
//
// The command is a string with the path appended, run through a shell,
// because that is what a tree writes: `check_cmd: /usr/sbin/visudo -c -f`
// is a command and its flags, and the flag it ends on takes the path.
func runFileCheckCmd(c *exec.Context, args *value.Map, path string, want []byte) (states.Result, bool) {
	dir := states.Str(args, "tmp_dir", "")
	pattern := "halite-check-*" + states.Str(args, "tmp_ext", "")

	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return states.False(fmt.Sprintf(
			"The check_cmd for %s needs a temporary file and could not make one: %v", path, err)), false
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()

	_, writeErr := f.Write(want)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return states.False(fmt.Sprintf(
			"The check_cmd for %s could not write its temporary file: %v",
			path, firstErr(writeErr, closeErr))), false
	}
	// Readable by the checker, which may drop privileges: visudo is the
	// documented case and runs as root, but a checker invoked through
	// `runas` is not.
	if err := os.Chmod(tmp, 0o644); err != nil {
		return states.False(fmt.Sprintf(
			"The check_cmd for %s could not set the mode on its temporary file: %v", path, err)), false
	}

	for _, cmd := range c.CheckCmd {
		full := cmd + " " + shellQuote(tmp)
		out, err := c.Run(exec.Command{
			Argv:           []string{full},
			Shell:          true,
			RunAs:          c.RunAs,
			Umask:          c.Umask,
			IgnoreExitCode: true,
		})
		if err != nil {
			return states.False(fmt.Sprintf(
				"The check_cmd %q for %s could not be run: %v", cmd, path, err)), false
		}
		if out.Code != 0 {
			// The command's own output is what says why. `visudo`
			// answers "syntax error near line 3", and a comment that
			// reported only an exit status would send an operator to
			// run it by hand to find that out.
			return states.False(strings.TrimRight(fmt.Sprintf(
				"The check_cmd %q rejected the contents of %s, so it was not written (exit status %d).\n%s",
				cmd, path, out.Code, checkCmdOutput(out)), "\n")), false
		}
	}
	return states.Result{}, true
}

// checkCmdOutput is what the checker said, in the order a terminal would
// have shown it.
func checkCmdOutput(out exec.Result) string {
	var b strings.Builder
	for _, s := range []string{out.Stdout, out.Stderr} {
		if t := strings.TrimSpace(s); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// shellQuote makes a path safe to append to a command that goes through
// a shell. A temporary path is this build's own and holds no spaces, but
// `tmp_dir` is the operator's.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`&;|<>()*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
