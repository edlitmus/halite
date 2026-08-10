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

// pkgBackend abstracts a platform package manager.
type pkgBackend struct {
	name      string
	installed func(pkg string) bool
	install   func(pkgs []string) (string, error)
	remove    func(pkgs []string) (string, error)
}

func cmdBackend(name string, checkArgv func(p string) []string, checkOK func(out string, rc int) bool,
	installArgv, removeArgv func(pkgs []string) []string) *pkgBackend {
	runArgv := func(argv []string) (string, error) {
		out, errOut, rc, err := run(argv[0], argv[1:]...)
		if err != nil {
			return out + errOut, err
		}
		if rc != 0 {
			return out + errOut, fmt.Errorf("%s exited %d: %s", argv[0], rc, strings.TrimSpace(errOut))
		}
		return out + errOut, nil
	}
	return &pkgBackend{
		name: name,
		installed: func(p string) bool {
			argv := checkArgv(p)
			out, _, rc, err := run(argv[0], argv[1:]...)
			if err != nil {
				return false
			}
			return checkOK(out, rc)
		},
		install: func(pkgs []string) (string, error) { return runArgv(installArgv(pkgs)) },
		remove:  func(pkgs []string) (string, error) { return runArgv(removeArgv(pkgs)) },
	}
}

func rcZero(_ string, rc int) bool { return rc == 0 }

// detectPkgBackend picks the package manager for the current platform.
// FreeBSD pkg(8) is checked first-class; Linux families, macOS Homebrew,
// and Windows Chocolatey/winget are covered behind it.
func detectPkgBackend() (*pkgBackend, error) {
	switch runtime.GOOS {
	case "freebsd":
		return cmdBackend("pkg",
			func(p string) []string { return []string{"pkg", "info", "-e", p} }, rcZero,
			func(pkgs []string) []string { return append([]string{"pkg", "install", "-y"}, pkgs...) },
			func(pkgs []string) []string { return append([]string{"pkg", "delete", "-y"}, pkgs...) },
		), nil
	case "darwin":
		if has("brew") {
			return cmdBackend("brew",
				func(p string) []string { return []string{"brew", "list", "--versions", p} }, rcZero,
				func(pkgs []string) []string { return append([]string{"brew", "install"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"brew", "uninstall"}, pkgs...) },
			), nil
		}
		return nil, fmt.Errorf("no package manager found (install Homebrew)")
	case "windows":
		if has("choco") {
			return cmdBackend("choco",
				func(p string) []string { return []string{"choco", "list", "--exact", "--limit-output", p} },
				func(out string, rc int) bool { return rc == 0 && strings.Contains(out, "|") },
				func(pkgs []string) []string { return append([]string{"choco", "install", "-y"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"choco", "uninstall", "-y"}, pkgs...) },
			), nil
		}
		if has("winget") {
			return cmdBackend("winget",
				func(p string) []string { return []string{"winget", "list", "--exact", "--id", p} }, rcZero,
				func(pkgs []string) []string {
					return append([]string{"winget", "install", "--exact", "--silent",
						"--accept-package-agreements", "--accept-source-agreements", "--id"}, pkgs...)
				},
				func(pkgs []string) []string {
					return append([]string{"winget", "uninstall", "--exact", "--id"}, pkgs...)
				},
			), nil
		}
		return nil, fmt.Errorf("no package manager found (install Chocolatey or winget)")
	default: // linux and friends
		switch {
		case has("apt-get"):
			return cmdBackend("apt",
				func(p string) []string { return []string{"dpkg-query", "-W", "-f=${Status}", p} },
				func(out string, rc int) bool { return rc == 0 && strings.Contains(out, "install ok installed") },
				func(pkgs []string) []string {
					return append([]string{"apt-get", "install", "-y", "--no-install-recommends"}, pkgs...)
				},
				func(pkgs []string) []string { return append([]string{"apt-get", "remove", "-y"}, pkgs...) },
			), nil
		case has("dnf"):
			return cmdBackend("dnf",
				func(p string) []string { return []string{"rpm", "-q", p} }, rcZero,
				func(pkgs []string) []string { return append([]string{"dnf", "install", "-y"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"dnf", "remove", "-y"}, pkgs...) },
			), nil
		case has("yum"):
			return cmdBackend("yum",
				func(p string) []string { return []string{"rpm", "-q", p} }, rcZero,
				func(pkgs []string) []string { return append([]string{"yum", "install", "-y"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"yum", "remove", "-y"}, pkgs...) },
			), nil
		case has("zypper"):
			return cmdBackend("zypper",
				func(p string) []string { return []string{"rpm", "-q", p} }, rcZero,
				func(pkgs []string) []string { return append([]string{"zypper", "-n", "install"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"zypper", "-n", "remove"}, pkgs...) },
			), nil
		case has("pacman"):
			return cmdBackend("pacman",
				func(p string) []string { return []string{"pacman", "-Qi", p} }, rcZero,
				func(pkgs []string) []string { return append([]string{"pacman", "-S", "--noconfirm"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"pacman", "-R", "--noconfirm"}, pkgs...) },
			), nil
		case has("apk"):
			return cmdBackend("apk",
				func(p string) []string { return []string{"apk", "info", "-e", p} }, rcZero,
				func(pkgs []string) []string { return append([]string{"apk", "add"}, pkgs...) },
				func(pkgs []string) []string { return append([]string{"apk", "del"}, pkgs...) },
			), nil
		}
		return nil, fmt.Errorf("no supported package manager found")
	}
}

func pkgNames(id string, args map[string]any) []string {
	if pkgs := List(args, "pkgs"); len(pkgs) > 0 {
		return pkgs
	}
	return []string{Str(args, "name", id)}
}

func pkgInstalled(c *Ctx, id string, args map[string]any) Result {
	be, err := detectPkgBackend()
	if err != nil {
		return resFail("%v", err)
	}
	names := pkgNames(id, args)
	var missing []string
	for _, n := range names {
		if !be.installed(n) {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return resOK(fmt.Sprintf("all packages already installed (%s)", strings.Join(names, ", ")))
	}
	if c.Test {
		return resWould(fmt.Sprintf("would install via %s: %s", be.name, strings.Join(missing, ", ")))
	}
	if _, err := be.install(missing); err != nil {
		return resFail("install failed: %v", err)
	}
	changes := map[string]string{}
	for _, n := range missing {
		changes[n] = "installed"
	}
	return resChanged(fmt.Sprintf("installed via %s: %s", be.name, strings.Join(missing, ", ")), changes)
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
