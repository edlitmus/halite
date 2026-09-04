package builtin

import (
	"os"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Chocolatey, the `choco` provider of SPEC section 15.2.
//
// Windows had no package provider at all, so `pkg.installed` on a
// Windows node answered "no package manager was found on this node
// (windows); halite ships providers for apt, dnf, yum, pkgng, apk, and
// mac_brew_pkg" -- a list with nothing on it for the platform SPEC 27.1
// puts in tier 1.
//
// Everything goes through --limit-output, which is Chocolatey's
// machine-readable mode: one record per line, fields separated by a
// vertical bar, no headers and no progress. SPEC 15.2 asks for exactly
// that, and it is why this is the provider that ships first: winget's
// `list` is a fixed-width table meant for a person to read, and parsing
// it by column breaks on the first long package name.

type chocoProvider struct{}

func (chocoProvider) Name() string { return "chocolatey" }

func (chocoProvider) Available(c *exec.Context) bool { return c.Which("choco") != "" }

// chocoEnv is the environment every choco invocation gets: the clean
// environment plus the one variable that tells Chocolatey where its
// package library is.
func chocoEnv() []string {
	return append(exec.CleanEnv(), "ChocolateyInstall="+chocolateyInstall())
}

// chocolateyInstall is where Chocolatey keeps its library. Read from the
// environment where the installer set it, with the documented default as
// the fallback, because a service started with a scrubbed environment
// still has to find the library.
func chocolateyInstall() string {
	if v := os.Getenv("ChocolateyInstall"); v != "" {
		return v
	}
	return `C:\ProgramData\chocolatey`
}

// chocoMajor is the installed Chocolatey's major version, or zero when
// it cannot be read.
//
// It matters because `choco list` changed meaning between the two: in
// 1.x it searches the configured feeds and needs --local-only to report
// what is installed, and in 2.x it reports what is installed and the
// flag was removed. Passing the wrong one either reports the whole
// community repository as installed, or fails on an unknown option.
func chocoMajor(c *exec.Context) int {
	res, err := c.Run(exec.Command{
		Argv: []string{"choco", "--version"}, Env: chocoEnv(), IgnoreExitCode: true,
	})
	if err != nil {
		return 0
	}
	v := strings.TrimSpace(res.Stdout)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '.'); i > 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// chocoListArgv is the command that reports installed packages, in the
// spelling this Chocolatey understands.
func chocoListArgv(c *exec.Context) []string {
	argv := []string{"choco", "list", "--limit-output"}
	if chocoMajor(c) < 2 {
		argv = append(argv, "--local-only")
	}
	return argv
}

// parseChocoRecords reads Chocolatey's --limit-output form: one record
// per line, fields separated by a vertical bar. A line without one is a
// summary or a warning and is skipped.
func parseChocoRecords(stdout string) [][]string {
	var out [][]string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		out = append(out, strings.Split(line, "|"))
	}
	return out
}

func (chocoProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: chocoListArgv(c), Env: chocoEnv()})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(64)
	for _, f := range parseChocoRecords(res.Stdout) {
		if len(f) >= 2 && f[0] != "" {
			out.Set(f[0], f[1])
		}
	}
	return out, nil
}

// chocoInstallArgv is the invariant part of an install.
//
// -y because a state run has nobody to answer a prompt. --no-progress
// because the progress bar is thousands of carriage returns in a
// captured stdout. --ignore-checksums is deliberately absent: a package
// whose checksum does not match is a package that must not be installed.
func chocoInstallArgv() []string {
	return []string{"choco", "install", "-y", "--no-progress"}
}

func (chocoProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	// No refresh step. Chocolatey queries its feeds on every command and
	// keeps no local index, so there is nothing a refresh could do.
	var pinnedNames []string
	pinned := map[string]string{}
	var plain []string
	for _, n := range names {
		if v, ok := versions[n]; ok && v != "" && !strings.HasSuffix(v, "*") {
			pinned[n] = v
			pinnedNames = append(pinnedNames, n)
			continue
		}
		plain = append(plain, n)
	}
	// --version applies to every package on the command line, so a
	// pinned one is installed on its own.
	for _, n := range pinnedNames {
		argv := append(chocoInstallArgv(), n, "--version", pinned[n])
		if _, err := c.Run(exec.Command{Argv: argv, Env: chocoEnv()}); err != nil {
			return err
		}
	}
	if len(plain) == 0 {
		return nil
	}
	_, err := c.Run(exec.Command{Argv: append(chocoInstallArgv(), plain...), Env: chocoEnv()})
	return err
}

func (chocoProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := []string{"choco", "uninstall", "-y", "--no-progress"}
	if purge {
		// The closest thing Chocolatey has to a purge: an uninstall that
		// also takes the dependencies the package brought with it.
		argv = append(argv, "--remove-dependencies")
	}
	argv = append(argv, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: chocoEnv()})
	return err
}

func (chocoProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"choco", "search", name, "--exact", "--limit-output"},
		Env:            chocoEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	for _, f := range parseChocoRecords(res.Stdout) {
		if len(f) >= 2 && strings.EqualFold(f[0], name) {
			return f[1], nil
		}
	}
	return "", nil
}

// RefreshDB does nothing, and reports success.
//
// Chocolatey has no local package index: every command queries the
// configured feeds. There is nothing to refresh, so this does not run a
// command that would look like a refresh and not be one.
func (chocoProvider) RefreshDB(c *exec.Context) error { return nil }

// ---- the optional interfaces of pkg_more.go ----

func (chocoProvider) Hold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{
		Argv: []string{"choco", "pin", "add", "--name", name}, Env: chocoEnv(),
	})
	return err
}

func (chocoProvider) Unhold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{
		Argv: []string{"choco", "pin", "remove", "--name", name}, Env: chocoEnv(),
	})
	return err
}

func (chocoProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"choco", "pin", "list", "--limit-output"}, Env: chocoEnv(),
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range parseChocoRecords(res.Stdout) {
		if len(f) >= 1 && f[0] != "" {
			out = append(out, f[0])
		}
	}
	return out, nil
}

func (p chocoProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	if _, err := c.Run(exec.Command{
		Argv: []string{"choco", "upgrade", "all", "-y", "--no-progress"}, Env: chocoEnv(),
	}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (chocoProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	// `choco outdated` exits non-zero when it finds outdated packages,
	// which is its report and not a failure.
	res, err := c.Run(exec.Command{
		Argv:           []string{"choco", "outdated", "--limit-output"},
		Env:            chocoEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(16)
	for _, f := range parseChocoRecords(res.Stdout) {
		// name|installed|available|pinned
		if len(f) < 3 || f[0] == "" {
			continue
		}
		if len(f) >= 4 && strings.EqualFold(strings.TrimSpace(f[3]), "true") {
			// A pinned package is not an upgrade available to this
			// machine. Listing it as one makes pkg.list_upgrades
			// disagree with what pkg.upgrade would actually do.
			continue
		}
		out.Set(f[0], f[2])
	}
	return out, nil
}

func (chocoProvider) ListRepos(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"choco", "source", "list", "--limit-output"}, Env: chocoEnv(),
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(8)
	for _, f := range parseChocoRecords(res.Stdout) {
		// name|url|disabled|user|certificate|priority|bypassProxy|
		// allowSelfService|visibleToAdminsOnly
		if len(f) < 3 || f[0] == "" {
			continue
		}
		entry := value.MapOf(
			"url", f[1],
			"enabled", !strings.EqualFold(strings.TrimSpace(f[2]), "true"),
		)
		if len(f) >= 6 {
			if n, err := strconv.Atoi(strings.TrimSpace(f[5])); err == nil {
				entry.Set("priority", int64(n))
			}
		}
		out.Set(f[0], entry)
	}
	return out, nil
}
