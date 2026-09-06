package builtin

import (
	"fmt"
	"runtime"

	"github.com/edlitmus/halite/internal/exec"
)

// registerAliases makes SPEC 15.3's per-platform module names callable.
//
// The specification names both, and both are true. 15.2 has `pkg`,
// `service` and `sysctl` as virtual modules that pick a provider for the
// node they are on; 15.3 names `aptpkg`, `freebsdpkg`, `systemd_service`
// and the rest as modules of their own. This build implemented the first
// and left the second refusing by name, which is why DIVERGENCE 2.3 has
// carried an open question about it since the FreeBSD row was written:
// the behaviour those modules would provide is already here, inside the
// virtual ones, and whether the names should also exist was never
// decided. They should.
//
// An alias is a module name rather than a second set of functions.
// Resolving `aptpkg.install` to `pkg.install` keeps the counts honest:
// `pkg` has eighteen functions whether or not four platforms can each
// reach them under another name, and a build reporting seventy-two would
// be describing its own bookkeeping. It also means an alias cannot drift
// from what it aliases, which a copy would do the first time one of them
// gained an argument.
//
// Only the names whose provider actually exists are aliased. `zypperpkg`
// and `dnfpkg` stay pending, because SUSE has no provider here and the
// dnf one covers repositories but not packages: aliasing either would
// turn "not built" into "built, and fails when you call it", which is
// the worse of the two answers.
func registerAliases(r *Registries) {
	// The package providers, by the name SPEC 15.3 gives them. The
	// provider's own Name() is what it calls itself, which for two of
	// these is not the specification's spelling — pkgng is FreeBSD's
	// tool and `freebsdpkg` is the module, chocolatey is the tool and
	// `win_pkg` is the module.
	for alias, provider := range map[string]string{
		"aptpkg":       "aptpkg",
		"freebsdpkg":   "pkgng",
		"win_pkg":      "chocolatey",
		"mac_brew_pkg": "mac_brew_pkg",
	} {
		r.Exec.Alias(alias, exec.Alias{
			Module:   "pkg",
			Provider: provider,
			Usable:   pkgProviderIs(provider),
		})
	}

	for alias, provider := range map[string]string{
		"systemd_service": "systemd_service",
		"freebsd_service": "freebsd_service",
		"mac_service":     "mac_service",
	} {
		r.Exec.Alias(alias, exec.Alias{
			Module:   "service",
			Provider: provider,
			Usable:   serviceProviderIs(provider),
		})
	}

	// sysctl has no provider table: it is one implementation that reads
	// the platform's own tool, so the guard is the platform.
	r.Exec.Alias("ufw", exec.Alias{
		Module:   "firewall",
		Provider: "ufw",
		Usable:   firewallProviderIs("ufw"),
	})

	r.Exec.Alias("freebsd_sysctl", exec.Alias{
		Module:   "sysctl",
		Provider: "freebsd",
		Usable: func(*exec.Context) error {
			if runtime.GOOS != "freebsd" {
				return fmt.Errorf("freebsd_sysctl is the FreeBSD spelling of `sysctl`, and this node is %s; call `sysctl` instead",
					runtime.GOOS)
			}
			return nil
		},
	})
}

// pkgProviderIs refuses where the node's package manager is not the one
// the alias names.
//
// The message says which provider this node would use, because that is
// the fact the operator is missing: `aptpkg.install` on a RHEL node is
// not a typo and not an unbuilt module, it is the wrong module for the
// machine.
func pkgProviderIs(provider string) func(*exec.Context) error {
	return func(c *exec.Context) error {
		p, err := pickPkgProvider(c)
		if err != nil {
			return err
		}
		if p.Name() != provider {
			return fmt.Errorf("this is the %s spelling of `pkg`, and this node's package manager is %s; call `pkg` instead, which picks the right one",
				provider, p.Name())
		}
		return nil
	}
}

func serviceProviderIs(provider string) func(*exec.Context) error {
	return func(c *exec.Context) error {
		p, err := pickServiceProvider(c)
		if err != nil {
			return err
		}
		if p.Name() != provider {
			return fmt.Errorf("this is the %s spelling of `service`, and this node's init system is %s; call `service` instead, which picks the right one",
				provider, p.Name())
		}
		return nil
	}
}

func firewallProviderIs(provider string) func(*exec.Context) error {
	return func(c *exec.Context) error {
		p, err := pickFirewallProvider(c)
		if err != nil {
			return err
		}
		if p.Name() != provider {
			return fmt.Errorf("this is the %s spelling of `firewall`, and this node's firewall is %s; call `firewall` instead, which picks the right one",
				provider, p.Name())
		}
		return nil
	}
}
