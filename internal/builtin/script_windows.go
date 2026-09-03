package builtin

import (
	"path/filepath"
	"strings"
)

// Running a script on Windows.
//
// The temporary copy was written as `halite-script-1234` with no
// extension at all, and CreateProcess will not run such a file: every
// cmd.script and cmd.script_retcode failed with
//
//	exec: "C:\...\halite-script-2041220917": executable file not found in %PATH%
//
// which names an absolute path and then says it is not on PATH, so it
// reads as a bug in halite rather than as what it is. What decides how a
// file runs here is its extension, and for two of the kinds an estate
// actually ships it is not the shell that runs it either.

// runnable are the extensions Windows will start directly.
var runnable = map[string]bool{
	".bat": true, ".cmd": true, ".exe": true, ".com": true,
}

// interpreted are the extensions a named interpreter runs.
var interpreted = map[string][]string{
	// -File rather than -Command, so the script's own arguments are
	// arguments and not a second command. -NoProfile because a profile
	// is an operator's interactive setup and has no business changing
	// what a state does; -NonInteractive so a prompt fails rather than
	// hanging the run.
	".ps1": {"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"},
	".vbs": {"cscript.exe", "//nologo"},
	".js":  {"cscript.exe", "//nologo"},
}

// scriptSuffix is what the temporary copy of a script is named with.
//
// The source's own extension where the platform knows it, because that
// is what the author chose and it is the only statement of intent
// available. A source with no extension, or one this platform has no
// meaning for, is treated as a batch file: it is what cmd.exe runs, and
// it is the closest thing here to "a script".
func scriptSuffix(source string) string {
	ext := strings.ToLower(filepath.Ext(source))
	if runnable[ext] || interpreted[ext] != nil {
		return ext
	}
	return ".cmd"
}

// scriptInterpreter is the program that runs a script of this kind, and
// its arguments before the script's own path.
func scriptInterpreter(path string) []string {
	return interpreted[strings.ToLower(filepath.Ext(path))]
}

// scriptMode is the mode a temporary script is written with. It is
// carried out as an access control list here; see internal/fileperm.
func scriptMode() uint32 { return 0o700 }
