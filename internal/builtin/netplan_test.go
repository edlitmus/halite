package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// netplanFixture points the module at a temporary directory and gives
// back a context whose `netplan` is scripted.
func netplanFixture(t *testing.T, generateFails bool) (dir string, c *exec.Context) {
	t.Helper()
	dir = t.TempDir()
	old := NetplanDir
	NetplanDir = dir
	t.Cleanup(func() { NetplanDir = old })

	c = newCtx(false)
	res := map[string]exec.Result{
		"netplan generate": {},
		"netplan apply":    {},
	}
	if generateFails {
		res["netplan generate"] = exec.Result{
			Code:   1,
			Stderr: "Error in network definition: unknown key 'addressess'\n",
		}
	}
	c.Runner = &exec.RecordingRunner{Responses: res}
	// The tool check reads the real filesystem, so it has to be told
	// netplan exists on a machine that has never had it.
	c.Lookup = func(name string) string { return "/usr/sbin/" + name }
	return dir, c
}

// A netplan document is rooted at `network:`, and one that is not
// renders to nothing while netplan says nothing.
//
// That is the failure worth refusing by name: a tree that omitted the
// root would write a file, netplan would read an empty configuration
// from it, and the state would report success over a node whose network
// is not what the tree says.
func TestADocumentWithNoNetworkRootIsRefused(t *testing.T) {
	skipOffPlatform(t, debianOnly)
	dir, c := netplanFixture(t, false)
	res, err := New().States.Call(c, "netplan.managed", value.MapOf(
		"name", "01-halite",
		"config", value.MapOf("ethernets", value.MapOf("eth0", value.MapOf("dhcp4", true))),
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a document with no `network:` root was accepted")
	}
	if !strings.Contains(res.Comment, "network:") {
		t.Errorf("the refusal does not name what is missing: %s", res.Comment)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused document was written anyway: %v", entries)
	}
}

// The file is written 0600, because netplan refuses to read one that
// others can read and a configuration can carry a wireless passphrase.
func TestTheFileIsWrittenPrivate(t *testing.T) {
	skipOffPlatform(t, debianOnly)
	dir, c := netplanFixture(t, false)
	res, err := New().States.Call(c, "netplan.managed", netplanArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	info, err := os.Stat(filepath.Join(dir, "01-halite.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		// Windows has no POSIX mode, so this only means anything where
		// one exists.
		if !strings.Contains(strings.ToLower(os.Getenv("OS")), "windows") && perm != 0o600 {
			t.Errorf("mode = %v, want -rw-------", perm)
		}
	}
}

// Writing is idempotent: a second run over the same declaration changes
// nothing.
func TestASecondRunWritesNothing(t *testing.T) {
	skipOffPlatform(t, debianOnly)
	_, c := netplanFixture(t, false)
	r := New()

	res, err := r.States.Call(c, "netplan.managed", netplanArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("the first run reported no change: %+v", res)
	}
	res, err = r.States.Call(c, "netplan.managed", netplanArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %v", res.Changes)
	}
}

// Nothing is applied unless the declaration asks.
//
// This is the module's whole safety position: a network configuration
// that strands a node is recoverable through a console the node may not
// have, so converging by applying it is not something to do because a
// state ran.
func TestNothingIsAppliedUnlessAsked(t *testing.T) {
	skipOffPlatform(t, debianOnly)
	_, c := netplanFixture(t, false)
	res, err := New().States.Call(c, "netplan.managed", netplanArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.Contains(ran, "apply") {
			t.Errorf("the configuration was applied without being asked: %q", ran)
		}
	}
	// And it says so, rather than leaving an operator to assume it is
	// live.
	if !strings.Contains(res.Comment, "not live") {
		t.Errorf("the result does not say the configuration is not live: %s", res.Comment)
	}

	// With apply: true it is applied.
	_, c2 := netplanFixture(t, false)
	if _, err := New().States.Call(c2, "netplan.managed", netplanArgs(true)); err != nil {
		t.Fatal(err)
	}
	applied := false
	for _, ran := range c2.Runner.(*exec.RecordingRunner).RanCommands() {
		if ran == "netplan apply" {
			applied = true
		}
	}
	if !applied {
		t.Error("`apply: true` did not apply the configuration")
	}
}

// A configuration netplan will not render fails the state, and the
// result says the file is on disk and nothing was applied.
func TestAConfigurationThatWillNotRenderFailsAndSaysWhereItGotTo(t *testing.T) {
	skipOffPlatform(t, debianOnly)
	_, c := netplanFixture(t, true)
	res, err := New().States.Call(c, "netplan.managed", netplanArgs(true))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a configuration netplan refused was reported as a success")
	}
	if !strings.Contains(res.Comment, "addressess") {
		t.Errorf("the failure does not carry netplan's own message: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "nothing has been applied") {
		t.Errorf("the failure does not say what state the node is in: %s", res.Comment)
	}
	// And it did not apply after failing to render.
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.Contains(ran, "apply") {
			t.Errorf("a configuration that would not render was applied: %q", ran)
		}
	}
}

// A name is accepted in the spellings a tree is written in, and one
// outside netplan's directory is refused rather than written where
// nothing reads it.
func TestANameIsResolvedIntoNetplansDirectory(t *testing.T) {
	dir := t.TempDir()
	old := NetplanDir
	NetplanDir = dir
	t.Cleanup(func() { NetplanDir = old })

	for _, name := range []string{"01-halite", "01-halite.yaml", "01-halite.yml"} {
		got, err := netplanPath(name)
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if filepath.Dir(got) != dir {
			t.Errorf("%q resolved outside the directory: %s", name, got)
		}
		if !strings.HasSuffix(got, ".yaml") && !strings.HasSuffix(got, ".yml") {
			t.Errorf("%q got no suffix: %s", name, got)
		}
	}

	if _, err := netplanPath("/etc/somewhere/else.yaml"); err == nil {
		t.Error("a path outside netplan's directory was accepted")
	}
	if _, err := netplanPath(""); err == nil {
		t.Error("an empty name was accepted")
	}
}

func netplanArgs(apply bool) *value.Map {
	return value.MapOf(
		"name", "01-halite",
		"config", value.MapOf("network", value.MapOf(
			"version", int64(2),
			"ethernets", value.MapOf("eth0", value.MapOf("dhcp4", true)),
		)),
		"apply", apply,
	)
}
