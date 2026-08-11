package modules

import (
	"fmt"
	"runtime"
	"strings"
)

func init() {
	register("pkg.installed", pkgInstalled)
	register("pkg.removed", pkgRemoved)
}

// pkgBackend abstracts a platform package manager. The optional fields are
// nil where the tool has no equivalent, and the states report that rather
// than pretending: a version that silently did not pin would be worse than
// a failure.
type pkgBackend struct {
	name      string
	installed func(pkg string) bool
	version   func(pkg string) string // installed version, "" if unknown
	install   func(pkgs []string) (string, error)
	remove    func(pkgs []string) (string, error)
	// pin renders "this package at this version" in the backend's own
	// syntax, for install to consume.
	pin    func(pkg, version string) string
	hold   func(pkg string) error
	unhold func(pkg string) error
	held   func(pkg string) bool
}

// pkgRun runs a package manager command and returns its combined output.
func pkgRun(argv ...string) (string, error) {
	out, errOut, rc, err := run(argv[0], argv[1:]...)
	if err != nil {
		return out + errOut, err
	}
	if rc != 0 {
		return out + errOut, fmt.Errorf("%s exited %d: %s", argv[0], rc, strings.TrimSpace(errOut))
	}
	return out + errOut, nil
}

// pkgQuery runs a query and reports its trimmed output and whether it
// succeeded.
func pkgQuery(argv ...string) (string, bool) {
	out, _, rc, err := run(argv[0], argv[1:]...)
	return strings.TrimSpace(out), err == nil && rc == 0
}

func pkgOK(argv ...string) func(string) bool {
	return func(p string) bool {
		_, ok := pkgQuery(argvWith(argv, p)...)
		return ok
	}
}

// argvWith appends one argument to a fixed command line without aliasing
// the template slice.
func argvWith(argv []string, arg string) []string {
	out := make([]string, len(argv), len(argv)+1)
	copy(out, argv)
	return append(out, arg)
}

func pkgInstaller(argv ...string) func([]string) (string, error) {
	return func(pkgs []string) (string, error) {
		return pkgRun(append(append([]string{}, argv...), pkgs...)...)
	}
}

// listedHold reports a hold by looking for the package name in the output
// of the backend's "what is held" command.
func listedHold(argv ...string) func(string) bool {
	return func(p string) bool {
		out, ok := pkgQuery(argv...)
		if !ok {
			return false
		}
		for _, line := range strings.Split(out, "\n") {
			for _, field := range strings.Fields(line) {
				if field == p || strings.HasPrefix(field, p+"-") || strings.HasPrefix(field, p+":") {
					return true
				}
			}
		}
		return false
	}
}

func holdCmd(argv ...string) func(string) error {
	return func(p string) error {
		_, err := pkgRun(argvWith(argv, p)...)
		return err
	}
}

// detectPkgBackend picks the package manager for the current platform.
// FreeBSD pkg(8) is checked first-class; Linux families, macOS Homebrew,
// and Windows Chocolatey/winget are covered behind it.
func detectPkgBackend() (*pkgBackend, error) {
	switch runtime.GOOS {
	case "freebsd":
		return &pkgBackend{
			name:      "pkg",
			installed: pkgOK("pkg", "info", "-e"),
			version: func(p string) string {
				out, ok := pkgQuery("pkg", "query", "%v", p)
				if !ok {
					return ""
				}
				return out
			},
			install: pkgInstaller("pkg", "install", "-y"),
			remove:  pkgInstaller("pkg", "delete", "-y"),
			pin:     func(p, v string) string { return p + "-" + v },
			hold:    holdCmd("pkg", "lock", "-y"),
			unhold:  holdCmd("pkg", "unlock", "-y"),
			held: func(p string) bool {
				out, ok := pkgQuery("pkg", "query", "%k", p)
				return ok && out == "1"
			},
		}, nil
	case "darwin":
		if has("brew") {
			return &pkgBackend{
				name:      "brew",
				installed: pkgOK("brew", "list", "--versions"),
				version: func(p string) string {
					out, ok := pkgQuery("brew", "list", "--versions", p)
					if !ok {
						return ""
					}
					if f := strings.Fields(out); len(f) > 1 {
						return f[len(f)-1]
					}
					return ""
				},
				install: pkgInstaller("brew", "install"),
				remove:  pkgInstaller("brew", "uninstall"),
				hold:    holdCmd("brew", "pin"),
				unhold:  holdCmd("brew", "unpin"),
				held:    listedHold("brew", "list", "--pinned"),
			}, nil
		}
		return nil, fmt.Errorf("no package manager found (install Homebrew)")
	case "windows":
		if has("choco") {
			return &pkgBackend{
				name: "choco",
				installed: func(p string) bool {
					out, ok := pkgQuery("choco", "list", "--exact", "--limit-output", p)
					return ok && strings.Contains(out, "|")
				},
				version: func(p string) string {
					out, ok := pkgQuery("choco", "list", "--exact", "--limit-output", p)
					if !ok {
						return ""
					}
					_, version, found := strings.Cut(strings.TrimSpace(out), "|")
					if !found {
						return ""
					}
					return strings.TrimSpace(version)
				},
				install: pkgInstaller("choco", "install", "-y"),
				remove:  pkgInstaller("choco", "uninstall", "-y"),
				hold:    func(p string) error { _, err := pkgRun("choco", "pin", "add", "-n="+p); return err },
				unhold:  func(p string) error { _, err := pkgRun("choco", "pin", "remove", "-n="+p); return err },
				held:    listedHold("choco", "pin", "list", "-r"),
			}, nil
		}
		if has("winget") {
			return &pkgBackend{
				name:      "winget",
				installed: pkgOK("winget", "list", "--exact", "--id"),
				install: pkgInstaller("winget", "install", "--exact", "--silent",
					"--accept-package-agreements", "--accept-source-agreements", "--id"),
				remove: pkgInstaller("winget", "uninstall", "--exact", "--id"),
			}, nil
		}
		return nil, fmt.Errorf("no package manager found (install Chocolatey or winget)")
	default: // linux and friends
		switch {
		case has("apt-get"):
			return &pkgBackend{
				name: "apt",
				installed: func(p string) bool {
					out, ok := pkgQuery("dpkg-query", "-W", "-f=${Status}", p)
					return ok && strings.Contains(out, "install ok installed")
				},
				version: func(p string) string {
					out, ok := pkgQuery("dpkg-query", "-W", "-f=${Version}", p)
					if !ok {
						return ""
					}
					return out
				},
				install: pkgInstaller("apt-get", "install", "-y", "--no-install-recommends"),
				remove:  pkgInstaller("apt-get", "remove", "-y"),
				pin:     func(p, v string) string { return p + "=" + v },
				hold:    holdCmd("apt-mark", "hold"),
				unhold:  holdCmd("apt-mark", "unhold"),
				held:    listedHold("apt-mark", "showhold"),
			}, nil
		case has("dnf"):
			return rpmBackend("dnf", pkgInstaller("dnf", "install", "-y"), pkgInstaller("dnf", "remove", "-y"),
				holdCmd("dnf", "versionlock", "add"), holdCmd("dnf", "versionlock", "delete"),
				listedHold("dnf", "versionlock", "list")), nil
		case has("yum"):
			return rpmBackend("yum", pkgInstaller("yum", "install", "-y"), pkgInstaller("yum", "remove", "-y"),
				holdCmd("yum", "versionlock", "add"), holdCmd("yum", "versionlock", "delete"),
				listedHold("yum", "versionlock", "list")), nil
		case has("zypper"):
			be := rpmBackend("zypper", pkgInstaller("zypper", "-n", "install"), pkgInstaller("zypper", "-n", "remove"),
				holdCmd("zypper", "addlock"), holdCmd("zypper", "removelock"),
				listedHold("zypper", "locks"))
			be.pin = func(p, v string) string { return p + "=" + v }
			return be, nil
		case has("pacman"):
			return &pkgBackend{
				name:      "pacman",
				installed: pkgOK("pacman", "-Qi"),
				version: func(p string) string {
					out, ok := pkgQuery("pacman", "-Q", p)
					if !ok {
						return ""
					}
					if f := strings.Fields(out); len(f) > 1 {
						return f[1]
					}
					return ""
				},
				install: pkgInstaller("pacman", "-S", "--noconfirm"),
				remove:  pkgInstaller("pacman", "-R", "--noconfirm"),
			}, nil
		case has("apk"):
			return &pkgBackend{
				name:      "apk",
				installed: pkgOK("apk", "info", "-e"),
				version: func(p string) string {
					out, ok := pkgQuery("apk", "list", "--installed", p)
					if !ok || out == "" {
						return ""
					}
					// "nginx-1.24.0-r7 x86_64 {nginx} (2-clause BSD) [installed]"
					name := strings.Fields(out)[0]
					return strings.TrimPrefix(name, p+"-")
				},
				install: pkgInstaller("apk", "add"),
				remove:  pkgInstaller("apk", "del"),
				pin:     func(p, v string) string { return p + "=" + v },
			}, nil
		}
		return nil, fmt.Errorf("no supported package manager found")
	}
}

// rpmBackend builds the shape shared by dnf, yum, and zypper: rpm answers
// the queries, the tool does the work.
func rpmBackend(name string, install, remove func([]string) (string, error),
	hold, unhold func(string) error, held func(string) bool) *pkgBackend {
	return &pkgBackend{
		name:      name,
		installed: pkgOK("rpm", "-q"),
		version: func(p string) string {
			out, ok := pkgQuery("rpm", "-q", "--qf", "%{VERSION}-%{RELEASE}", p)
			if !ok {
				return ""
			}
			return out
		},
		install: install,
		remove:  remove,
		pin:     func(p, v string) string { return p + "-" + v },
		hold:    hold,
		unhold:  unhold,
		held:    held,
	}
}

func pkgNames(id string, args map[string]any) []string {
	if pkgs := List(args, "pkgs"); len(pkgs) > 0 {
		return pkgs
	}
	return []string{Str(args, "name", id)}
}

// pkgInstalled ensures packages are installed, optionally at a given
// version and optionally held there.
//
//	nginx:
//	  pkg.installed:
//	    - version: 1.24.0
//	    - hold: true
func pkgInstalled(c *Ctx, id string, args map[string]any) Result {
	be, err := detectPkgBackend()
	if err != nil {
		return resFail("%v", err)
	}
	names := pkgNames(id, args)
	wantVersion := Str(args, "version", "")
	if err := pinnable(be, names, wantVersion); err != nil {
		return resFail("%v", err)
	}

	var missing []string
	for _, n := range names {
		if !be.installed(n) || versionDrift(be, n, wantVersion) {
			missing = append(missing, n)
		}
	}
	specs := missing
	if wantVersion != "" && len(missing) > 0 {
		specs = []string{be.pin(missing[0], wantVersion)}
	}
	toHold, wantHeld, err := holdDrift(be, args, names)
	if err != nil {
		return resFail("%v", err)
	}

	if len(missing) == 0 && len(toHold) == 0 {
		return resOK(fmt.Sprintf("all packages already installed (%s)", strings.Join(names, ", ")))
	}
	if c.Test {
		return resWould(strings.Join(pkgPlan(be, specs, toHold, wantHeld, true), "; "))
	}

	changes := map[string]string{}
	if len(specs) > 0 {
		if _, err := be.install(specs); err != nil {
			return resFail("install failed: %v", err)
		}
		for _, n := range missing {
			changes[n] = "installed"
			if wantVersion != "" {
				changes[n] = "installed " + wantVersion
			}
		}
	}
	for _, n := range toHold {
		apply := be.hold
		if !wantHeld {
			apply = be.unhold
		}
		if err := apply(n); err != nil {
			return resFail("hold %s: %v", n, err)
		}
		changes[n+" hold"] = holdPhrase(wantHeld, false)
	}
	return resChanged(strings.Join(pkgPlan(be, specs, toHold, wantHeld, false), "; "), changes)
}

// pinnable reports whether a version request can be honoured. A pin the
// backend cannot express has to fail: installing the current version
// instead would be the wrong package, quietly.
func pinnable(be *pkgBackend, names []string, version string) error {
	if version == "" {
		return nil
	}
	if len(names) > 1 {
		return fmt.Errorf("version applies to a single package; declare one state per pinned package")
	}
	if be.pin == nil {
		return fmt.Errorf("version pinning is not implemented for %s", be.name)
	}
	return nil
}

// pkgPlan renders what the state is about to do, or has just done, as one
// phrase per kind of work.
func pkgPlan(be *pkgBackend, specs, holds []string, wantHeld, pending bool) []string {
	var out []string
	if len(specs) > 0 {
		verb := "installed"
		if pending {
			verb = "would install"
		}
		out = append(out, fmt.Sprintf("%s via %s: %s", verb, be.name, strings.Join(specs, ", ")))
	}
	if len(holds) > 0 {
		out = append(out, fmt.Sprintf("%s via %s: %s",
			holdPhrase(wantHeld, pending), be.name, strings.Join(holds, ", ")))
	}
	return out
}

// holdPhrase names the hold action in the tense the caller is writing in.
func holdPhrase(want, pending bool) string {
	switch {
	case want && pending:
		return "would hold"
	case want:
		return "held"
	case pending:
		return "would release"
	default:
		return "released"
	}
}

// versionDrift reports whether an installed package is at the wrong
// version. A backend that cannot report versions never drifts, so a pin is
// applied once and then trusted.
func versionDrift(be *pkgBackend, pkg, want string) bool {
	if want == "" || be.version == nil {
		return false
	}
	have := be.version(pkg)
	return have != "" && have != want
}

// holdDrift returns the packages whose hold state does not match the one
// the state declares. A state that says nothing about holds never touches
// them, so an existing hold is left alone.
func holdDrift(be *pkgBackend, args map[string]any, names []string) (drifted []string, want bool, err error) {
	if _, declared := args["hold"]; !declared {
		return nil, false, nil
	}
	want = Bool(args, "hold", false)
	if be.hold == nil || be.unhold == nil || be.held == nil {
		return nil, false, fmt.Errorf("package holds are not implemented for %s", be.name)
	}
	for _, n := range names {
		if be.held(n) != want {
			drifted = append(drifted, n)
		}
	}
	return drifted, want, nil
}

func pkgRemoved(c *Ctx, id string, args map[string]any) Result {
	be, err := detectPkgBackend()
	if err != nil {
		return resFail("%v", err)
	}
	names := pkgNames(id, args)
	var present []string
	for _, n := range names {
		if be.installed(n) {
			present = append(present, n)
		}
	}
	if len(present) == 0 {
		return resOK(fmt.Sprintf("all packages already absent (%s)", strings.Join(names, ", ")))
	}
	if c.Test {
		return resWould(fmt.Sprintf("would remove via %s: %s", be.name, strings.Join(present, ", ")))
	}
	if _, err := be.remove(present); err != nil {
		return resFail("remove failed: %v", err)
	}
	changes := map[string]string{}
	for _, n := range present {
		changes[n] = "removed"
	}
	return resChanged(fmt.Sprintf("removed via %s: %s", be.name, strings.Join(present, ", ")), changes)
}
