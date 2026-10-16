package builtin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// A package name is refused before it can become a package manager's
// option, on every provider, through every pkg function that takes one.
//
// # What this reproduces
//
// `pkgs: ["-oDpkg::Pre-Invoke::=touch /tmp/pwned", curl]` made the apt
// provider run `apt-get install ... "-oDpkg::Pre-Invoke::=touch
// /tmp/pwned" curl` and report success: a dpkg hook, set from pillar,
// running as root. dnf's `--setopt=` is the same door. The runner here is
// a recording one, so nothing is executed; what is asserted is the argv
// halite would have handed the tool, which is where the injection lives.
//
// # Why the provider is chosen by Lookup rather than called directly
//
// Calling `aptProvider{}.Install` with a bad name would test the provider,
// and the provider is not where the check is: it is at the module
// boundary (packageSpecs and the single-name functions), so the test goes
// through the registry, as a state file does. Lookup decides which
// provider pickPkgProvider finds, the same way a node's PATH does.

// pkgInjectionProviders names, per provider, the programs whose presence
// makes pickPkgProvider choose it, and nothing else. The list order of
// pkgProviders means each set must exclude the earlier providers' tools.
var pkgInjectionProviders = []struct {
	name     string
	binaries []string
}{
	{"apt", []string{"dpkg-query", "apt-get"}},
	{"zypper", []string{"zypper", "rpm"}},
	{"dnf", []string{"dnf", "rpm"}},
	{"pkgng", []string{"pkg"}},
	{"apk", []string{"apk"}},
	{"brew", []string{"brew"}},
	{"choco", []string{"choco"}},
}

func pkgInjectionCtx(test bool, binaries []string) (*exec.Context, *exec.RecordingRunner) {
	have := map[string]bool{}
	for _, b := range binaries {
		have[b] = true
	}
	rec := &exec.RecordingRunner{}
	c := newCtx(test)
	c.Runner = rec
	c.Lookup = func(name string) string {
		if have[name] {
			return "/usr/bin/" + name
		}
		return ""
	}
	return c, rec
}

// The payloads. Each is a name or a version that must never reach argv.
const (
	aptHookPayload = "-oDpkg::Pre-Invoke::=touch /tmp/pwned"
	dnfOptPayload  = "--setopt=tsflags=noscripts"
)

type pkgInjectionCall struct {
	fn    string // registry name
	state bool   // a state function rather than an execution one
	args  *value.Map
}

// pkgInjectionCalls is every pkg function that hands a caller-supplied
// name or version to a provider, each with a payload in the argument it
// reads. TestPkgEveryNameTakingFunctionIsCovered holds this list to the
// registry, so a new function cannot be added without appearing here.
func pkgInjectionCalls() []pkgInjectionCall {
	list := []any{aptHookPayload, "curl"}
	return []pkgInjectionCall{
		{"pkg.installed", true, value.MapOf("name", "x", "pkgs", list)},
		{"pkg.installed", true, value.MapOf("name", dnfOptPayload)},
		{"pkg.installed", true, value.MapOf("name", "curl", "version", "-1 --setopt=x")},
		{"pkg.installed", true, value.MapOf("name", "x", "pkgs", []any{value.MapOf("curl", "--version")})},
		{"pkg.installed", true, value.MapOf("name", "curl\n-oFoo=bar")},
		{"pkg.installed", true, value.MapOf("name", "curl\x00")},
		{"pkg.latest", true, value.MapOf("name", "x", "pkgs", list)},
		{"pkg.removed", true, value.MapOf("name", "x", "pkgs", list)},
		{"pkg.purged", true, value.MapOf("name", "x", "pkgs", list)},
		{"pkg.install", false, value.MapOf("pkgs", list)},
		{"pkg.install", false, value.MapOf("name", "curl", "version", "--x")},
		{"pkg.remove", false, value.MapOf("pkgs", list)},
		{"pkg.purge", false, value.MapOf("pkgs", list)},
		{"pkg.hold", false, value.MapOf("name", aptHookPayload)},
		{"pkg.unhold", false, value.MapOf("name", aptHookPayload)},
		{"pkg.latest_version", false, value.MapOf("name", aptHookPayload)},
		{"pkg.available_version", false, value.MapOf("name", aptHookPayload)},
		{"pkg.file_list", false, value.MapOf("name", aptHookPayload)},
		{"pkg.file_dict", false, value.MapOf("pkgs", list)},
		{"pkg.info_installed", false, value.MapOf("pkgs", list)},
		{"pkg.download", false, value.MapOf("pkgs", list)},
	}
}

// **The payload is refused on every provider, and nothing at all runs.**
//
// "Nothing at all", not "nothing containing the payload": the check comes
// before the installed list is read, so a refused call is one the package
// manager never hears about, in a dry run or a real one.
func TestPkgRefusesOptionInjectionOnEveryProvider(t *testing.T) {
	r := New()
	for _, p := range pkgInjectionProviders {
		for _, call := range pkgInjectionCalls() {
			for _, test := range []bool{true, false} {
				c, rec := pkgInjectionCtx(test, p.binaries)
				var refused bool
				var said string
				if call.state {
					res, err := r.States.Call(c, call.fn, call.args)
					if err != nil {
						t.Fatalf("%s %s: %v", p.name, call.fn, err)
					}
					refused = res.Result != nil && !*res.Result
					said = res.Comment
				} else {
					_, err := r.Exec.Call(c, call.fn, call.args)
					refused = err != nil
					if err != nil {
						said = err.Error()
					}
				}
				where := fmt.Sprintf("%s %s %s", p.name, call.fn, describePkgArgs(call.args))
				if test {
					where += " (test)"
				}
				if !refused {
					t.Errorf("%s: was not refused", where)
				} else if !strings.Contains(said, "refused") {
					t.Errorf("%s: failed, but not because the name was refused: %s", where, said)
				}
				if len(rec.Ran) != 0 {
					t.Errorf("%s: ran %q; a refused name must reach no command", where, rec.RanCommands())
				}
			}
		}
	}
}

// Every pkg function that takes `name` or `pkgs` is either in the
// injection list above or reads the name without handing it to a tool.
// The second set is short and named, so adding to it is a decision
// somebody makes in review rather than an omission.
func TestPkgEveryNameTakingFunctionIsCovered(t *testing.T) {
	// Functions whose name never reaches an argv: `pkg.version` looks it up
	// in the installed list, and `pkg.upgrade_available` asks pkg.version
	// and pkg.latest_version, which is checked above.
	readsOnly := map[string]bool{"pkg.version": true, "pkg.upgrade_available": true}

	covered := map[string]bool{}
	for _, call := range pkgInjectionCalls() {
		covered[call.fn] = true
	}
	r := New()
	for _, reg := range []*signature.Registry{r.Exec.Signatures(), r.States.Signatures()} {
		for _, sig := range reg.Functions("pkg") {
			name := sig.Name()
			if sigTakesPkgName(sig.Params) && !covered[name] && !readsOnly[name] {
				t.Errorf("%s takes a package name and is not in pkgInjectionCalls; "+
					"validate it with pkgValidateName and add it there", name)
			}
		}
	}

	// And the reads-only ones are true to their description: the payload
	// reaches no argv through them on any provider.
	for _, p := range pkgInjectionProviders {
		for name := range readsOnly {
			c, rec := pkgInjectionCtx(false, p.binaries)
			_, _ = r.Exec.Call(c, name, value.MapOf("name", aptHookPayload))
			for _, cmd := range rec.Ran {
				for _, a := range cmd.Argv {
					if strings.Contains(a, "Pre-Invoke") {
						t.Errorf("%s %s: the payload reached %q", p.name, name, cmd.Argv)
					}
				}
			}
		}
	}
}

// describePkgArgs spells a call's arguments for a failure message, with
// each value quoted so a newline or NUL in a payload is visible.
func describePkgArgs(args *value.Map) string {
	var parts []string
	for _, e := range args.Entries() {
		parts = append(parts, fmt.Sprintf("%s=%s", value.KeyString(e.Key), describePkgValue(e.Val)))
	}
	return strings.Join(parts, " ")
}

func describePkgValue(v any) string {
	switch t := v.(type) {
	case []any:
		var parts []string
		for _, item := range t {
			parts = append(parts, describePkgValue(item))
		}
		return "[" + strings.Join(parts, " ") + "]"
	case *value.Map:
		return "{" + describePkgArgs(t) + "}"
	}
	return fmt.Sprintf("%q", value.KeyString(v))
}

func sigTakesPkgName(params []signature.Param) bool {
	for _, p := range params {
		if p.Name == "name" || p.Name == "pkgs" {
			return true
		}
	}
	return false
}

// **Legitimate names still reach the tool, spelled as they were.**
//
// The refusal is only worth having if it refuses nothing real. These are
// the shapes the managers actually use that a naive rule would trip on:
// `+` in Debian names, a dotted version in a name, apt's `:arch`, a
// FreeBSD origin with its `/`, a flavour with its `@`. Each provider's
// install argv is asserted whole, so a validator that rewrote or dropped a
// name would fail here rather than pass by omission.
func TestPkgLegitimateNamesReachTheArgv(t *testing.T) {
	names := []any{"libstdc++6", "python3.11", "nginx:amd64", "www/nginx", "py311-foo@py311"}
	operands := "libstdc++6 python3.11 nginx:amd64 www/nginx py311-foo@py311"
	want := map[string]string{
		"apt": "apt-get install -y -q -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef " +
			operands,
		"dnf":    "dnf install -y -q " + operands,
		"pkgng":  "pkg install -y -q " + operands,
		"apk":    "apk add --no-progress " + operands,
		"zypper": "zypper --non-interactive install --auto-agree-with-licenses --name " + operands,
	}
	r := New()
	for _, p := range pkgInjectionProviders {
		w, ok := want[p.name]
		if !ok {
			continue
		}
		c, rec := pkgInjectionCtx(false, p.binaries)
		if _, err := r.Exec.Call(c, "pkg.install", value.MapOf("pkgs", names)); err != nil {
			t.Errorf("%s: %v", p.name, err)
			continue
		}
		if got := rec.RanCommands(); len(got) != 1 || got[0] != w {
			t.Errorf("%s ran %q\nwant [%q]", p.name, got, w)
		}
	}
}

// A pinned version that is legitimate is glued to its name as before,
// including an epoch and a Debian `~`.
func TestPkgLegitimateVersionsReachTheArgv(t *testing.T) {
	pkgs := []any{value.MapOf("nginx", "1:1.24.0-2~deb12u1"), value.MapOf("tree", "2.1.1-2.el9")}
	want := map[string]string{
		"apt": "apt-get install -y -q -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef " +
			"nginx=1:1.24.0-2~deb12u1 tree=2.1.1-2.el9",
		"dnf": "dnf install -y -q nginx-1:1.24.0-2~deb12u1 tree-2.1.1-2.el9",
	}
	r := New()
	for _, p := range pkgInjectionProviders {
		w, ok := want[p.name]
		if !ok {
			continue
		}
		c, rec := pkgInjectionCtx(false, p.binaries)
		if _, err := r.Exec.Call(c, "pkg.install", value.MapOf("pkgs", pkgs)); err != nil {
			t.Errorf("%s: %v", p.name, err)
			continue
		}
		if got := rec.RanCommands(); len(got) != 1 || got[0] != w {
			t.Errorf("%s ran %q\nwant [%q]", p.name, got, w)
		}
	}
}

// The validator's own table, for the edges the registry tests do not
// spell: Unicode whitespace and format characters, and what is let
// through on purpose.
func TestPkgValidateName(t *testing.T) {
	refuse := []string{"", "-", "-y", "--setopt=x", "a b", "a\tb", "a\nb", "a\rb", "a\x00b",
		"a\x7fb", "a b", "a‮b", "a b"}
	for _, n := range refuse {
		if pkgValidateName(n) == nil {
			t.Errorf("%q was accepted", n)
		}
	}
	accept := []string{"curl", "libstdc++6", "python3.11", "nginx:amd64", "www/nginx",
		"py311-foo@py311", "openssl@3", "@development-tools", "kernel-devel.x86_64", "python3*",
		"Git.Install", "a-", "nginx/bookworm-backports"}
	for _, n := range accept {
		if err := pkgValidateName(n); err != nil {
			t.Errorf("%q was refused: %v", n, err)
		}
	}
	if pkgValidateVersion("x", "") != nil {
		t.Error("an empty version (no pin) was refused")
	}
	if pkgValidateVersion("x", "-1") == nil {
		t.Error("a version beginning with - was accepted")
	}
}

// FreeBSD's comparison hands both versions to pkg(8) as their own argv
// elements, so it is the one place a version alone is an option.
func TestCompareFreeBSDRefusesAnOptionShapedVersion(t *testing.T) {
	c, rec := pkgInjectionCtx(false, []string{"pkg"})
	if _, err := compareFreeBSD(c, "1.0", "-o"); err == nil {
		t.Error("a version beginning with - was handed to pkg version -t")
	}
	if len(rec.Ran) != 0 {
		t.Errorf("ran %q", rec.RanCommands())
	}
}
