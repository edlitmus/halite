package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// fakePkgDB is a package database that changes when something is
// installed, which a RecordingRunner cannot be: it answers a command by
// its spelling, so the installed list read after an install would be the
// one read before it, and a pin could never be seen to be met or missed.
//
// It speaks pkg(8) and apk as far as pkg.installed uses them, and pkg's
// half is what was measured on beastie (pkg 2.8.4, FreeBSD 15.1): an
// operand is a bare name, which installs what the repository carries, or
// `name-version`, which installs that version if the repository carries
// it and otherwise exits 1 with pkg's own message. A repository carries
// one version of each package, as a FreeBSD one does.
//
// apk's half is only what this package already sends -- a bare name -- and
// no claim about how apk would take a pin, which nobody here has run.
type fakePkgDB struct {
	repo      map[string]string // name -> the one version the repository carries
	installed map[string]string
	ran       []string
}

func (f *fakePkgDB) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	f.ran = append(f.ran, cmd.String())
	argv := cmd.Argv
	switch {
	case len(argv) >= 2 && argv[0] == "pkg" && argv[1] == "query":
		return exec.Result{Stdout: f.list("%s\t%s\n")}, nil
	case len(argv) >= 2 && argv[0] == "pkg" && argv[1] == "install":
		return f.install(argv[2:], func(op string) (string, string, bool) {
			if v, ok := f.repo[op]; ok {
				return op, v, true
			}
			for name, v := range f.repo {
				if op == name+"-"+v {
					return name, v, true
				}
			}
			return "", "", false
		}, "pkg: No packages available to install matching '%s' have been found in the repositories")
	case len(argv) >= 2 && argv[0] == "apk" && argv[1] == "info":
		return exec.Result{Stdout: f.list("%s-%s\n")}, nil
	case len(argv) >= 2 && argv[0] == "apk" && argv[1] == "add":
		return f.install(argv[2:], func(op string) (string, string, bool) {
			v, ok := f.repo[op]
			return op, v, ok
		}, "ERROR: unable to select packages: %s (no such package)")
	}
	return exec.Result{Code: 127}, fmt.Errorf("fakePkgDB does not know %q", cmd.String())
}

func (f *fakePkgDB) list(format string) string {
	names := make([]string, 0, len(f.installed))
	for n := range f.installed {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, format, n, f.installed[n])
	}
	return b.String()
}

// install takes every operand or none, which is what both managers do
// with a line naming something they cannot find.
func (f *fakePkgDB) install(operands []string, resolve func(string) (string, string, bool), refusal string) (exec.Result, error) {
	got := map[string]string{}
	for _, op := range operands {
		if strings.HasPrefix(op, "-") {
			continue
		}
		name, v, ok := resolve(op)
		if !ok {
			msg := fmt.Sprintf(refusal, op)
			return exec.Result{Code: 1, Stderr: msg}, fmt.Errorf("exit 1: %s", msg)
		}
		got[name] = v
	}
	for n, v := range got {
		f.installed[n] = v
	}
	return exec.Result{}, nil
}

func pinCtx(db *fakePkgDB, binary string, grains *value.Map) *exec.Context {
	c := newCtx(false)
	c.Grains = grains
	c.Runner = db
	c.Lookup = func(name string) string {
		if name == binary {
			return "/usr/sbin/" + name
		}
		return ""
	}
	return c
}

func freebsdGrains() *value.Map { return value.MapOf("os", "FreeBSD", "os_family", "FreeBSD") }

// **pkgng puts the pin on the command line.** It used to drop it, so a
// pinned package was installed at whatever version the repository had
// (issue 321, M-33). The version is FreeBSD's whole spelling, port
// revision and epoch included; a wildcard goes as the bare name, since pkg
// only globs under `-g`, which would apply to the whole line.
func TestPkgngPutsThePinOnTheCommandLine(t *testing.T) {
	c, rec := pkgInjectionCtx(false, []string{"pkg"})
	pkgs := []any{value.MapOf("nginx", "1.30.5_4,3"), "tree", value.MapOf("curl", "8.*")}
	if _, err := New().Exec.Call(c, "pkg.install", value.MapOf("pkgs", pkgs)); err != nil {
		t.Fatal(err)
	}
	want := "pkg install -y -q nginx-1.30.5_4,3 tree curl"
	if got := rec.RanCommands(); len(got) != 1 || got[0] != want {
		t.Errorf("ran %q\nwant [%q]", got, want)
	}
}

// A pin the repository carries is installed, and the second run finds it
// and changes nothing: the state converges, which is what it never did.
func TestPkgInstalledConvergesOnAFreeBSDPin(t *testing.T) {
	db := &fakePkgDB{repo: map[string]string{"tree": "2.3.2"}, installed: map[string]string{}}
	r := New()
	args := value.MapOf("name", "tree", "version", "2.3.2")

	first, err := r.States.Call(pinCtx(db, "pkg", freebsdGrains()), "pkg.installed", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result == nil || !*first.Result || !first.HasChanges() {
		t.Fatalf("first run: %+v", first)
	}
	if db.installed["tree"] != "2.3.2" {
		t.Fatalf("installed %v", db.installed)
	}

	second, err := r.States.Call(pinCtx(db, "pkg", freebsdGrains()), "pkg.installed", args)
	if err != nil {
		t.Fatal(err)
	}
	if second.Result == nil || !*second.Result || second.HasChanges() {
		t.Errorf("second run did not converge: %+v", second)
	}
}

// A pin the repository does not carry fails, with pkg's own reason, and
// installs nothing. Before the fix the bare name went and the repository's
// version came back as success.
func TestPkgInstalledRefusesAFreeBSDPinTheRepositoryLacks(t *testing.T) {
	db := &fakePkgDB{repo: map[string]string{"tree": "2.3.2"}, installed: map[string]string{}}
	res, err := New().States.Call(pinCtx(db, "pkg", freebsdGrains()), "pkg.installed",
		value.MapOf("name", "tree", "version", "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Fatalf("a pin the repository lacks succeeded: %+v", res)
	}
	if !strings.Contains(res.Comment, "tree-1.0.0") {
		t.Errorf("the comment does not carry pkg's reason: %s", res.Comment)
	}
	if len(db.installed) != 0 {
		t.Errorf("installed %v", db.installed)
	}
}

// **What was installed is checked against the pin**, so a provider that
// does not honour one -- apk, still, and pkgng for a wildcard, which goes
// as the bare name -- fails instead of reporting a change it will report
// again on every run.
func TestPkgInstalledFailsWhenThePinWasNotInstalled(t *testing.T) {
	cases := []struct {
		name, binary, pin, want string
		grains                  *value.Map
	}{
		{"pkgng wildcard", "pkg", "2.2.*", "tree is pinned to 2.2.* and 2.3.2 is installed", freebsdGrains()},
		{"apk exact", "apk", "2.2.0-r0", "tree is pinned to 2.2.0-r0 and 2.3.2-r0 is installed",
			value.MapOf("os", "Alpine", "os_family", "Alpine")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := "2.3.2"
			if tc.binary == "apk" {
				repo = "2.3.2-r0"
			}
			db := &fakePkgDB{repo: map[string]string{"tree": repo}, installed: map[string]string{}}
			args := value.MapOf("name", "tree", "version", tc.pin)
			for run := 1; run <= 2; run++ {
				res, err := New().States.Call(pinCtx(db, tc.binary, tc.grains), "pkg.installed", args)
				if err != nil {
					t.Fatal(err)
				}
				if res.Result == nil || *res.Result {
					t.Fatalf("run %d: a pin that was not installed succeeded: %+v", run, res)
				}
				if !strings.Contains(res.Comment, tc.want) {
					t.Errorf("run %d: comment %q does not say %q", run, res.Comment, tc.want)
				}
			}
		})
	}
}

// The check is the same versionSatisfies the decision to install uses, so
// an install that met the pin, exactly or by a wildcard's prefix, is still
// a success with its change reported.
func TestPkgInstalledAcceptsWhatThePinAllows(t *testing.T) {
	for _, pin := range []string{"2.3.*", "2.3.2"} {
		db := &fakePkgDB{repo: map[string]string{"tree": "2.3.2"}, installed: map[string]string{}}
		res, err := New().States.Call(pinCtx(db, "pkg", freebsdGrains()), "pkg.installed",
			value.MapOf("name", "tree", "version", pin))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || !*res.Result || !res.HasChanges() {
			t.Errorf("pin %s: %+v", pin, res)
		}
	}
}
