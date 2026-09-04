package grains

import (
	"os"
	"path/filepath"
	"strings"
)

// isRunnable reports whether a file in grains.d is a program to run
// rather than a document to parse.
//
// There is no execute bit here. os.Stat synthesises 0666 for every
// writable file, so `info.Mode()&0o111 != 0` was false for everything
// and every file in grains.d was parsed as YAML — including the scripts.
// A provider script's first line then failed to parse and the operator
// got a YAML error about a shell script.
//
// What decides whether Windows can run a file is its extension, and
// PATHEXT is the machine's own list of them. A file whose extension is
// on that list is a program; anything else is a document. .ps1 is added
// because PATHEXT does not carry it and a PowerShell script is the
// obvious way to write a grain provider here — see runGrainProvider,
// which knows to hand one to powershell.
func isRunnable(path string, info os.FileInfo) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	if ext == ".ps1" {
		return true
	}
	for _, known := range strings.Split(strings.ToLower(pathext()), ";") {
		if known != "" && ext == known {
			return true
		}
	}
	return false
}

// pathext is the machine's list of runnable extensions, with the
// documented default for a service started without one.
func pathext() string {
	if v := os.Getenv("PATHEXT"); v != "" {
		return v
	}
	return ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.WSF;.MSC"
}

// providerArgv is how a grain provider is invoked.
//
// A .ps1 is not an executable file here: it is an argument to
// powershell. -NoProfile because an operator's interactive setup has no
// business changing what a grain reports, and -NonInteractive so a
// prompt fails rather than hanging grain collection until the timeout.
func providerArgv(path string) []string {
	if strings.EqualFold(filepath.Ext(path), ".ps1") {
		return []string{
			"powershell.exe", "-NoProfile", "-NonInteractive",
			"-ExecutionPolicy", "Bypass", "-File", path,
		}
	}
	return []string{path}
}
