package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// The command environment on Windows.
//
// Everything here was POSIX. The fallback search path was a
// colon-separated list of unix directories, which is not a path on this
// platform at all; the shell was /bin/sh, which does not exist; and the
// clean environment carried PATH, LC_ALL, LANG and nothing else, which
// on Windows is not a usable environment: a process started without
// SystemRoot cannot load a system DLL and fails before main, and one
// without PATHEXT cannot resolve a program by name.

// defaultPath is the search path a spawned process gets when nothing
// else says what it should be. Built from the real system root rather
// than assuming C:, because a machine imaged onto another drive is not
// unusual and the failure would be every command at once.
func defaultPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return strings.Join([]string{
		root + `\system32`,
		root,
		root + `\System32\Wbem`,
		root + `\System32\WindowsPowerShell\v1.0`,
	}, ";")
}

// comspec is the command interpreter. %ComSpec% rather than a literal,
// because it is what the platform itself uses to find one.
func comspec() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\system32\cmd.exe`
}

// shellFlag names the switch that hands a script to the interpreter,
// which is not the same switch for every interpreter `shell:` can name.
//
// cmd.exe's is `/c`. Windows PowerShell and pwsh take `-Command`
// instead, though both happen to also accept `/c` undocumented --
// confirmed on this host -- which is not a reason to rely on it. Every
// other name is assumed to be a POSIX-style shell (bash, sh, zsh) and
// gets `-c`. Passing `/c` there does not merely fail to be recognised:
// Git's bash reads an unknown switch as a path, and `/c` is the drive
// letter Git mounts `C:\` on, so `shell: bash` ran no command at all and
// failed every time with "/c: Is a directory" -- confirmed on this host.
func shellFlag(shell string) string {
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(shell), ".exe")) {
	case "cmd":
		return "/c"
	case "powershell", "pwsh":
		return "-Command"
	default:
		return "-c"
	}
}

// ShellRunFlag is the switch a named interpreter needs before a file
// path to run it as a script, for a caller that otherwise hands the
// interpreter a bare path -- `cmd.script`'s `shell` option.
//
// Every shell this platform's `shellFlag` recognises as POSIX-style, and
// PowerShell given a path, runs a file named positionally; that is
// `scriptInterpreter`'s `-File` for PowerShell and plain argv for the
// rest. cmd.exe does not: `cmd.exe C:\x.cmd`, with no switch, starts an
// interactive session and ignores the path, so `cmd.script` with
// `shell: cmd` ran nothing and reported exit 0 -- confirmed on this
// host. It needs `/c` before the path, the same as a Shell command does.
func ShellRunFlag(shell string) string {
	if shellFlag(shell) == "/c" {
		return "/c"
	}
	return ""
}

// shellCommand is how a Shell command is handed to an interpreter.
//
// `shell` names it when the caller asked for a particular one -- Salt's
// `shell:` argument -- and %ComSpec% is the default.
func shellCommand(script, shell string) []string {
	if shell == "" {
		shell = comspec()
	}
	return []string{shell, shellFlag(shell), script}
}

// applyShellCmdLine overrides the Windows command line os/exec would
// otherwise build for a Shell command, for interpreters that need it.
//
// os/exec escapes an argument's embedded double quotes as `\"`, which is
// the convention the Microsoft C runtime's argv parser expects and is
// right for every interpreter here except one. cmd.exe pairs quotes
// itself and gives a backslash no special meaning, so the backslash
// reached it as a literal character and the quote still closed the
// pair: a script holding a double quote, such as
// `cmd.run 'echo "a" & echo "b"'`, arrived with stray backslashes in
// the text cmd.exe actually ran rather than the quotes the caller wrote
// -- confirmed on this host, where the unchanged code turned
// `echo "hello world"` into the output `\"hello world\"`. Building the
// command line by hand, unescaped past the interpreter's own path,
// matches what typing the script at a cmd.exe prompt would do.
func applyShellCmdLine(c *exec.Cmd, shell, script string) {
	flag := shellFlag(shell)
	if flag != "/c" {
		return
	}
	c.SysProcAttr = &windows.SysProcAttr{
		CmdLine: windows.EscapeArg(shell) + " " + flag + " " + script,
	}
}

// cleanEnv is the environment a spawned process receives unless a module
// says otherwise.
//
// The unix version carries PATH and nothing else that the host set,
// which is the whole point: no hub credentials and no pillar values
// reach a child. Windows needs four more names before a process can
// start at all, so they are passed through by name — never by copying
// the environment, which is what this function exists to avoid.
func cleanEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = defaultPath()
	}
	env := []string{
		"PATH=" + path,
		"LC_ALL=C",
		"LANG=C",
		"HALITE=1",
	}
	for _, name := range []string{"SystemRoot", "windir", "PATHEXT", "ComSpec", "TEMP", "TMP", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// shellSeparator ends one command in a Shell string so another can
// follow. cmd.exe uses `&`; `;` is an ordinary character there and a
// line using it runs as one command with a stray argument.
const shellSeparator = "&"

// wantOnFallbackPath is a directory the fallback search path must name,
// for the test that checks it is a usable path on this platform.
const wantOnFallbackPath = `\system32`
