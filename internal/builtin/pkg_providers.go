package builtin

import (
	"fmt"
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
