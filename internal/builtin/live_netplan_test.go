package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `netplan`, driven against the real netplan on a real machine.
//
// # What was assumed until now
//
// `netplan generate` and `netplan apply` had never been run, and the
// YAML this module writes had never been round-tripped through a real
// netplan (evidence.go, DIVERGENCE 5.33). `netplan.managed` renders a
// document with this package's own YAML encoder and then trusts that
// netplan reads it back the same way — the exact shape of guess that
// 5.31 cost a firewall.
//
// # Why it is not in the fleet container, and barely in CI
//
// netplan's subsystem is the machine's network. `netplan generate` only
// writes backend configuration under /run and is safe to run anywhere —
// it runs at every boot — but `netplan apply` reconfigures the interface
// the job is connected over, and on a cloud instance the console that
// would recover it may not exist. DIVERGENCE 5.35's no-network rule
// keeps it out of the fleet image, and a GitHub runner is reachable only
// over the interface an apply would touch.
//
// So this never applies. It writes a file, has netplan validate it,
// reads the values back through netplan's own `get`, and removes it.
// That is the whole of what a fixture could not establish: whether the
// YAML is netplan's YAML. Whether `netplan apply` keeps a node reachable
// is a fact about the network and not about this module, and the module
// is built around refusing to be the thing that finds out.
//
// # It brings its own file
//
// Nothing here touches a file the machine came with. The probe is
// numbered to sort last, describes an interface named after nothing on
// any machine, sets `renderer: networkd` so it cannot perturb a
// NetworkManager host, and is `optional: true` so that even the apply
// that never happens could not stall a boot. It is removed and the tree
// regenerated in a cleanup that runs whatever the test did.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1`, the same gate as `hostname`, `sysctl` and
// `apparmor`. Writing into /etc/netplan is not a thing to do because
// somebody typed `go test ./...`.

// liveNetplanName sorts after anything a machine ships; liveNetplanIface
// is deliberately not a real interface name, so that an apply — which
// never happens here — would still confine nothing and rename nothing.
const (
	liveNetplanName  = "98-halite-live-probe"
	liveNetplanIface = "hal0probe"
)

// netplanLive gates on the machine being offered up and having netplan,
// or fails saying which is missing.
//
// It fails rather than skips when HALITE_SYSTEM_LIVE says the machine is
// available but netplan is not installed, for the reason the other live
// helpers do: a test that quietly passed by finding no tool would be the
// defect this whole file exists to prevent.
func netplanLive(t *testing.T) *exec.Context {
	t.Helper()
	c := system(t)
	if runtime.GOOS != "linux" {
		t.Skipf("netplan is Linux's, and this is %s", runtime.GOOS)
	}
	if c.Which("netplan") == "" {
		t.Fatal("this machine has no netplan; HALITE_SYSTEM_LIVE says it is available")
	}
	return c
}

// deferNetplanCleanup removes the probe file and regenerates the tree
// after the test, whatever the test managed to do to it.
func deferNetplanCleanup(t *testing.T, c *exec.Context) {
	t.Helper()
	path := filepath.Join(NetplanDir, liveNetplanName+".yaml")
	t.Cleanup(func() {
		_ = os.Remove(path)
		// Drop the backend files netplan rendered from the probe. This is
		// `netplan generate`, not `apply`: it rewrites /run and touches no
		// interface.
		res, err := c.Run(exec.Command{
			Argv:           []string{"netplan", "generate"},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			t.Errorf("regenerating the tree after removing the probe failed: %v\n%s",
				err, res.Stderr+res.Stdout)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("the probe file is still on disk: %s", path)
		}
	})
}

// liveNetplanConfig is the smallest document that is well formed and
// perturbs nothing: an interface that matches no hardware, off DHCP, not
// waited on at boot, rendered by networkd so a NetworkManager host never
// sees it.
func liveNetplanConfig() *value.Map {
	return value.MapOf("network", value.MapOf(
		"version", int64(2),
		"renderer", "networkd",
		"ethernets", value.MapOf(liveNetplanIface, value.MapOf(
			"dhcp4", false,
			"optional", true,
		)),
	))
}

// **The configuration directory really parses**, and the order agrees
// with the machine.
//
// `netplanFiles` lists /etc/netplan in the order netplan reads it, which
// is lexical — the reason a tree numbers its files. A build that sorted
// differently, or dropped a suffix it did not recognise, would apply
// files in an order the operator did not write.
func TestLiveNetplanReadsTheRealConfigDirectory(t *testing.T) {
	c := netplanLive(t)
	r := New()

	out, err := r.Exec.Call(c, "netplan.list", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	listed, ok := out.([]any)
	if !ok {
		t.Fatalf("netplan.list returned %T", out)
	}

	// The same directory, read here without going through the module.
	var want []string
	entries, err := os.ReadDir(NetplanDir)
	if err != nil {
		t.Fatalf("reading %s: %v", NetplanDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".yaml") || strings.HasSuffix(e.Name(), ".yml") {
			want = append(want, filepath.Join(NetplanDir, e.Name()))
		}
	}
	sort.Strings(want)

	got := make([]string, len(listed))
	for i, v := range listed {
		got[i], _ = v.(string)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("netplan.list =\n%v\nand the directory holds\n%v", got, want)
	}

	if len(want) == 0 {
		t.Skip("this machine has no netplan configuration to parse")
	}

	// One real file, parsed. Every netplan document is rooted at
	// `network:`; one that came back without it would mean the parser
	// read something other than what it was pointed at.
	first, err := r.Exec.Call(c, "netplan.get", value.MapOf("name", filepath.Base(want[0])))
	if err != nil {
		t.Fatalf("netplan.get(%s): %v", want[0], err)
	}
	doc, ok := first.(*value.Map)
	if !ok {
		t.Fatalf("netplan.get returned %T", first)
	}
	if _, ok := doc.Get("network"); !ok {
		t.Errorf("%s parsed without a `network:` root: keys %v", want[0], doc.Keys())
	}

	// And netplan's own reader is happy with the same tree.
	res, err := c.Run(exec.Command{Argv: []string{"netplan", "get", "all"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 {
		t.Errorf("`netplan get all` failed on this machine's own configuration:\n%s", res.Stderr+res.Stdout)
	}
}

// **A document written, validated by real netplan, and read back through
// netplan's own `get`.**
//
// This is the half a fixture cannot reach and the reason the module was
// `assumed`. `netplan.managed` encodes the document with this package's
// YAML encoder; whether that is the YAML netplan reads is exactly the
// question, and `netplan get` answering with the value that went in is
// the answer.
//
// Nothing is applied, and this test asserts it: a module that converged
// by reconfiguring the interface would be the one thing this module is
// built never to be.
func TestLiveNetplanWritesADocumentRealNetplanAccepts(t *testing.T) {
	c := netplanLive(t)
	deferNetplanCleanup(t, c)
	r := New()

	args := value.MapOf("name", liveNetplanName, "config", liveNetplanConfig())

	res, err := r.States.Call(c, "netplan.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("netplan.managed failed: %s", res.Comment)
	}
	if !res.HasChanges() {
		t.Errorf("writing a file that was not there reported no change: %+v", res)
	}
	if !strings.Contains(res.Comment, "not live") {
		t.Errorf("the result does not say the configuration is not live: %s", res.Comment)
	}

	// On disk, and private: netplan refuses to read a file others can
	// read, because it can carry a wireless passphrase.
	path := filepath.Join(NetplanDir, liveNetplanName+".yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the state reported success and wrote no file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want -rw-------", perm)
	}

	// Real netplan validates it. The module already ran this; asserted
	// again here so a provider that swallowed the error cannot pass.
	gen, err := c.Run(exec.Command{Argv: []string{"netplan", "generate"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Code != 0 {
		t.Fatalf("`netplan generate` rejected the document this module wrote:\n%s", gen.Stderr+gen.Stdout)
	}

	// **The round trip.** netplan's own `get` returns what went in, which
	// is the assertion that the encoder wrote netplan's YAML and not
	// merely valid YAML.
	optional, err := c.Run(exec.Command{
		Argv: []string{"netplan", "get", "ethernets." + liveNetplanIface + ".optional"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out := strings.TrimSpace(optional.Stdout); out != "true" {
		t.Errorf("`netplan get ethernets.%s.optional` = %q, want \"true\"; the document did not round-trip",
			liveNetplanIface, out)
	}
	ethernets, err := c.Run(exec.Command{Argv: []string{"netplan", "get", "ethernets"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ethernets.Stdout, liveNetplanIface) {
		t.Errorf("`netplan get ethernets` does not name the interface this module wrote:\n%s", ethernets.Stdout)
	}

	// And it converges: a second run over the same declaration changes
	// nothing.
	again, err := r.States.Call(c, "netplan.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if again.HasChanges() {
		t.Errorf("a second run reported a change: %+v", again.Changes)
	}
	if !strings.Contains(again.Comment, "already") {
		t.Errorf("the converged comment reads %q", again.Comment)
	}
}

// **A document real netplan rejects fails the state**, with netplan's
// own message and the node's state named.
//
// The module writes the file, has netplan validate it, and on rejection
// reports the failure with the file left on disk and nothing applied. A
// fixture supplied netplan's error text; this runs the parser that
// actually produces it.
func TestLiveNetplanRefusesADocumentNetplanWillNotRender(t *testing.T) {
	c := netplanLive(t)
	deferNetplanCleanup(t, c)
	r := New()

	bad := value.MapOf("network", value.MapOf(
		"version", int64(2),
		"renderer", "networkd",
		"ethernets", value.MapOf(liveNetplanIface, value.MapOf(
			"dhcp4", false,
			// netplan errors on an unknown key rather than ignoring it.
			"addressess", []any{"10.0.0.1/24"},
		)),
	))

	res, err := r.States.Call(c, "netplan.managed", value.MapOf("name", liveNetplanName, "config", bad))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatalf("a document netplan refuses was reported as a success: %+v", res)
	}
	if !strings.Contains(res.Comment, "addressess") {
		t.Errorf("the failure does not carry netplan's own message: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "nothing has been applied") {
		t.Errorf("the failure does not say what state the node is in: %s", res.Comment)
	}

	// The interface the probe names was never brought up: no apply, no
	// interface.
	if link, _ := c.Run(exec.Command{
		Argv:           []string{"ip", "link", "show", liveNetplanIface},
		IgnoreExitCode: true,
	}); link.Code == 0 {
		t.Errorf("`ip link show %s` succeeded; a refused document was applied", liveNetplanIface)
	}
}

// `netplan.generate` against the machine's own unmodified tree.
//
// The cheap validity check: the module's validate path runs the real
// `netplan generate`, and this machine's actual configuration is well
// formed. A failure here is the machine's, and worth seeing before the
// tests that write.
func TestLiveNetplanGenerateAcceptsTheRealTree(t *testing.T) {
	c := netplanLive(t)
	r := New()

	out, err := r.Exec.Call(c, "netplan.generate", value.NewMap(0))
	if err != nil {
		t.Fatalf("netplan.generate on this machine's own configuration: %v", err)
	}
	if out != true {
		t.Errorf("netplan.generate returned %v", out)
	}
}
