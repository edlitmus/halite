package config

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// A path for a named target is built with that target's separator, never
// with the host's. filepath.Join takes its separator from the machine it
// runs on, so RootFor("linux") answered `\etc\halite` on a Windows host
// and the whole point of taking the target as an argument — checking
// every platform's layout from one machine — was lost on the one host
// where it was most needed. These two joins are explicit about which
// convention they are in.

// posixJoin joins with a forward slash whatever host it runs on.
func posixJoin(elem ...string) string { return path.Join(elem...) }

// winJoin joins with a backslash whatever host it runs on.
func winJoin(elem ...string) string {
	parts := make([]string, 0, len(elem))
	for _, e := range elem {
		if e = strings.Trim(e, `\/`); e != "" {
			parts = append(parts, e)
		}
	}
	// A bare drive letter keeps its separator: `D:\Halite`, never
	// `D:Halite`, which names that drive's working directory rather than
	// its root.
	if len(parts) > 1 && len(parts[0]) == 2 && parts[0][1] == ':' {
		return parts[0] + `\` + strings.Join(parts[1:], `\`)
	}
	return strings.Join(parts, `\`)
}

// SPEC 27.3 fixes the filesystem layout in Linux FHS terms, and a BSD
// does not use those paths for anything. `/usr/local/etc` is where a
// port's configuration lives, `/var/db` is durable state, and `/run`
// does not exist. Following the specification literally there would put
// halite's files in three places no BSD administrator looks, and it did:
// the rc.d scripts in contrib/ already say `/usr/local/etc/halite`,
// which meant a service and a hand-run command read different
// configuration on the one platform this build is verified on.
//
// The specification's paths are the Linux ones. These are the same
// layout expressed in the local convention.

// windowsRoot is where SPEC 27.3 puts configuration on Windows:
// %PROGRAMDATA%\Halite, which is the location the .msi registers and the
// only one an administrator would think to look in.
//
// Without this the FHS paths were used literally there — filepath.Join
// turns "/etc/halite" into "\etc\halite" — so a Windows node kept its
// configuration, its keys, and its cache in three directories off the
// root of whichever drive the process started on.
func windowsRoot() string {
	if dir := os.Getenv("PROGRAMDATA"); dir != "" {
		return winJoin(dir, "Halite")
	}
	// PROGRAMDATA is set on every supported Windows, but a service
	// started with a scrubbed environment is not worth crashing over.
	return winJoin(`C:\ProgramData`, "Halite")
}

// isBSD reports whether this platform uses the /usr/local hierarchy for
// ports and packages. macOS is excluded: Homebrew's prefix is not fixed,
// so /etc is the honest default there.
func isBSD() bool { return isBSDFor(runtime.GOOS) }

func isBSDFor(goos string) bool {
	switch goos {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return true
	}
	return false
}

// prefix is where packaged configuration lives.
func prefix() string { return prefixFor(runtime.GOOS) }

func prefixFor(goos string) string {
	if isBSDFor(goos) {
		return "/usr/local/etc"
	}
	return "/etc"
}

// DefaultRoot is where packaged configuration lives. SPEC section 27.3.
var DefaultRoot = RootFor(runtime.GOOS)

// RootFor is the configuration root a given platform uses.
//
// Takes the platform rather than reading runtime.GOOS so that the whole
// layout can be checked for every target from one host. Three of the
// four platforms here have never had this build run on them, and a
// layout that can only be verified by booting the platform is a layout
// that stays unverified.
func RootFor(goos string) string {
	if goos == "windows" {
		return windowsRoot()
	}
	return posixJoin(prefixFor(goos), "halite")
}

// DefaultPKIDir, DefaultStateDir, DefaultCacheDir, and DefaultSocketDir
// are the rest of 27.3's layout, in the local convention.
var (
	DefaultPKIDir    = filepath.Join(DefaultRoot, "pki")
	DefaultPolicy    = filepath.Join(DefaultRoot, "policy.yaml")
	DefaultCacheDir  = varPath("cache")
	DefaultStateDir  = varPath("lib")
	DefaultLogDir    = varPath("log")
	DefaultSocketDir = runPath()
)

// varPath maps FHS's /var/<kind>/halite onto the local convention. A BSD
// keeps durable state in /var/db and has no /var/lib at all.
func varPath(kind string) string { return VarPathFor(runtime.GOOS, kind) }

// VarPathFor is /var/<kind>/halite in the local convention.
func VarPathFor(goos, kind string) string {
	if goos == "windows" {
		// Windows has no /var. Everything lives under the one root the
		// .msi creates and the administrator already knows about.
		return winJoin(windowsRoot(), kind)
	}
	if isBSDFor(goos) && kind == "lib" {
		return "/var/db/halite"
	}
	return posixJoin("/var", kind, "halite")
}

// runPath is /run on Linux and /var/run on a BSD.
func runPath() string { return RunPathFor(runtime.GOOS) }

// RunPathFor is where a platform keeps its runtime sockets.
func RunPathFor(goos string) string {
	if goos == "windows" {
		return winJoin(windowsRoot(), "run")
	}
	if isBSDFor(goos) {
		return "/var/run/halite"
	}
	return "/run/halite"
}

// pathTokens map a resolved platform path back to the portable name the
// generated documentation uses, so that a document generated on FreeBSD
// and one generated on Linux are the same document.
var pathTokens = []struct{ path, token string }{
	{DefaultRoot, "<config root>"},
	{DefaultStateDir, "<state dir>"},
	{DefaultCacheDir, "<cache dir>"},
	{DefaultSocketDir, "<socket dir>"},
	{DefaultLogDir, "<log dir>"},
}

// PortablePath renders a default path without the platform in it.
//
// Exact matches are taken before prefixes, and the longest prefix wins.
// On Windows every one of these directories is a child of the
// configuration root, so a first-match-wins scan rendered the cache
// directory as `<config root>\cache` and the document generated there
// differed from the one generated anywhere else. The remainder is
// normalised to forward slashes for the same reason: the token stands
// for a path on every platform, so the text after it must not name one.
func PortablePath(p string) string {
	for _, t := range pathTokens {
		if p == t.path {
			return t.token
		}
	}
	best, bestToken := "", ""
	for _, t := range pathTokens {
		// Both separators, because these paths are the host's and the
		// host may be Windows, where a child of the config root is
		// spelled with a backslash.
		if strings.HasPrefix(p, t.path+"/") || strings.HasPrefix(p, t.path+`\`) {
			if len(t.path) > len(best) {
				best, bestToken = t.path, t.token
			}
		}
	}
	if best == "" {
		return p
	}
	return bestToken + strings.ReplaceAll(p[len(best):], `\`, "/")
}

// PathTable describes what each token resolves to, for the documentation
// that uses them.
func PathTable() []struct{ Token, Linux, BSD string } {
	return []struct{ Token, Linux, BSD string }{
		{"<config root>", "/etc/halite", "/usr/local/etc/halite"},
		{"<state dir>", "/var/lib/halite", "/var/db/halite"},
		{"<cache dir>", "/var/cache/halite", "/var/cache/halite"},
		{"<socket dir>", "/run/halite", "/var/run/halite"},
		{"<log dir>", "/var/log/halite", "/var/log/halite"},
	}
}
