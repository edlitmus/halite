package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	halexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The self-restart guard of issue 728, driven against the real
// `service(8)`, the real rc.subr and the real `ps`.
//
// # What it is for
//
// `freebsdRCProvider.RunsThisProcess` reads two things off the system and
// compares them: the pids rc.subr says a service is running under, and
// the chain of processes above this one. Both readings are prose from a
// tool, and the unit tests next door hold captures of that prose. A
// capture goes stale silently -- the whole point of this leg -- so this
// test makes the readings against the machine and compares the answer
// with a pid it chose itself.
//
// # The two services it writes
//
// `haliteprobeself` names **this test process** as its `procname` and
// holds this process's pid in its pidfile, so rc.subr reports the test
// binary as that service's process. The guard must then refuse to
// restart it.
//
// `haliteprobechild` names a `sleep` **this test started** -- a child,
// never an ancestor. The guard must not refuse that one, and it is the
// control that matters: an implementation that walked the process tree
// the wrong way, or compared sets rather than ancestry, passes the first
// assertion and fails this one. Stopping a service this process is above
// does not stop this process.
//
// Nothing here starts or stops a service: rc.subr's `status` needs only
// a pidfile, a `procname` and a live process, so the whole test is
// readings. That is deliberate -- a test that restarted a service whose
// pidfile points at the test binary would be asking rc.subr to kill the
// test.
//
// # Why it skips everywhere else
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveFreeBSDServiceSelf -v ./internal/builtin/
const liveSelfRCName = "haliteprobeself"

const liveChildRCName = "haliteprobechild"

// liveSelfRCScript is the throwaway rc.d script, filled in with the name,
// the pidfile and the procname.
//
// No `command`: `status` never runs it, and leaving it out means a stray
// `service <name> start` on a machine where this file survived can do
// nothing at all.
const liveSelfRCScript = `#!/bin/sh
#
# PROVIDE: %[1]s
# REQUIRE: LOGIN
#
# A throwaway service written by halite's live test for issue 728. If
# this file is still here, the test that wrote it did not finish; it is
# safe to remove. It starts nothing: it has no ` + "`command`" + `.

. /etc/rc.subr

name="%[1]s"
rcvar="%[1]s_enable"
pidfile="%[2]s"
procname="%[3]s"

load_rc_config $name
: ${%[1]s_enable:="NO"}

run_rc_command "$1"
`

// writeProbeRCService writes one throwaway rc.d script and its pidfile,
// and takes both away afterwards.
func writeProbeRCService(t *testing.T, name, procname string, pid int) {
	t.Helper()
	pidfile := filepath.Join(t.TempDir(), name+".pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", pidfile, err)
	}
	path := filepath.Join(liveRCScriptDir, name)
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s already exists; this test will not overwrite it", path)
	}
	script := fmt.Sprintf(liveSelfRCScript, name, pidfile, procname)
	if err := os.MkdirAll(liveRCScriptDir, 0o755); err != nil {
		t.Fatalf("the local rc.d directory could not be made: %v", err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func TestLiveFreeBSDServiceSelfRestartIsRefused(t *testing.T) {
	c := freebsdServiceLive(t)

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("this process cannot name its own binary: %v", err)
	}
	writeProbeRCService(t, liveSelfRCName, self, os.Getpid())

	// rc.subr's own answer first, so that a failure downstream can be
	// told apart from a script rc.subr never agreed with. This is also
	// the capture the unit tests' parser is written against: if the line
	// ever changes shape, it changes here.
	res, err := c.Run(halexec.Command{
		Argv:           []string{"service", liveSelfRCName, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`service %s status`: %v", liveSelfRCName, err)
	}
	line := strings.TrimSpace(res.Stdout)
	t.Logf("`service %s status` said: %q", liveSelfRCName, line)
	want := fmt.Sprintf("%s is running as pid %d.", liveSelfRCName, os.Getpid())
	if line != want {
		t.Fatalf("rc.subr did not report this process as the service:\n got %q\nwant %q", line, want)
	}

	pids, err := freebsdRCServicePIDs(c, liveSelfRCName)
	if err != nil {
		t.Fatalf("reading the service's pids: %v", err)
	}
	if len(pids) != 1 || pids[0] != os.Getpid() {
		t.Fatalf("freebsdRCServicePIDs = %v, want [%d]", pids, os.Getpid())
	}

	isSelf, err := freebsdRCProvider{}.RunsThisProcess(c, liveSelfRCName)
	if err != nil {
		t.Fatalf("RunsThisProcess: %v", err)
	}
	if !isSelf {
		t.Fatal("the service holding this process's pid was not recognised as this process's service")
	}

	refusal := serviceSelfActionRefusal(freebsdRCProvider{}, c, liveSelfRCName, "restarting")
	for _, phrase := range []string{
		"is the one running this process",
		"daemon -f /bin/sh -c 'sleep 30; service " + liveSelfRCName + " restart'",
	} {
		if !strings.Contains(refusal, phrase) {
			t.Errorf("the refusal does not name %q:\n%s", phrase, refusal)
		}
	}

	// And through the registry, which is the path a job takes.
	if _, err := New().Exec.Call(c, "service.restart", value.MapOf("name", liveSelfRCName)); err == nil {
		t.Fatal("service.restart on this process's own service returned no error")
	} else if !strings.Contains(err.Error(), "is the one running this process") {
		t.Fatalf("service.restart failed for the wrong reason: %v", err)
	}
}

// The control: a service whose process is a *child* of this one. Stopping
// it would not stop this process, so the guard must stay out of the way.
func TestLiveFreeBSDServiceBelowThisProcessIsNotRefused(t *testing.T) {
	c := freebsdServiceLive(t)

	sleeper := exec.Command("/bin/sleep", "120")
	if err := sleeper.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	t.Cleanup(func() {
		_ = sleeper.Process.Kill()
		_, _ = sleeper.Process.Wait()
	})
	writeProbeRCService(t, liveChildRCName, "/bin/sleep", sleeper.Process.Pid)

	pids, err := freebsdRCServicePIDs(c, liveChildRCName)
	if err != nil {
		t.Fatalf("reading the service's pids: %v", err)
	}
	if len(pids) != 1 || pids[0] != sleeper.Process.Pid {
		t.Fatalf("freebsdRCServicePIDs = %v, want [%d]", pids, sleeper.Process.Pid)
	}

	isSelf, err := freebsdRCProvider{}.RunsThisProcess(c, liveChildRCName)
	if err != nil {
		t.Fatalf("RunsThisProcess: %v", err)
	}
	if isSelf {
		t.Fatal("a service running a child of this process was taken for this process's own service")
	}
	if refusal := serviceSelfActionRefusal(freebsdRCProvider{}, c, liveChildRCName, "restarting"); refusal != "" {
		t.Fatalf("a service below this process was refused:\n%s", refusal)
	}
}

// processAncestry against the real process table, with the one pid every
// unix has above everything: init. A walk that stopped at the first hop
// or never started would miss it.
func TestLiveFreeBSDProcessAncestryReachesInit(t *testing.T) {
	c := freebsdServiceLive(t)

	ancestry, err := processAncestry(c, os.Getpid())
	if err != nil {
		t.Fatalf("processAncestry: %v", err)
	}
	for _, pid := range []int{os.Getpid(), os.Getppid(), 1} {
		if !ancestry[pid] {
			t.Errorf("the ancestry of %d does not hold %d: %v", os.Getpid(), pid, ancestry)
		}
	}
	if ancestry[0] {
		t.Errorf("the ancestry walk ran past init into pid 0: %v", ancestry)
	}
}
