package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A hostname a node cannot carry is refused before anything is written.
//
// Halfway is the failure mode worth preventing: setting the running name
// and then failing to write the file, or the reverse, leaves a node that
// renames itself at the next boot — which is the condition this module
// exists to close rather than to create.
func TestAnInvalidHostnameIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		why  string
	}{
		{"", "empty"},
		{"web1_prod", "an underscore is not a hostname character"},
		{"-web1", "a label may not start with a hyphen"},
		{"web1-", "a label may not end with a hyphen"},
		{"web1..example", "an empty label"},
		{"web1.exam ple", "a space"},
	} {
		if err := validHostname(tc.name); err == nil {
			t.Errorf("%q was accepted, and should not have been: %s", tc.name, tc.why)
		}
	}
	for _, ok := range []string{"web1", "web1.example", "WEB1", "w", "web-1.example.com"} {
		if err := validHostname(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
}

// A node configured with a fully qualified name and running the short
// one is not a node to rewrite on every run.
//
// `hostname` returns whatever was set, and the two halves are set by
// different tools that disagree about whether the domain belongs. A
// state naming `web1` against a node running `web1.example` has got what
// it asked for.
func TestAShortNameAndAQualifiedOneAreTheSameNode(t *testing.T) {
	same := [][2]string{
		{"web1", "web1"},
		{"web1.example", "web1"},
		{"web1", "web1.example"},
		{"WEB1", "web1"},
	}
	for _, p := range same {
		if !sameHostname(p[0], p[1]) {
			t.Errorf("sameHostname(%q, %q) = false, want true", p[0], p[1])
		}
	}
	// But two different domains are two different names: matching those
	// would make the state unable to move a node between them.
	differ := [][2]string{
		{"web1.example", "web1.internal"},
		{"web1", "web2"},
		{"", "web1"},
	}
	for _, p := range differ {
		if sameHostname(p[0], p[1]) {
			t.Errorf("sameHostname(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

// hostnameFixture points the persistent name wherever this platform
// keeps it, and returns the function that writes one there.
//
// FreeBSD keeps it in rc.conf and is read through `sysrc`; everything
// else keeps it in /etc/hostname. The first version of these tests
// redirected the file and nothing else, so on FreeBSD — the platform
// this project is developed on — they exercised a branch the module
// does not take there, and reported a pass for it. That went unseen
// until FreeBSD had CI, because the file branch is what Linux takes and
// Windows skips the module entirely.
func hostnameFixture(t *testing.T) (c *exec.Context, where string, set func(name string)) {
	t.Helper()
	c = newCtx(false)

	if runtime.GOOS == "freebsd" {
		runner := &exec.RecordingRunner{
			// Unset is a non-zero exit, which is a node that has never
			// had one configured rather than an error.
			Responses: map[string]exec.Result{"sysrc -n hostname": {Code: 1}},
		}
		c.Runner = runner
		return c, "rc.conf", func(name string) {
			runner.Responses["sysrc -n hostname"] = exec.Result{Stdout: name + "\n"}
		}
	}

	path := filepath.Join(t.TempDir(), "hostname")
	old := EtcHostnamePath
	EtcHostnamePath = path
	t.Cleanup(func() { EtcHostnamePath = old })
	return c, path, func(name string) {
		if name == "" {
			_ = os.Remove(path)
			return
		}
		writeFile(t, path, "# set by the installer\n\n"+name+"\n")
	}
}

// The persistent name is read from wherever this platform keeps it, and
// a node that has never had one configured reads as empty rather than as
// an error.
func TestThePersistentHostnameIsRead(t *testing.T) {
	c, wantWhere, set := hostnameFixture(t)

	// Unset is empty and not an error: that is the difference the state
	// closes, so it has to be readable rather than fatal.
	name, where, err := persistentHostname(c)
	if err != nil {
		t.Fatalf("an unset persistent hostname was an error: %v", err)
	}
	if name != "" {
		t.Errorf("an unset persistent hostname read as %q", name)
	}
	if where != wantWhere {
		t.Errorf("it was reported as coming from %q, want %q", where, wantWhere)
	}

	// And once it is set it reads back. On a unix that also means a
	// comment and a blank line in the file are not the name.
	set("web1.example")
	name, _, err = persistentHostname(c)
	if err != nil {
		t.Fatal(err)
	}
	if name != "web1.example" {
		t.Errorf("the persistent hostname read as %q, want web1.example", name)
	}
}

// On Windows the state refuses by name rather than doing something
// plausible.
//
// A rename there does not take effect until a reboot, so a state that
// set one would report a change on every run until somebody rebooted.
// Refusing says that in one line; the alternative says it once a day
// forever.
func TestTheHostnameStateRefusesOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this is the refusal, and it only happens on windows")
	}
	r := New()
	_, err := r.States.Call(newCtx(true), "hostname.system", value.MapOf("name", "web1"))
	if err == nil {
		t.Fatal("hostname.system was accepted on windows")
	}
	if !strings.Contains(err.Error(), "windows") {
		t.Errorf("the refusal does not name the platform: %v", err)
	}
}

// The state reports the two halves separately, because which one was
// wrong is what an operator needs to know.
func TestTheHostnameStateReportsWhichHalfWasWrong(t *testing.T) {
	skipOffPlatform(t, unixOnly)
	c, where, set := hostnameFixture(t)

	running, err := runningHostname()
	if err != nil {
		t.Fatal(err)
	}

	// The persistent name says something else, and the running name is
	// already what the state asks for. Only the persistent half should
	// be in the changes — this is the hand-renamed node that would have
	// gone back at the next boot.
	set("something-else")

	c.Test = true
	r := New()
	res, err := r.States.Call(c, "hostname.system", value.MapOf("name", running))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("no change was reported when the file disagreed: %+v", res)
	}
	if _, ok := res.Changes.Get("running"); ok {
		t.Errorf("the running name was reported as changing, and it already matched: %+v", res.Changes)
	}
	if _, ok := res.Changes.Get(where); !ok {
		t.Errorf("the persistent name was not reported as changing: %+v", res.Changes)
	}

	// And with both already right, nothing changes and the state says
	// so rather than rewriting what is already correct.
	set(running)
	res, err = r.States.Call(c, "hostname.system", value.MapOf("name", running))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a converged node reported changes: %+v", res.Changes)
	}
	if !res.Succeeded() {
		t.Errorf("a converged node did not succeed: %+v", res)
	}
}
