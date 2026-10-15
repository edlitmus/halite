package builtin

import (
	osexec "os/exec"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// cpan.module_version is declared non-mutating and not arbitrary_code, so
// a read-only grant reaches it. It used to splice the module name into
// perl source (`-M<name>` and `-e 'print $<name>::VERSION'`), and
// `Carp;BEGIN{print "INJECTED\n"}` ran its BEGIN block -- reproduced with
// perl 5.44.0 before this was fixed. DIVERGENCE 5.290.
//
// A name that is not a Perl package name is refused before anything runs.
func TestCpanModuleVersionRefusesANameThatIsNotAPackage(t *testing.T) {
	r := New()
	for _, bad := range []string{
		`Carp;BEGIN{print "INJECTED\n"}`,
		`Carp;system("id")`,
		`..::..::tmp::evil`, // a require path out of @INC, through the second wall
		`-e`,
		`Foo'Bar`, // perl's old separator: accepted by perl, refused here
		`::Foo`, `Foo::`, `Foo:::Bar`, `1Foo`, `Foo Bar`, "Foo\nBar", `Foo/Bar`, ``,
	} {
		c := newCtx(false)
		_, err := r.Exec.Call(c, "cpan.module_version", value.MapOf("module", bad))
		if err == nil || !strings.Contains(err.Error(), "not a Perl module name") {
			t.Errorf("%q was not refused as a module name: err=%v", bad, err)
		}
		if ran := c.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 0 {
			t.Errorf("%q: commands ran before the name was refused: %v", bad, ran)
		}
	}
}

// A real module name reaches perl as an argument, never as source.
func TestCpanModuleVersionPassesTheNameAsData(t *testing.T) {
	r := New()
	for _, good := range []string{"Carp", "JSON::PP", "_Private::Mod2", "List::Util"} {
		c := newCtx(false)
		if _, err := r.Exec.Call(c, "cpan.module_version", value.MapOf("module", good)); err != nil {
			t.Errorf("%s was refused: %v", good, err)
			continue
		}
		ran := c.Runner.(*exec.RecordingRunner).Ran
		if len(ran) != 1 {
			t.Fatalf("%s: ran %d commands", good, len(ran))
		}
		argv := ran[0].Argv
		if argv[len(argv)-1] != good || argv[len(argv)-2] != "--" {
			t.Errorf("%s is not the last argument after --: %q", good, argv)
		}
		for _, a := range argv[:len(argv)-1] {
			if strings.Contains(a, good) {
				t.Errorf("%s appears in perl's source or options: %q", good, argv)
			}
		}
	}
}

// The same two questions asked of the perl on this machine: the injection
// does not execute even with the validation bypassed (the argument form on
// its own), and a real module reports the version the old -M form did, so
// the change of form has not changed the answer.
func TestCpanModuleVersionAgainstRealPerl(t *testing.T) {
	if _, err := osexec.LookPath("perl"); err != nil {
		t.Skip("no perl on this machine")
	}
	// Standard output only: perl's "Can't locate" on stderr quotes the
	// name back, BEGIN block and all, without having run it.
	run := func(argv []string) (string, int) {
		res, err := (&exec.OSRunner{}).Run(t.Context(), exec.Command{Argv: argv, IgnoreExitCode: true})
		if err != nil {
			t.Fatalf("%q: %v", argv, err)
		}
		return res.Stdout, res.Code
	}

	injection := `Carp;BEGIN{print "INJECTED\n"}`
	if out, _ := run(perlModuleVersionArgv(injection)); strings.Contains(out, "INJECTED") {
		t.Errorf("the argument form executed the name: %s", out)
	}

	for _, mod := range []string{"Carp", "List::Util"} {
		got, code := run(perlModuleVersionArgv(mod))
		want, _ := run([]string{"perl", "-M" + mod, "-e", "print $" + mod + "::VERSION"})
		if code != 0 || got == "" || got != want {
			t.Errorf("%s: the argument form said %q (exit %d), the -M form %q", mod, got, code, want)
		}
	}
	if _, code := run(perlModuleVersionArgv("No::Such::Module::Here")); code == 0 {
		t.Error("a module that is not installed exited 0, so it would read as installed with no version")
	}

	// And through the function, with the real runner: refused, nothing printed.
	c := newCtx(false)
	c.Runner = &exec.OSRunner{}
	out, err := New().Exec.Call(c, "cpan.module_version", value.MapOf("module", injection))
	if err == nil {
		t.Errorf("the injection was not refused; it returned %#v", out)
	}
}
