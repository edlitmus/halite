package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

// The persistent name is read out of the file, and a node that has never
// had one configured reads as empty rather than as an error.
func TestThePersistentHostnameIsReadFromTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hostname")
	old := EtcHostnamePath
	EtcHostnamePath = path
	t.Cleanup(func() { EtcHostnamePath = old })

	c := newCtx(false)

	// Absent is empty and not an error: that is the difference the
	// state closes, so it has to be readable rather than fatal.
	name, where, err := persistentHostname(c)
	if err != nil {
		t.Fatalf("an absent file was an error: %v", err)
	}
	if name != "" {
		t.Errorf("an absent file read as %q", name)
	}
	if where != path {
		t.Errorf("the file was reported as %q", where)
	}

	// A comment and a blank line are not the name.
	if err := os.WriteFile(path, []byte("# set by the installer\n\nweb1.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if runtime.GOOS == "windows" {
		t.Skip("hostname.system is unix only; see TestTheHostnameStateRefusesOnWindows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "hostname")
	old := EtcHostnamePath
	EtcHostnamePath = path
	t.Cleanup(func() { EtcHostnamePath = old })

	running, err := runningHostname()
	if err != nil {
		t.Fatal(err)
	}

	// The file says something else, and the running name is already
	// what the state asks for. Only the file should be in the changes,
	// and the comment should say so — this is the hand-renamed node
	// that would have gone back at the next boot.
	if err := os.WriteFile(path, []byte("something-else\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New()
	res := run(t, r, "hostname.system", value.MapOf("name", running), true)
	if !res.HasChanges() {
		t.Fatalf("no change was reported when the file disagreed: %+v", res)
	}
	if _, ok := res.Changes.Get("running"); ok {
		t.Errorf("the running name was reported as changing, and it already matched: %+v", res.Changes)
	}
	if _, ok := res.Changes.Get(path); !ok {
		t.Errorf("the file was not reported as changing: %+v", res.Changes)
	}

	// And with both already right, nothing changes and the state says
	// so rather than rewriting a file that is correct.
	if err := os.WriteFile(path, []byte(running+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = run(t, r, "hostname.system", value.MapOf("name", running), true)
	if res.HasChanges() {
		t.Errorf("a converged node reported changes: %+v", res.Changes)
	}
	if !res.Succeeded() {
		t.Errorf("a converged node did not succeed: %+v", res)
	}
}
