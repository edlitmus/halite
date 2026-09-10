package builtin

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// `iptables` and `nftables`, driven for real inside a private network
// namespace.
//
// # Why a namespace, and why it needs no root
//
// A packet filter is the one subsystem where a wrong rule in a test
// takes the machine off the network -- the runner, or the developer's
// own box. So nothing here touches the host ruleset: each test runs
// inside a fresh network namespace with an empty filter table, created
// by re-executing the test binary under
// `unshare --net --map-root-user`. That call needs no privilege on a
// kernel with unprivileged user namespaces (Debian and Ubuntu since
// ~2023), and inside it the process is uid 0 over a network stack that
// is thrown away when the test returns.
//
// If the kernel refuses the namespace -- `kernel.unprivileged_userns_clone=0`,
// or Ubuntu 24.04's AppArmor restriction without a profile -- the test
// skips rather than falling back to the host's real firewall.
//
// # What it establishes
//
// The mutating paths of both modules run against the real `iptables`
// and `nft`: rules added, checked for idempotence with the tool's own
// `-C` (iptables) and comment tags (nftables), policies set, chains and
// tables created and removed, and the dangerous-flush guards shown to
// refuse. Every result is checked against a fresh read of the tool, not
// the module's own answer.

// netnsReexec re-runs the calling test inside a network namespace and
// reports the child's result as this test's. It returns only when the
// process is already inside the namespace.
func netnsReexec(t *testing.T) {
	t.Helper()
	if os.Getenv("HALITE_NETNS_INNER") == "1" {
		return // already inside; run the body
	}
	if runtime.GOOS != "linux" {
		t.Skipf("network namespaces are Linux; this is %s", runtime.GOOS)
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("this host has no `unshare`")
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot find the test binary: %v", err)
	}
	cmd := exec.Command(unshare, "--net", "--map-root-user", "--",
		self, "-test.run", "^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "HALITE_NETNS_INNER=1")
	out, err := cmd.CombinedOutput()
	text := string(out)

	// unshare itself failing to set up the namespace is an environment
	// limitation, not a test failure.
	if err != nil && (strings.Contains(text, "unshare: ") ||
		strings.Contains(text, "Operation not permitted") ||
		strings.Contains(text, "clone failed")) &&
		!strings.Contains(text, "--- FAIL") {
		t.Skipf("this kernel will not give an unprivileged network namespace:\n%s", strings.TrimSpace(text))
	}
	if err != nil {
		t.Fatalf("the namespaced run failed:\n%s", text)
	}
	// Surface the child's log, then stop -- the assertions already ran
	// inside.
	t.Logf("ran inside a network namespace:\n%s", strings.TrimSpace(text))
	t.SkipNow()
}

// netnsRun runs a command inside the namespace and fails the test on a
// non-zero exit, which the setup steps need and the module's own Run
// does not do.
func netnsRun(t *testing.T, argv ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return string(out)
}
