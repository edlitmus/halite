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

	// Read out of the registry rather than written down again. The copy
	// that used to live here listed four aliases and went stale the
	// moment `dnfpkg` and `yumpkg` were added -- and before that it was
	// the reason this test could not be satisfied on a RHEL node at all,
	// since it demanded exactly one match from a table that had no entry
	// for the provider such a node has.
	// The package aliases only: the registry also carries the `service`,
	// `firewall` and `sysctl` ones, and calling `.list_pkgs` on
	// `systemd_service` asks a question that module was never meant to
	// answer.
	forMine := map[string]string{}
	for name, a := range r.Exec.Aliases() {
		if a.Module == "pkg" {
			forMine[name] = a.Provider
		}
	}
	if len(forMine) == 0 {
		t.Fatal("no aliases are registered; this test has stopped testing anything")
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
	// One alias per provider, where SPEC 15.3 names one at all.
	//
	// It does not name one for every provider this build has: Alpine's
	// `apkpkg` has no row in that table, so an Alpine node legitimately
	// matches nothing and inventing a name to make this number 1 would
	// be the test deciding the specification. So the expectation is
	// derived from the registry too -- at most one, and exactly one when
	// this node's provider is aliased.
	want := 0
	for _, provider := range forMine {
		if provider == mine.Name() {
			want = 1
			break
		}
	}
	if matched != want {
		t.Errorf("%d aliases matched this node's provider %s, want %d",
			matched, mine.Name(), want)
	}
}

// Every SPEC 15.3 package-module name whose provider this build has is
// actually aliased.
//
// `dnfpkg` and `yumpkg` were missing for as long as the comment in
// aliases.go claimed the dnf provider did not do packages -- which it
// does, and which `pkg` has been selecting on RHEL nodes throughout. A
// provider with no reachable 15.3 name is a gap this names directly,
// rather than leaving it to be noticed when a node fails a count.
func TestEverySpecNamedProviderThisBuildHasIsAliased(t *testing.T) {
	r := New()

	aliased := map[string]bool{}
	for _, a := range r.Exec.Aliases() {
		if a.Module == "pkg" {
			aliased[a.Provider] = true
		}
	}

	// The names SPEC 15.3's platform table gives, against the provider
	// each one would reach. `zypperpkg` is deliberately absent: there is
	// no SUSE provider to alias yet.
	for _, c := range []struct{ specName, provider string }{
		{"aptpkg", "aptpkg"},
		{"freebsdpkg", "pkgng"},
		{"win_pkg", "chocolatey"},
		{"mac_brew_pkg", "mac_brew_pkg"},
		{"dnfpkg", "dnfpkg"},
		{"yumpkg", "yumpkg"},
	} {
		if !aliased[c.provider] {
			t.Errorf("SPEC 15.3 names %s and this build has the %s provider, but no alias "+
				"reaches it", c.specName, c.provider)
		}
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
