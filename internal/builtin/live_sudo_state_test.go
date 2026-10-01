package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// sudo.present and sudo.absent, against the node's real sudoers policy.
//
// # What this is allowed to touch
//
// One drop-in, named for this test and this process, in the directory
// the node's sudoers file includes. The main sudoers file is never
// written. The rule it installs lets `nobody` run a path that does not
// exist, which `sudo -l -U nobody` can see and nothing can use. The
// directory's listing is compared before and after, and the drop-in is
// removed in cleanup whatever happened.
func TestLiveSudoStatesManageADropIn(t *testing.T) {
	c := liveSudoSetup(t)
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this add a drop-in to the node's sudoers policy")
	}
	if os.Geteuid() != 0 {
		t.Skip("a sudoers drop-in is root's to write")
	}
	dir, err := sudoDropInDir(c, "")
	if err != nil {
		t.Skipf("this node's sudoers includes no single drop-in directory: %v", err)
	}
	name := fmt.Sprintf("halite-live-%d", os.Getpid())
	path := filepath.Join(dir, name)
	before := liveDirListing(t, dir)
	t.Cleanup(func() {
		_ = os.Remove(path)
		if after := liveDirListing(t, dir); after != before {
			t.Errorf("%s was not left as found:\nbefore: %s\nafter:  %s", dir, before, after)
		}
		if ok, said, _ := sudoRunVisudo(c, []string{"visudo", "-c"}, ""); !ok {
			t.Errorf("the policy fails visudo -c after the test: %s", said)
		}
	})
	t.Logf("drop-in directory %s, read from the node's sudoers", dir)

	rule := "nobody ALL=(root) NOPASSWD: /nonexistent/halite-live-test\n"
	present := []any{"name", name, "contents", rule}

	wantPredicted(t, "test mode", liveApply(t, true, "sudo.present", present...))
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("test mode wrote the drop-in")
	}

	wantChanged(t, "sudo.present", liveApply(t, false, "sudo.present", present...))
	data, mode, existed, err := sudoReadDropIn(path)
	if err != nil || !existed || string(data) != rule || mode != "root:0:0440" {
		t.Fatalf("the drop-in is %q, %s, %v, %v; want the rule, root:0:0440", data, mode, existed, err)
	}
	// sudo's own word that the rule is in force.
	if out := liveSudoList(t, c, "nobody"); !strings.Contains(out, "/nonexistent/halite-live-test") {
		t.Fatalf("sudo -l -U nobody does not show the rule:\n%s", out)
	}
	wantConverged(t, "a second sudo.present", liveApply(t, false, "sudo.present", present...))
	wantConverged(t, "test mode once applied", liveApply(t, true, "sudo.present", present...))

	// A mode drifted by hand is put back.
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	wantChanged(t, "sudo.present over a drifted mode", liveApply(t, false, "sudo.present", present...))
	if _, mode, _, _ := sudoReadDropIn(path); mode != "root:0:0440" {
		t.Errorf("the mode was not put back: %s", mode)
	}

	// Text visudo refuses changes nothing, in either mode.
	broken := []any{"name", name, "contents", "nobody ALL=(root) NOPASSWD /nonexistent/halite-live-test\n"}
	for _, test := range []bool{true, false} {
		res := liveApply(t, test, "sudo.present", broken...)
		if !res.Failed() || !strings.Contains(res.Comment, "syntax error") {
			t.Errorf("text visudo refuses (test=%v) = %+v, want a failure carrying visudo's words", test, res)
		}
	}
	if data, _, _, _ := sudoReadDropIn(path); string(data) != rule {
		t.Errorf("refused text reached the drop-in: %q", data)
	}

	// A name sudo would skip is refused by name, before anything is
	// written. The comment is asserted and not just the failure: with the
	// name check broken on purpose, the drop-in was written, missing from
	// visudo's listing, and taken back out -- a failure too, and the
	// first version of this assertion passed on it.
	if res := liveApply(t, false, "sudo.present", "name", name+".conf", "contents", rule); !res.Failed() ||
		!strings.Contains(res.Comment, "sudo skips every such file") {
		t.Errorf("a dotted name = %+v, want a refusal by name", res)
	}
	if _, err := os.Lstat(path + ".conf"); err == nil {
		_ = os.Remove(path + ".conf")
		t.Error("a dotted name was written")
	}

	// A directory sudo does not include: written, found missing from
	// visudo's listing, and taken back out.
	elsewhere := t.TempDir()
	res := liveApply(t, false, "sudo.present", "name", name, "contents", rule, "dir", elsewhere)
	if !res.Failed() || !strings.Contains(res.Comment, "did not list it") {
		t.Errorf("a drop-in in a directory sudo does not read = %+v, want a failure saying so", res)
	}
	if _, err := os.Lstat(filepath.Join(elsewhere, name)); err == nil {
		t.Error("the drop-in sudo does not read was left behind")
	}

	absent := []any{"name", name}
	wantPredicted(t, "test mode, absent", liveApply(t, true, "sudo.absent", absent...))
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("test mode removed the drop-in")
	}
	wantChanged(t, "sudo.absent", liveApply(t, false, "sudo.absent", absent...))
	if _, err := os.Lstat(path); err == nil {
		t.Fatal("the drop-in is still there")
	}
	if out := liveSudoList(t, c, "nobody"); strings.Contains(out, "/nonexistent/halite-live-test") {
		t.Errorf("sudo -l -U nobody still shows the rule:\n%s", out)
	}
	wantConverged(t, "a second sudo.absent", liveApply(t, false, "sudo.absent", absent...))
}

func liveSudoList(t *testing.T, c *exec.Context, account string) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"sudo", "-l", "-U", account}, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("sudo -l -U %s: %v", account, err)
	}
	return res.Stdout + res.Stderr
}

func liveDirListing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}
