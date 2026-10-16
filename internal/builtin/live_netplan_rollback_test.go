package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **A document real netplan refuses is not left for the next run, or the
// next boot, to find.**
//
// TestLiveNetplanRefusesADocumentNetplanWillNotRender watched real
// netplan refuse a document and the state fail, and stopped there. What
// it did not look at was the file it left in /etc/netplan, and the run
// after: that run found the refused file matching the declaration and
// reported it converged without asking netplan again. The fix puts the
// directory back as it was; this watches it do that against the real
// tool, and watches the second run fail too.
//
// The same probe file and interface as live_netplan_test.go, and the same
// cleanup, so nothing here touches a file the machine came with and
// nothing is applied.
func TestLiveNetplanARefusedDocumentIsNotLeftForTheNextRun(t *testing.T) {
	c := netplanLive(t)
	deferNetplanCleanup(t, c)
	r := New()
	path := filepath.Join(NetplanDir, liveNetplanName+".yaml")

	bad := value.MapOf("name", liveNetplanName, "config", value.MapOf("network", value.MapOf(
		"version", int64(2),
		"renderer", "networkd",
		"ethernets", value.MapOf(liveNetplanIface, value.MapOf(
			"dhcp4", false,
			"addressess", []any{"10.0.0.1/24"},
		)),
	)))

	for i := 1; i <= 2; i++ {
		res, err := r.States.Call(c, "netplan.managed", bad)
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Fatalf("run %d: a document real netplan refuses was reported as a success: %s", i, res.Comment)
		}
		if !strings.Contains(res.Comment, "addressess") {
			t.Errorf("run %d: the failure does not carry netplan's own message: %s", i, res.Comment)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("run %d: the refused document is still in %s (%v)", i, NetplanDir, err)
		}
		if !strings.Contains(res.Comment, "rendered again") {
			t.Errorf("run %d: the machine's own tree did not render after the probe was removed: %s", i, res.Comment)
		}
	}
}
