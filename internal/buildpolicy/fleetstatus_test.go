package buildpolicy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// A live leg's step exits with the suite's own status.
//
// The `linux` and `freebsd` legs run the suite inside
// `{ go test ...; echo $? > /tmp/live.status; } | tee /tmp/live.txt` and
// then `exit "$(cat /tmp/live.status)"`, carrying the status through a
// file because a pipeline's status is tee's. Each step also begins
// `set -e`, which the brace group inherits: when the suite failed, the
// group stopped at `go test` and never wrote the file. GitHub runs a
// Linux step as `bash -e` without pipefail, so the pipeline still
// succeeded, `cat` found nothing, and the step died on `exit ""` --
// "numeric argument required", status 2 -- rather than with the suite's
// status. Fleet run 37342637678 showed it. The leg still failed, which
// is why nobody had noticed; the freebsd leg has the same lines and had
// never had a failing suite to show it.
//
// So this runs each such step, as it is written in fleet.yml, with sudo
// replaced by a stub standing in for the suite, under the shell that leg
// uses, and requires the step to exit with the stub's status and to have
// kept its output. The FreeBSD leg's shell is FreeBSD's /bin/sh, which
// is not here; it is run under every POSIX sh this host has, which is
// the same rule set and not the same binary.
func TestFleetLiveStepsExitWithTheSuitesStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the steps are POSIX shell, and this host has none to run them under")
	}
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "fleet.yml"))
	if err != nil {
		t.Fatal(err)
	}
	steps := statusCarryingSteps(string(body))
	if len(steps) < 2 {
		t.Fatalf("found %d step(s) carrying /tmp/live.status, expected the linux and "+
			"freebsd legs' at least; this test is not reading what it thinks", len(steps))
	}

	var posix []string
	for _, sh := range []string{"sh", "dash"} {
		if p, err := exec.LookPath(sh); err == nil {
			posix = append(posix, p)
		}
	}
	bash, bashErr := exec.LookPath("bash")

	for _, step := range steps {
		// How the runner invokes the script: GitHub's default for a
		// Linux step is `bash -e {0}`; cross-platform-actions hands the
		// FreeBSD step to sh, and the script sets -e itself.
		var shells [][]string
		if step.cpa {
			for _, p := range posix {
				shells = append(shells, []string{p})
			}
		} else if bashErr == nil {
			shells = append(shells, []string{bash, "-e"})
		}
		if len(shells) == 0 {
			t.Errorf("line %d: no shell here to run it under", step.line)
			continue
		}
		for _, shell := range shells {
			for _, want := range []int{0, 3} {
				got, out := runStatusStep(t, step.script, shell, want)
				if got != want {
					t.Errorf("fleet.yml line %d under %s: the suite exited %d and the step "+
						"exited %d:\n%s", step.line, strings.Join(shell, " "), want, got, out)
				}
			}
		}
	}
}

type statusStep struct {
	line   int
	cpa    bool
	script string
}

// statusCarryingSteps is every `run: |` block in the workflow that
// writes /tmp/live.status, with whether its step runs under cpa.sh.
func statusCarryingSteps(workflow string) []statusStep {
	lines := strings.Split(workflow, "\n")
	var out []statusStep
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "run: |" {
			continue
		}
		indent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
		cpa := false
		for j := i - 1; j >= 0 && j > i-4; j-- {
			if strings.Contains(lines[j], "shell: cpa.sh") {
				cpa = true
			}
		}
		var block []string
		j := i + 1
		for ; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) != "" && len(l)-len(strings.TrimLeft(l, " ")) <= indent {
				break
			}
			block = append(block, l)
		}
		script := strings.Join(block, "\n")
		if strings.Contains(script, "/tmp/live.status") {
			out = append(out, statusStep{line: i + 1, cpa: cpa, script: script})
		}
		i = j - 1
	}
	return out
}

// runStatusStep runs one step with sudo standing in for the suite, which
// prints a line and exits with status. /tmp is moved into the test's own
// directory so that nothing here touches the host's.
func runStatusStep(t *testing.T, script string, shell []string, status int) (int, string) {
	t.Helper()
	dir := t.TempDir()
	stub := "sudo() { echo the-suite-ran; return " + strconv.Itoa(status) + "; }\n"
	body := stub + strings.ReplaceAll(script, "/tmp/", dir+"/")
	path := filepath.Join(dir, "step.sh")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell[0], append(shell[1:], path)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(filepath.Join(dir, "live.txt")); !strings.Contains(string(kept), "the-suite-ran") {
		t.Errorf("under %s the step did not keep the suite's output in live.txt", shell[0])
	}
	return code, string(out)
}
