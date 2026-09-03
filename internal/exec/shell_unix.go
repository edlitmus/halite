//go:build !windows

package exec

import (
	"os"
	"strings"
)

// defaultPath is the search path a spawned process gets when nothing else
// says what it should be.
//
// SPEC 25.4 asks for an explicit PATH, and a process started by a service
// manager inherits whatever that manager happened to set — which differs
// between rc.d, systemd, and an operator's shell. The `exec_path` setting
// makes it explicit; this is the last resort when neither it nor the
// environment names one.
func defaultPath() string {
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

// shellCommand is how a Shell command is handed to an interpreter.
func shellCommand(script string) []string { return []string{"/bin/sh", "-c", script} }

// shellName is what to call the interpreter in a message.
func shellName() string { return "/bin/sh" }

// cleanEnv is the environment a spawned process receives unless a module
// says otherwise: no hub credentials, no pillar values, and an explicit
// PATH. SPEC section 25.4.
func cleanEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = defaultPath()
	}
	return []string{
		"PATH=" + path,
		"LC_ALL=C",
		"LANG=C",
		"HALITE=1",
	}
}

// quoteScriptArg wraps a string so a shell reads it as one argument.
func quoteScriptArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSeparator ends one command in a Shell string so another can
// follow. `;` on a POSIX shell, `&` in cmd.exe.
const shellSeparator = ";"

// wantOnFallbackPath is a directory the fallback search path must name,
// for the test that checks it is a usable path on this platform.
const wantOnFallbackPath = "/usr/bin"
