package exec

import (
	"os"
	"strings"
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

// shellCommand is how a Shell command is handed to an interpreter.
//
// /c rather than /k, so the interpreter exits when the command does.
func shellCommand(script string) []string { return []string{comspec(), "/c", script} }

// shellName is what to call the interpreter in a message.
func shellName() string { return comspec() }

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

// quoteScriptArg wraps a string so the interpreter reads it as one
// argument.
//
// cmd.exe has no quoting that makes a value safe the way a POSIX single
// quote does: `^` escapes, `"` groups, and the two interact differently
// depending on whether the line is parsed once or twice. The only caller
// is the umask rewrite, which does not run here, so this refuses to
// pretend by returning the value unchanged rather than a quoting that
// would look right and not be.
func quoteScriptArg(s string) string { return s }

// shellSeparator ends one command in a Shell string so another can
// follow. cmd.exe uses `&`; `;` is an ordinary character there and a
// line using it runs as one command with a stray argument.
const shellSeparator = "&"

// wantOnFallbackPath is a directory the fallback search path must name,
// for the test that checks it is a usable path on this platform.
const wantOnFallbackPath = `\system32`
