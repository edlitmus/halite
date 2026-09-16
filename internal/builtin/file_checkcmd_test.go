package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// runCheck calls a state with a per-state check_cmd, which reaches the
// state on the context the way the runner delivers it.
func runCheck(t *testing.T, r *Registries, name string, args *value.Map, checkCmd []string, test bool) states.Result {
	t.Helper()
	c := &hexec.Context{
		Ctx:      context.Background(),
		Grains:   value.MapOf("os", "Ubuntu", "os_family", "Debian"),
		Pillar:   value.NewMap(0),
		Config:   value.NewMap(0),
		NodeID:   "test.node",
		Env:      "base",
		Test:     test,
		CheckCmd: checkCmd,
		Runner:   &hexec.OSRunner{},
	}
	res, err := r.States.Call(c, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// The estate writes `check_cmd: /usr/sbin/visudo -c -f`, with no file
// argument, because Salt appends the path of the temporary file holding
// what it is about to install. Nothing appended it here, so the command
// ran as a bare `visudo -c -f` -- and visudo exits 1 when -f has no
// argument, so a state that was correct failed on every run.
//
// Checked against the real visudo where there is one.
func TestCheckCmdAppendsTheFileTheWayVisudoNeeds(t *testing.T) {
	visudo := findVisudo(t)
	r := New()
	path := filepath.Join(t.TempDir(), "eic-breakglass")

	res := runCheck(t, r, "file.managed", value.MapOf(
		"name", path,
		"contents", "eic-breakglass ALL=(ALL) NOPASSWD: ALL\n",
		"mode", "0440",
	), []string{visudo + " -c -f"}, false)

	if !res.Succeeded() {
		t.Fatalf("the estate's own state failed: %v %q", res.Result, res.Comment)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "NOPASSWD") {
		t.Errorf("the file was not written: %v %q", err, b)
	}
}

// And the other half: contents visudo rejects must never reach the
// destination. A check that runs after the write has already lost.
func TestCheckCmdRejectedContentsAreNeverWritten(t *testing.T) {
	visudo := findVisudo(t)
	r := New()
	path := filepath.Join(t.TempDir(), "broken")

	res := runCheck(t, r, "file.managed", value.MapOf(
		"name", path,
		"contents", "this is not sudoers syntax !!!\n",
	), []string{visudo + " -c -f"}, false)

	if res.Succeeded() {
		t.Fatalf("invalid sudoers was accepted: %+v", res)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the rejected contents were written anyway")
	}
	// What the checker said is the reason, and an operator should not
	// have to run it by hand to find out.
	if !strings.Contains(res.Comment, "syntax error") && !strings.Contains(res.Comment, "parse error") {
		t.Errorf("the comment does not carry the checker's output: %q", res.Comment)
	}
}

// An existing file keeps its contents when the new ones are rejected,
// which is the case that matters on a sudoers file: locking an estate
// out of sudo is worse than not updating it.
func TestCheckCmdLeavesTheExistingFileAlone(t *testing.T) {
	visudo := findVisudo(t)
	r := New()
	path := filepath.Join(t.TempDir(), "sudoers")
	const good = "ops ALL=(ALL) NOPASSWD: ALL\n"
	if err := os.WriteFile(path, []byte(good), 0o440); err != nil {
		t.Fatal(err)
	}

	res := runCheck(t, r, "file.managed", value.MapOf(
		"name", path, "contents", "garbage !!!\n",
	), []string{visudo + " -c -f"}, false)

	if res.Succeeded() {
		t.Fatalf("invalid contents were accepted: %+v", res)
	}
	if b, _ := os.ReadFile(path); string(b) != good {
		t.Errorf("the existing file was changed to %q", b)
	}
}

// A test run must not execute the operator's command. Salt's test
// branch returns before the check, and the command may do anything.
func TestCheckCmdDoesNotRunInTestMode(t *testing.T) {
	r := New()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	path := filepath.Join(dir, "f")

	res := runCheck(t, r, "file.managed", value.MapOf(
		"name", path, "contents", "x\n",
	), []string{"touch " + marker + " ; true"}, true)

	if res.Result != nil {
		t.Errorf("test mode should report a would-change, got %v", res.ResultString())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("test mode ran the check_cmd")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("test mode wrote the file")
	}
}

// The command is given a real file holding the pending contents, not
// the destination and not an empty temporary.
func TestCheckCmdSeesThePendingContents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell pipeline")
	}
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	out := filepath.Join(dir, "seen")

	// The checker copies what it was handed, so the test can read it.
	//
	// The word after the script is $0, and it must be a plain name
	// rather than `--`: FreeBSD's sh takes `--` as an end-of-options
	// marker and consumes it, so the appended path lands in $0 and $1
	// is empty. Linux's dash leaves `--` as $0 and this passed there.
	check := `sh -c 'cp "$1" ` + out + `' checker`
	res := runCheck(t, r, "file.managed", value.MapOf(
		"name", path, "contents", "the-pending-contents\n",
	), []string{check}, false)

	if !res.Succeeded() {
		t.Fatalf("result = %v %q", res.Result, res.Comment)
	}
	seen, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the checker was handed nothing readable: %v", err)
	}
	if string(seen) != "the-pending-contents\n" {
		t.Errorf("the checker saw %q, not the pending contents", seen)
	}
	// And the destination did not exist while the check ran, which is
	// the difference between validating and reporting.
	if string(seen) == "" {
		t.Error("the checker was handed an empty file")
	}
}

// tmp_ext is for a checker that insists on a suffix, and tmp_dir for
// one confined to a directory. Both are Salt's, and both are only
// visible in the path the command is handed.
func TestCheckCmdTmpDirAndTmpExt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell test")
	}
	r := New()
	dir := t.TempDir()
	tmpDir := filepath.Join(dir, "checkers")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "f")

	// The check passes only if the path it was handed is inside tmp_dir
	// and ends in .conf.
	check := `sh -c 'case "$1" in ` + tmpDir + `/*.conf) exit 0 ;; *) echo "got $1" ; exit 1 ;; esac' checker`
	res := runCheck(t, r, "file.managed", value.MapOf(
		"name", path, "contents", "x\n",
		"tmp_dir", tmpDir, "tmp_ext", ".conf",
	), []string{check}, false)

	if !res.Succeeded() {
		t.Fatalf("tmp_dir/tmp_ext were not honoured: %q", res.Comment)
	}
}

// file.serialize owns check_cmd in Salt too, and for the same reason.
func TestCheckCmdOnFileSerialize(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	rejected := runCheck(t, r, "file.serialize", value.MapOf(
		"name", path,
		"dataset", value.MapOf("a", int64(1)),
		"serializer", "json",
	), []string{"false"}, false)
	if rejected.Succeeded() {
		t.Fatalf("a rejected serialization was accepted: %+v", rejected)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the rejected file was written")
	}

	accepted := runCheck(t, r, "file.serialize", value.MapOf(
		"name", path,
		"dataset", value.MapOf("a", int64(1)),
		"serializer", "json",
	), []string{"true"}, false)
	if !accepted.Succeeded() {
		t.Fatalf("result = %v %q", accepted.Result, accepted.Comment)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the accepted file was not written: %v", err)
	}
}

func findVisudo(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"/usr/sbin/visudo", "/sbin/visudo", "/usr/local/sbin/visudo"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("visudo"); err == nil {
		return p
	}
	t.Skip("no visudo on this machine")
	return ""
}
