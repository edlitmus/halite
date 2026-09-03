package builtin

import (
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Each provider speaks to its package manager through the manager's own
// binary, in a machine-readable output mode and with a non-interactive
// environment, never through a C library binding. SPEC section 15.2.

// aptEnv is the environment every apt invocation gets: non-interactive, so
// a configuration file prompt cannot hang a state run forever.
func aptEnv() []string {
	return append(exec.CleanEnv(),
		"DEBIAN_FRONTEND=noninteractive",
		"APT_LISTCHANGES_FRONTEND=none",
		"UCF_FORCE_CONFFOLD=1",
	)
}

type aptProvider struct{}

func (aptProvider) Name() string { return "aptpkg" }

func (aptProvider) Available(c *exec.Context) bool {
	return c.Which("dpkg-query") != "" && c.Which("apt-get") != ""
}

func (aptProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n"},
		Env:  aptEnv(),
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		// Only packages that are actually installed count; dpkg lists
		// removed-but-configured ones too, and treating those as present
		// makes a pkg.installed state a no-op forever.
		if !strings.HasSuffix(fields[2], " installed") {
			continue
		}
		out.Set(fields[0], fields[1])
	}
	return out, nil
}

func (aptProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"apt-get", "update", "-q"}, Env: aptEnv()}); err != nil {
			return fmt.Errorf("apt-get update: %w", err)
		}
	}
	argv := []string{"apt-get", "install", "-y", "-q",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "Dpkg::Options::=--force-confdef"}
	for _, n := range names {
		if v, ok := versions[n]; ok && v != "" && !strings.HasSuffix(v, "*") {
			argv = append(argv, n+"="+v)
			continue
		}
		argv = append(argv, n)
	}
	_, err := c.Run(exec.Command{Argv: argv, Env: aptEnv()})
	return err
}

func (aptProvider) Remove(c *exec.Context, names []string, purge bool) error {
	verb := "remove"
	if purge {
		verb = "purge"
	}
	argv := append([]string{"apt-get", verb, "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: aptEnv()})
	return err
}

func (aptProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"apt-cache", "policy", name},
		Env:            aptEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Candidate:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Candidate:"))
			if v == "(none)" {
				return "", nil
			}
			return v, nil
		}
	}
	return "", nil
}

func (aptProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"apt-get", "update", "-q"}, Env: aptEnv()})
	return err
}

// dnfProvider covers both dnf and yum, whose command surfaces are the same
// for what halite needs.
type dnfProvider struct{ binary string }

func (p dnfProvider) Name() string { return p.binary + "pkg" }

func (p dnfProvider) Available(c *exec.Context) bool {
	return c.Which("rpm") != "" && c.Which(p.binary) != ""
}

func (p dnfProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"rpm", "-qa", "--queryformat", "%{NAME}\\t%{EPOCH}:%{VERSION}-%{RELEASE}\\n"},
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, version, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		// rpm writes "(none)" for a missing epoch; an epoch of zero is
		// conventionally omitted, and keeping the literal string would
		// make every version comparison fail.
		version = strings.TrimPrefix(version, "(none):")
		version = strings.TrimPrefix(version, "0:")
		out.Set(name, version)
	}
	return out, nil
}

func (p dnfProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	argv := []string{p.binary, "install", "-y", "-q"}
	if !refresh {
		argv = append(argv, "-C")
	}
	for _, n := range names {
		if v, ok := versions[n]; ok && v != "" && !strings.HasSuffix(v, "*") {
			argv = append(argv, n+"-"+v)
			continue
		}
		argv = append(argv, n)
	}
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (p dnfProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := append([]string{p.binary, "remove", "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (p dnfProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{p.binary, "--quiet", "list", "available", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(fields[0], name+".") {
			return strings.TrimPrefix(fields[1], "0:"), nil
		}
	}
	return "", nil
}

func (p dnfProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{p.binary, "makecache", "-q"}})
	return err
}

// pkgngProvider is FreeBSD's pkg.
type pkgngProvider struct{}

func (pkgngProvider) Name() string { return "pkgng" }

func (pkgngProvider) Available(c *exec.Context) bool { return c.Which("pkg") != "" }

func (pkgngProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"pkg", "query", "%n\\t%v"}})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, version, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		out.Set(name, version)
	}
	return out, nil
}

func (pkgngProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"pkg", "update", "-q"}}); err != nil {
			return fmt.Errorf("pkg update: %w", err)
		}
	}
	argv := append([]string{"pkg", "install", "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: append(exec.CleanEnv(), "ASSUME_ALWAYS_YES=YES")})
	return err
}

func (pkgngProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := append([]string{"pkg", "delete", "-y", "-q"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: append(exec.CleanEnv(), "ASSUME_ALWAYS_YES=YES")})
	return err
}

func (pkgngProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"pkg", "rquery", "%v", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(firstLine(res.Stdout)), nil
}

func (pkgngProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"pkg", "update", "-q"}})
	return err
}

// apkProvider is Alpine's apk.
type apkProvider struct{}

func (apkProvider) Name() string { return "apkpkg" }

func (apkProvider) Available(c *exec.Context) bool { return c.Which("apk") != "" }

func (apkProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"apk", "info", "-v"}})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(128)
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// apk prints name-version-release; the name ends at the last two
		// hyphen-separated fields.
		parts := strings.Split(line, "-")
		if len(parts) < 3 {
			continue
		}
		name := strings.Join(parts[:len(parts)-2], "-")
		version := strings.Join(parts[len(parts)-2:], "-")
		out.Set(name, version)
	}
	return out, nil
}

func (apkProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	argv := []string{"apk", "add", "--no-progress"}
	if refresh {
		argv = append(argv, "--update-cache")
	}
	argv = append(argv, names...)
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (apkProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := append([]string{"apk", "del", "--no-progress"}, names...)
	_, err := c.Run(exec.Command{Argv: argv})
	return err
}

func (apkProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"apk", "list", "--available", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	line := firstLine(res.Stdout)
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	parts := strings.Split(fields[0], "-")
	if len(parts) < 3 {
		return "", nil
	}
	return strings.Join(parts[len(parts)-2:], "-"), nil
}

func (apkProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"apk", "update", "--no-progress"}})
	return err
}

// brewEnv is the environment every Homebrew invocation gets: no
// auto-update on every command (that belongs to refresh_db alone), no
// post-install cleanup pass, and no interactive hints, so a state run gets
// only the output of the thing it asked for.
//
// Unlike every other provider here, brew refuses outright to run without
// $HOME set ("Error: $HOME must be set to run brew"), which CleanEnv does
// not carry — this was found by running the provider against a real
// Homebrew, not read off documentation.
func brewEnv() []string {
	env := append(exec.CleanEnv(),
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"HOMEBREW_NO_INSTALL_CLEANUP=1",
		"HOMEBREW_NO_ENV_HINTS=1",
	)
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}

// brewProvider is macOS's Homebrew, named mac_brew_pkg to match the module
// Salt trees already call by that name. SPEC section 15.2, 15.3.
type brewProvider struct{}

func (brewProvider) Name() string { return "mac_brew_pkg" }

func (brewProvider) Available(c *exec.Context) bool { return c.Which("brew") != "" }

func (brewProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"brew", "list", "--versions"}, Env: brewEnv()})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(256)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Homebrew can have more than one version of a formula on disk at
		// once, listed oldest first; only one is ever linked as current,
		// and the last field is that newest one.
		out.Set(fields[0], fields[len(fields)-1])
	}
	return out, nil
}

func (brewProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"brew", "update", "--quiet"}, Env: brewEnv()}); err != nil {
			return fmt.Errorf("brew update: %w", err)
		}
	}
	// Homebrew has no apt/dnf-style "name=version" pin at install time; a
	// version request is satisfied by installing by name and is enforced
	// afterward with brew.hold, the way pkgng's lock does.
	argv := append([]string{"brew", "install", "--quiet"}, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: brewEnv()})
	return err
}

func (brewProvider) Remove(c *exec.Context, names []string, purge bool) error {
	argv := []string{"brew", "uninstall", "--quiet"}
	if purge {
		// Homebrew's uninstall already removes a formula's Cellar
		// entirely; --force additionally takes every installed version
		// rather than just the linked one, which is the closest analogue
		// this package manager has to purge.
		argv = append(argv, "--force")
	}
	argv = append(argv, names...)
	_, err := c.Run(exec.Command{Argv: argv, Env: brewEnv()})
	return err
}

func (brewProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"brew", "info", "--json=v2", name},
		Env:            brewEnv(),
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", nil
	}
	f, err := firstBrewFormula(res.Stdout)
	if err != nil || f == nil {
		return "", err
	}
	versions, ok := mustMap(f, "versions")
	if !ok {
		return "", nil
	}
	stable, _ := versions.Get("stable")
	return value.KeyString(stable), nil
}

func (brewProvider) RefreshDB(c *exec.Context) error {
	_, err := c.Run(exec.Command{Argv: []string{"brew", "update", "--quiet"}, Env: brewEnv()})
	return err
}

// firstBrewFormula decodes a `brew info --json=v2` response and returns
// its first formula, or nil when the name matched none.
func firstBrewFormula(stdout string) (*value.Map, error) {
	v, err := value.DecodeJSON([]byte(stdout))
	if err != nil {
		return nil, err
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, nil
	}
	formulae, ok := m.Get("formulae")
	if !ok {
		return nil, nil
	}
	list, ok := formulae.([]any)
	if !ok || len(list) == 0 {
		return nil, nil
	}
	f, _ := list[0].(*value.Map)
	return f, nil
}

// ---- mac_brew_pkg: hold, upgrade, and the optional interfaces it can
// actually answer. FileList, OwnerOf, and ListRepos have no clean
// Homebrew analogue and are left unimplemented, the same as for every
// provider but pkgng. ----

func (brewProvider) Hold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"brew", "pin", name}, Env: brewEnv()})
	return err
}

func (brewProvider) Unhold(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"brew", "unpin", name}, Env: brewEnv()})
	return err
}

func (brewProvider) ListHolds(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv: []string{"brew", "list", "--pinned"}, Env: brewEnv(), IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
}

func (p brewProvider) Upgrade(c *exec.Context, refresh bool) (*value.Map, error) {
	before, err := p.ListPkgs(c)
	if err != nil {
		return nil, err
	}
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"brew", "update", "--quiet"}, Env: brewEnv()}); err != nil {
			return nil, fmt.Errorf("brew update: %w", err)
		}
	}
	if _, err := c.Run(exec.Command{Argv: []string{"brew", "upgrade", "--quiet"}, Env: brewEnv()}); err != nil {
		return nil, err
	}
	return pkgDelta(c, p, before)
}

func (brewProvider) ListUpgrades(c *exec.Context, refresh bool) (*value.Map, error) {
	if refresh {
		if _, err := c.Run(exec.Command{Argv: []string{"brew", "update", "--quiet"}, Env: brewEnv()}); err != nil {
			return nil, fmt.Errorf("brew update: %w", err)
		}
	}
	res, err := c.Run(exec.Command{
		Argv: []string{"brew", "outdated", "--json=v2"}, Env: brewEnv(), IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	v, err := value.DecodeJSON([]byte(res.Stdout))
	if err != nil {
		return nil, err
	}
	m, ok := v.(*value.Map)
	if !ok {
		return value.NewMap(0), nil
	}
	formulae, _ := m.Get("formulae")
	list, _ := formulae.([]any)
	out := value.NewMap(len(list))
	for _, item := range list {
		f, ok := item.(*value.Map)
		if !ok {
			continue
		}
		name, _ := f.Get("name")
		current, _ := f.Get("current_version")
		out.Set(value.KeyString(name), value.KeyString(current))
	}
	return out, nil
}
