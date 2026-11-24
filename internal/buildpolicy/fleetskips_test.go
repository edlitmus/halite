package buildpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The live legs' skip report says why each test skipped, from what that
// test logged.
//
// It printed the line before each `--- SKIP`, which is the reason only
// when nothing is logged after it (issue 857). In Fleet job 138603165785,
// 7 of the linux leg's 23 skips read wrong: the kernelpkg removal test's
// "reason" was the last package of the kernel set its Cleanup put back,
// and six iptables and nftables tests, which skip with a network-namespace
// child's output, read "PASS". The report is only a report, but it is the
// one place a person reads why a live test did not run, and a skip that
// reads as something else is a skip nobody looks at.
//
// Both inputs here are captured: that job's suite output, from its log
// with the timestamps taken off (testdata/fleet-linux-live-138603165785.txt),
// and `go test -v` run on testdata/gotest-v-skip-shapes.go.txt, which
// makes the shapes that log does not show -- parallel tests, subtests, a
// skip that logs nothing. The second is where the rule about subtests came
// from: their log lines are at four spaces under their own name, not at
// eight as the first version of the script assumed.
func TestFleetSkipReportsReadEachTestsOwnBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the report is an awk program run by the live legs' POSIX shells")
	}
	root := repoRoot(t)
	script := filepath.Join(root, ".github", "skip-reasons.awk")

	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "fleet.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	// Every place the workflow reads a skip's reason uses the script: the
	// linux and freebsd legs' reports, and the snap step's.
	if n := strings.Count(workflow, "awk -f .github/skip-reasons.awk /tmp/live.txt"); n < 3 {
		t.Errorf("fleet.yml runs .github/skip-reasons.awk %d time(s); the linux and freebsd reports and the "+
			"snap step should all read their skips with it", n)
	}
	for _, old := range []string{"prev=$0", "grep -B 1 '^--- SKIP"} {
		if strings.Contains(workflow, old) {
			t.Errorf("fleet.yml still reads a skip's reason as the line before it (%s)", old)
		}
	}
	if !strings.Contains(workflow, `- ".github/skip-reasons.awk"`) {
		t.Error("fleet.yml's trigger paths do not name .github/skip-reasons.awk, so a change to it would " +
			"never run the legs that use it")
	}

	awks := awksHere()
	if len(awks) == 0 {
		t.Skip("no awk here to run the script under")
	}
	live := filepath.Join(root, "internal", "buildpolicy", "testdata", "fleet-linux-live-138603165785.txt")
	shapes := filepath.Join(root, "internal", "buildpolicy", "testdata", "gotest-v-skip-shapes.txt")
	liveBody, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	var skipped []string
	for _, m := range regexp.MustCompile(`(?m)^--- SKIP: (\S+)`).FindAllStringSubmatch(string(liveBody), -1) {
		skipped = append(skipped, m[1])
	}
	if len(skipped) != 23 {
		t.Fatalf("the captured log has %d top-level skips, and 23 were read from it; this test is not "+
			"reading what it thinks", len(skipped))
	}

	for _, awk := range awks {
		out := runAwk(t, awk, script, live)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		named := map[string]bool{}
		for _, l := range lines {
			name, said, ok := strings.Cut(l, ": ")
			if !ok {
				t.Errorf("%s: a line with no test name: %q", awk, l)
				continue
			}
			named[name] = true
			if strings.TrimSpace(said) == "PASS" {
				t.Errorf("%s: %s's reason reads %q, which is a namespace child's last line", awk, name, said)
			}
			// The Cleanup's kernel set, which the old reading printed.
			if strings.Contains(said, "linux-modules-7.0.0-1014-aws 7.0.0-1014") {
				t.Errorf("%s: %s's reason is a line of what its Cleanup logged: %q", awk, name, said)
			}
		}
		for _, name := range skipped {
			if !named[name] {
				t.Errorf("%s: the skip of %s is not reported", awk, name)
			}
		}
		for _, want := range []string{
			"TestLiveIPTablesSaveConverges: live_iptables_save_test.go:21: ran inside a network namespace:",
			"TestLiveACLStatesOnNFSv4ACLs: live_acl_state_test.go:253: Linux's getfacl speaks POSIX.1e only; this is linux",
			"TestLiveKernelpkgRemovesAnOlderKernelAndRefusesTheRest: live_kernelpkg_test.go:729: slow mirror: ",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the report does not say %q:\n%s", awk, want, out)
			}
		}

		// The shapes, line for line. Each reason is the test's own; a
		// subtest's log and skip are not its parent's; parallel tests do
		// not take each other's lines; a Cleanup's entry is reported
		// (first line only) beside the skip message, not instead of it.
		want := strings.Join([]string{
			"TestPlainSkip: shapes_test.go:10: the plain reason",
			"TestSkipThenCleanupLogs: shapes_test.go:14: before the skip",
			"TestSkipThenCleanupLogs: shapes_test.go:15: the reason, with the cleanup after it",
			"TestSkipThenCleanupLogs: shapes_test.go:13: cleanup put things back:",
			"TestMultiLineSkip: shapes_test.go:19: ran inside a child:",
			"TestSkipWithSubtests: shapes_test.go:25: the parent's reason, after its subtests",
			"TestSilentSkip: (it logged nothing)",
			"TestParallelA: shapes_test.go:34: A before",
			"TestParallelA: shapes_test.go:36: A's reason",
			"TestParallelB: shapes_test.go:42: B logs while A waits",
			"TestParallelB: shapes_test.go:44: B's reason",
		}, "\n") + "\n"
		if got := runAwk(t, awk, script, shapes); got != want {
			t.Errorf("%s on the captured shapes:\ngot:\n%swant:\n%s", awk, got, want)
		}
	}
}

// awksHere is every distinct awk on this host: the one-true-awk that macOS
// and FreeBSD ship, and mawk and gawk where Linux has them. The freebsd
// leg's is FreeBSD's own, which is not here; the one-true-awk is its
// ancestor and the closest this can come.
func awksHere() []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range []string{"awk", "mawk", "gawk", "nawk", "original-awk"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		if !seen[real] {
			seen[real] = true
			out = append(out, p)
		}
	}
	return out
}

func runAwk(t *testing.T, awk, script, input string) string {
	t.Helper()
	out, err := exec.Command(awk, "-f", script, input).Output()
	if err != nil {
		t.Fatalf("%s -f %s %s: %v", awk, script, input, err)
	}
	return string(out)
}
