package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// Every alias points at a module that exists, and names a provider that
// exists.
//
// The table is written by hand, so a typo in either half is a module
// that resolves to nothing or a guard that can never pass. Neither shows
// up until somebody on that platform calls it.
func TestEveryAliasResolvesToSomethingReal(t *testing.T) {
	r := New()
	modules := map[string]bool{}
	for _, m := range r.Exec.Signatures().Modules() {
		modules[m] = true
	}

	providers := map[string]bool{"freebsd": true}
	for _, p := range pkgProviders {
		providers[p.Name()] = true
	}
	for _, p := range availableServiceProviders() {
		providers[p.Name()] = true
	}
	for _, p := range firewallProviders {
		providers[p.Name()] = true
	}

	aliases := r.Exec.Aliases()
	if len(aliases) == 0 {
		t.Fatal("no aliases are registered; this audit has stopped checking anything")
	}
	for name, a := range aliases {
		if !modules[a.Module] {
			t.Errorf("%s aliases %s, which is not a module this build ships", name, a.Module)
		}
		if !providers[a.Provider] {
			t.Errorf("%s names the provider %q, which no provider calls itself", name, a.Provider)
		}
		if a.Usable == nil {
			t.Errorf("%s has no guard, so it would answer on every node", name)
		}
	}
}

// An alias resolves to the virtual module's function where the provider
// matches, and refuses where it does not.
//
// The refusal is the interesting half. `aptpkg.install` on a node whose
// package manager is not apt is neither a typo nor an unbuilt module: it
// is the wrong module for the machine, and saying which one the machine
// would use is what an operator needs to fix the tree.
func TestAnAliasRefusesOnTheWrongProvider(t *testing.T) {
	r := New()
	c := newCtx(false)

	// Whichever package provider this node has, the alias for it must
	// work and every other one must not.
	mine, err := pickPkgProvider(c)
	if err != nil {
		t.Skipf("this node has no package manager: %v", err)
	}

	forMine := map[string]string{
		"aptpkg":       "aptpkg",
		"freebsdpkg":   "pkgng",
		"win_pkg":      "chocolatey",
		"mac_brew_pkg": "mac_brew_pkg",
	}
	matched := 0
	for alias, provider := range forMine {
		_, err := r.Exec.Call(c, alias+".list_pkgs", value.NewMap(0))
		if provider == mine.Name() {
			matched++
			// It resolved and ran the real thing. Whether listing
			// packages succeeded is this node's business, not the
			// alias's; what matters is that the guard let it through.
			if err != nil && strings.Contains(err.Error(), "spelling of") {
				t.Errorf("%s was refused on a node whose provider is %s: %v", alias, mine.Name(), err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s answered on a node whose provider is %s", alias, mine.Name())
			continue
		}
		if !strings.Contains(err.Error(), mine.Name()) {
			t.Errorf("%s's refusal does not name the provider this node has (%s): %v",
				alias, mine.Name(), err)
		}
	}
	if matched != 1 {
		t.Errorf("%d aliases matched this node's provider %s, want exactly 1", matched, mine.Name())
	}
}

// The name exists whether or not this node can run it.
//
// `Has` is what validation and the migration report ask, and a tree
// naming `aptpkg.install` is a tree written for the Debian nodes it will
// run on. Reporting it as unknown because the machine holding the file
// is not one of them would flag every correct tree.
func TestAnAliasedNameIsKnownEverywhere(t *testing.T) {
	r := New()
	for _, name := range []string{
		"aptpkg.install", "freebsdpkg.install", "win_pkg.install",
		"systemd_service.start", "freebsd_service.start", "freebsd_sysctl.get",
	} {
		if !r.Exec.Has(name) {
			t.Errorf("%s is not known, so a tree naming it reads as a typo", name)
		}
	}
	// But a function the target module does not have is still unknown,
	// rather than resolving to nothing and looking real.
	if r.Exec.Has("aptpkg.no_such_function") {
		t.Error("an alias made up a function its module does not have")
	}
}

// freebsd_sysctl is guarded on the platform rather than on a provider,
// because sysctl has no provider table.
func TestTheSysctlAliasIsGuardedOnThePlatform(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(newCtx(false), "freebsd_sysctl.get", value.MapOf("name", "kern.hostname"))
	if runtime.GOOS == "freebsd" {
		// It resolved; whether the sysctl exists is the node's business.
		if err != nil && strings.Contains(err.Error(), "spelling of") {
			t.Errorf("freebsd_sysctl was refused on FreeBSD: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("freebsd_sysctl answered on %s", runtime.GOOS)
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("the refusal does not name this platform: %v", err)
	}
}
