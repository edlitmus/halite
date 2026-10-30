//go:build linux

package builtin

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// TestLiveWriteConfigFileKeepsSELinuxContext rewrites a file under /etc
// with a real SELinux context and checks the context is still there.
//
// It needs what no CI leg here has: a kernel with SELinux enabled, and root.
// The fleet's rpm legs are containers, whose kernel is the runner's. It was
// run on the lab's rocky9 row (Rocky Linux 9.8, kernel 5.14.0-687.54.1,
// enforcing, targeted policy), where it is the check that issue 321's
// section 5 SELinux bullet is closed, and it skips everywhere else with
// the reason. The unit test beside it stands in a user attribute and shows
// the copy; this shows that it is the right attribute and the right value.
func TestLiveWriteConfigFileKeepsSELinuxContext(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this rewrite a file under /etc")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root to set a SELinux context")
	}
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err != nil {
		t.Skipf("this kernel has no SELinux (no /sys/fs/selinux): %v", err)
	}
	if _, err := exec.LookPath("chcon"); err != nil {
		t.Skip("no chcon on this machine")
	}

	const path = "/etc/halite-selinux-label-test.conf"
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	// net_conf_t is /etc/hosts's type, and differs from etc_t, which is what a
	// new file in /etc is given: the pair the defect turned one into the other.
	if out, err := exec.Command("chcon", "-t", "net_conf_t", path).CombinedOutput(); err != nil {
		t.Skipf("chcon refused: %v: %s", err, out)
	}
	before := selinuxContextOf(t, path)
	if !strings.Contains(before, ":net_conf_t:") {
		t.Fatalf("the test's own setup is wrong: context is %q", before)
	}

	if err := writeConfigFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := selinuxContextOf(t, path); after != before {
		t.Fatalf("the rewritten file's context is %q, was %q", after, before)
	}
}

func selinuxContextOf(t *testing.T, path string) string {
	t.Helper()
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, "security.selinux", buf)
	if err != nil {
		t.Fatalf("reading %s's context: %v", path, err)
	}
	return strings.TrimRight(string(buf[:n]), "\x00")
}
