package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/job"
)

// runOneshotFor runs `halite-node oneshot` in-process against req, with
// stdin and stdout swapped for files and the inline tree made under a
// directory of the test's own, and returns the exit status, what was
// written, and that directory.
//
// Not parallel: it swaps os.Stdin and os.Stdout for the process.
func runOneshotFor(t *testing.T, req OneshotRequest) (int, string, string) {
	t.Helper()
	root := t.TempDir()
	temp := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := oneshotTempRoot
	oneshotTempRoot = temp
	t.Cleanup(func() { oneshotTempRoot = saved })

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(in, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	savedIn, savedOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdin, stdout
	defer func() { os.Stdin, os.Stdout = savedIn, savedOut }()

	t.Setenv("HALITE_NODE_ID", "oneshot-test")
	args, err := cli.Parse([]string{"--root", root})
	if err != nil {
		t.Fatal(err)
	}
	code := runOneshot(args)
	written, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(written), temp
}

// leftInlineTrees names what a run left in its temporary directory.
func leftInlineTrees(t *testing.T, temp string) []string {
	t.Helper()
	entries, err := os.ReadDir(temp)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	return left
}

// Every agentless `state.*` run made a `halite-oneshot-*` directory of
// the hub's state and template files and nothing ever removed it.
// DIVERGENCE 5.318.
//
// The state reads a file the hub sent, through file.managed's source,
// so the tree is shown to have been in use during the run and gone
// after it -- a removal that happened before the state ran would fail
// the state rather than pass this test.
func TestAnAgentlessStateRunRemovesItsInlineTree(t *testing.T) {
	target := filepath.Join(t.TempDir(), "managed.conf")
	// What is on disk when the frame is written. Read through the hook
	// rather than after the run, because a defer would also have removed
	// it by then.
	var atFrame []string
	saved := oneshotFrame
	oneshotFrame = func(ret *job.Return) {
		entries, _ := os.ReadDir(oneshotTempRoot)
		for _, e := range entries {
			atFrame = append(atFrame, e.Name())
		}
		saved(ret)
	}
	t.Cleanup(func() { oneshotFrame = saved })
	code, out, temp := runOneshotFor(t, OneshotRequest{
		Protocol: OneshotProtocol,
		JID:      "20261015000000000001",
		Fun:      "state.apply",
		Arg:      []string{"web"},
		Files: map[string]string{
			"web.sls": "managed:\n  file.managed:\n    - name: " + filepath.ToSlash(target) +
				"\n    - source: salt://files/managed.conf\n",
			"files/managed.conf": "served from the inline tree\n",
		},
	})
	if code != 0 {
		t.Fatalf("the run exited %d; output:\n%s", code, out)
	}
	if len(atFrame) != 0 {
		t.Errorf("the tree %v was still there when the return was written; "+
			"a hub that has gone away makes that write a SIGPIPE, which runs no defers", atFrame)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "served from the inline tree\n" {
		t.Fatalf("the state did not apply the file the hub sent: %q, %v\noutput:\n%s", got, err, out)
	}
	if left := leftInlineTrees(t, temp); len(left) != 0 {
		t.Errorf("a successful agentless run left %v in the temporary directory", left)
	}
}

func TestAFailedAgentlessStateRunRemovesItsInlineTree(t *testing.T) {
	code, out, temp := runOneshotFor(t, OneshotRequest{
		Protocol: OneshotProtocol,
		JID:      "20261015000000000002",
		Fun:      "state.apply",
		Arg:      []string{"broken"},
		Files: map[string]string{
			"broken.sls": "fails:\n  test.fail_without_changes: []\n",
		},
	})
	if code == 0 {
		t.Fatalf("a state that fails exited 0; output:\n%s", out)
	}
	if !strings.Contains(out, OneshotBegin) {
		t.Fatalf("a failed run wrote no framed return:\n%s", out)
	}
	if left := leftInlineTrees(t, temp); len(left) != 0 {
		t.Errorf("a failed agentless run left %v in the temporary directory", left)
	}
}

// A file the hub sent that cannot be written fails the run before the
// job starts, after the directory was made.
func TestAnAgentlessRunWhoseTreeCannotBeWrittenRemovesIt(t *testing.T) {
	code, out, temp := runOneshotFor(t, OneshotRequest{
		Protocol: OneshotProtocol,
		Fun:      "state.apply",
		Files:    map[string]string{"../escape.sls": "x: {}\n"},
	})
	if code == 0 || !strings.Contains(out, "leaves the tree") && !strings.Contains(out, "not a path inside the tree") {
		t.Fatalf("a path out of the tree was not refused; exit %d, output:\n%s", code, out)
	}
	if left := leftInlineTrees(t, temp); len(left) != 0 {
		t.Errorf("a run refused while writing its tree left %v in the temporary directory", left)
	}
}

// A run ended by a signal removes the tree before it exits, with the
// status a process the signal killed would have had. The signal is
// delivered on a channel rather than to the test binary.
func TestAnAgentlessRunEndedBySignalRemovesItsInlineTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "halite-oneshot-1")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tree := &inlineTree{dir: dir}
	ch := make(chan os.Signal, 1)
	var (
		mu     sync.Mutex
		status = -1
		exited = make(chan struct{})
	)
	stop := tree.watch(nil, ch, func(code int) {
		mu.Lock()
		status = code
		mu.Unlock()
		close(exited)
	}, func() {})
	ch <- syscall.SIGTERM
	<-exited
	stop()

	mu.Lock()
	defer mu.Unlock()
	if want := 128 + int(syscall.SIGTERM); status != want {
		t.Errorf("exit status %d, want %d", status, want)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the tree survived the signal: %v", err)
	}
}

// Stopping the watch without a signal neither removes the tree nor
// exits: the normal path removes it itself, before writing.
func TestStoppingTheSignalWatchLeavesTheTreeToTheRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "halite-oneshot-2")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tree := &inlineTree{dir: dir}
	stop := tree.watch(nil, make(chan os.Signal), func(code int) {
		t.Errorf("exited %d with no signal", code)
	}, func() {})
	stop()
	stop()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the tree went with no signal and no remove: %v", err)
	}
	tree.remove(nil)
	tree.remove(nil)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("remove left the tree: %v", err)
	}
}
