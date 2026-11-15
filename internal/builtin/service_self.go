package builtin

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
)

// serviceSelfIdentifier is an extra interface a provider implements when
// it can tell that a named service is the one running this process --
// that stopping that service would stop the agent applying the state.
//
// It is an interface rather than a method on serviceProvider because the
// answer is init-system-specific and only two init systems can give it:
// systemd puts every process of a unit in that unit's cgroup, and
// FreeBSD's rc.subr will name a service's pids. A provider that cannot
// tell does not implement it, and the guard below does nothing -- which
// is the honest outcome, because a guard that guessed would refuse
// restarts that are perfectly safe.
type serviceSelfIdentifier interface {
	// RunsThisProcess reports whether the named service's processes
	// include this one or one of its ancestors -- the two cases where
	// stopping the service takes this process with it.
	//
	// The ancestors matter as much as this process: a `cmd.run` that
	// shells out to `halite-node call service.restart halite_node` is a
	// grandchild of the agent, and the stop kills the agent above it,
	// which cancels the command.
	RunsThisProcess(c *exec.Context, name string) (bool, error)

	// DetachedRestart is a command line that restarts the service from
	// outside this process tree, for the refusal to name. It is the
	// recipe `contrib/examples/tree/states/halite/node.sls` ships, which
	// was measured end to end in DIVERGENCE 5.482.
	DetachedRestart(name string) string

	// SelfActionConsequence says what this init system does when the
	// service running this process is restarted from inside it.
	//
	// **The two platforms differ, and a message that said the worse one
	// everywhere would be the kind of claim this guard exists to
	// prevent.** On FreeBSD the service stays down. On systemd it comes
	// back, because the restart is a job for pid 1 rather than work done
	// by the caller -- which is why 5.663 called Linux "clean". It is not
	// clean: the job still never returns, so nothing reports whether the
	// change took. Both measured, DIVERGENCE 5.663.1.
	SelfActionConsequence() string
}

// serviceSelfActionRefusal is the comment a restart or reload of the
// service running this process must fail with, or "" when the named
// service is something else.
//
// **Refusing is the whole point, and the alternative was considered and
// rejected.** Detaching the restart silently would report success for a
// restart that has not happened yet and could not report the failure of
// one -- an undemonstrated claim written into a job return, which is the
// one thing this project will not ship. Issue 728 argued the same way and
// asked for the refusal.
//
// What it does *not* cover is deliberate: `service.dead` and
// `service.stop` on the agent's own service are left alone, because they
// do exactly what they say. The job's return is lost either way, but the
// operator asked for the service to stop and it stops; a restart that
// reports success and leaves the host with no agent is a different claim.
//
// gerund is "restarting" or "reloading" -- what is being refused.
func serviceSelfActionRefusal(p serviceProvider, c *exec.Context, name, gerund string) string {
	id, ok := p.(serviceSelfIdentifier)
	if !ok {
		return ""
	}
	self, err := id.RunsThisProcess(c, name)
	if err != nil {
		// A detection that failed must not block a restart: the common
		// case is a one-shot `halite-node state apply --local`, where
		// nothing is at risk and the answer was going to be "no". Say so
		// rather than deciding either way in silence.
		c.Logf("warn", "whether %s is the service running this process could not be read, so the self-%s guard did not run: %v", name, gerund, err)
		return ""
	}
	if !self {
		return ""
	}
	return fmt.Sprintf(
		"The service %s is the one running this process, so %s it from here would stop this process before it could finish: %s. "+
			"Restart it from outside this process instead -- `cmd.run: %s` with an `onchanges` on whatever changed -- and leave "+
			"the `service.running` state without a `watch`, so the next run starts a service that did not come back. "+
			"See docs/handbook/33-configuration.md section 6.",
		name, gerund, id.SelfActionConsequence(), id.DetachedRestart(name))
}

// ---- systemd ----

// selfCgroupPath is where this process reads its own cgroup. A variable
// so that a test can hand it a captured file rather than needing to be
// running under the unit it is reasoning about.
var selfCgroupPath = "/proc/self/cgroup"

func (systemdProvider) RunsThisProcess(c *exec.Context, name string) (bool, error) {
	units, err := systemdSelfUnits()
	if err != nil {
		return false, err
	}
	want := withServiceSuffix(name)
	for _, u := range units {
		if u == want {
			return true, nil
		}
	}
	return false, nil
}

// Measured on system76-pc (Ubuntu 24.04, systemd 255, kernel 6.18.7) on
// 2026-11-15: a hub-dispatched `watch` against a guard-free build left
// `halite-hub run` with no answer at all until it was killed at 100 s,
// while the unit went from pid 258282 to 258314, stayed `active`, and
// recorded `NRestarts: 0` -- the start was part of the restart job, not a
// recovery from failure.
func (systemdProvider) SelfActionConsequence() string {
	return "this job would never return, so nothing would report whether the change took, " +
		"even though systemd starts the service again"
}

func (systemdProvider) DetachedRestart(name string) string {
	unit := withServiceSuffix(name)
	return fmt.Sprintf("systemd-run --on-active=30 --unit=%s-restart systemctl restart %s",
		strings.TrimSuffix(unit, ".service"), unit)
}

// systemdSelfUnits names every unit whose cgroup this process sits in.
//
// Measured on system76-pc (issue 728) and on this project's Linux leg:
// /proc/self/cgroup under cgroup v2 is a single line,
//
//	0::/system.slice/halite-node.service
//
// and under v1 one line per controller, `4:pids:/system.slice/....`. Both
// are "hierarchy:controllers:path", so the path is what is after the
// second colon either way.
//
// **Every `.service` and `.scope` element counts, not just the last
// one.** systemd puts a unit's own subgroups beneath it, so a process can
// be at `/system.slice/foo.service/payload`; and stopping a unit kills its
// whole subtree, so a process under `user@1000.service/...` is stopped by
// `user@1000.service` as surely as by the leaf. The question this answers
// is "would stopping that unit stop me", and the answer is yes for any
// unit on the path.
func systemdSelfUnits() ([]string, error) {
	data, err := os.ReadFile(selfCgroupPath)
	if err != nil {
		return nil, err
	}
	var units []string
	for _, line := range strings.Split(string(data), "\n") {
		path := line
		// "hierarchy:controllers:path", and a controller list may hold
		// no colon of its own, so two splits are enough.
		if parts := strings.SplitN(line, ":", 3); len(parts) == 3 {
			path = parts[2]
		}
		for _, element := range strings.Split(path, "/") {
			if strings.HasSuffix(element, ".service") || strings.HasSuffix(element, ".scope") {
				units = append(units, element)
			}
		}
	}
	return units, nil
}

// ---- FreeBSD rc ----

// selfPID is this process's pid, as a variable so that a test can state
// a pid that is in the process table it also states.
var selfPID = os.Getpid

func (freebsdRCProvider) RunsThisProcess(c *exec.Context, name string) (bool, error) {
	servicePIDs, err := freebsdRCServicePIDs(c, name)
	if err != nil {
		return false, err
	}
	if len(servicePIDs) == 0 {
		// Not running. Nothing to stop, so nothing can stop this
		// process -- and `service.running` has a start to do.
		return false, nil
	}
	ancestry, err := processAncestry(c, selfPID())
	if err != nil {
		return false, err
	}
	for _, pid := range servicePIDs {
		if ancestry[pid] {
			return true, nil
		}
	}
	return false, nil
}

// Measured on beastie (FreeBSD 15.1-RELEASE-p6) on 2026-11-15, and on
// r720 before that (5.663): the restart script is a child of this
// process, so halite's own command confinement kills it when the agent
// shuts down, and the start never happens.
func (freebsdRCProvider) SelfActionConsequence() string {
	return "this job would never return and the service would stay down, because the restart " +
		"runs as a child of this process and is killed with it"
}

func (freebsdRCProvider) DetachedRestart(name string) string {
	return fmt.Sprintf("daemon -f /bin/sh -c 'sleep 30; service %s restart'", name)
}

// freebsdRCStatusRunning is the fixed part of the line rc.subr's `status`
// prints for a running service, from rc.subr itself:
//
//	echo "${name} is running as pid $rc_pid."
//
// Captured on beastie (FreeBSD 15.1-RELEASE-p6):
//
//	sshd is running as pid 10335.
//
// `$rc_pid` is unquoted there and check_process prints a space-separated
// list, so a service with no pidfile whose procname matches several
// processes prints several numbers before the full stop. No base service
// on the dev host produced one, so it was produced on purpose -- a
// throwaway script with no pidfile and three matching processes:
//
//	haliteprobemulti is running as pid 3886 3887 3888.
//
// DIVERGENCE 5.664.
const freebsdRCStatusRunning = " is running as pid "

// freebsdRCServicePIDs asks rc.subr for the pids of a service.
//
// It parses prose, which this project distrusts, and the alternative was
// worse: the pidfile would have to be guessed, because an rc.d script
// names its own `pidfile` and rc.subr has no command that prints it --
// the same trap as `${name}_enable` versus the script's own `rcvar`
// (DIVERGENCE 5.123's neighbour). `status` is rc.subr asking the script,
// so it is right for a port's script as much as for base.
//
// An unparseable line is an error and not an empty list: "no pids" is
// read as "nothing to stop", and reporting that for a line nobody
// anticipated would turn a broken parser into a silently absent guard.
func freebsdRCServicePIDs(c *exec.Context, name string) ([]int, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"service", name, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(firstLine(res.Stdout))
	index := strings.Index(line, freebsdRCStatusRunning)
	if index < 0 {
		// "<name> is not running." is the other line rc.subr prints, and
		// it is the whole of the not-running case.
		return nil, nil
	}
	rest := strings.TrimSuffix(line[index+len(freebsdRCStatusRunning):], ".")
	var pids []int
	for _, field := range strings.Fields(rest) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("the status of %s named %q where a pid was expected: %q", name, field, line)
		}
		pids = append(pids, pid)
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("the status of %s named no pid: %q", name, line)
	}
	return pids, nil
}

// maxProcessAncestryDepth bounds the walk up the process table. A parent
// map built from a `ps` snapshot can disagree with itself -- a pid reused
// between the rows -- and a cycle in it would hang the agent rather than
// fail it.
const maxProcessAncestryDepth = 64

// processAncestry is the set holding pid and every process above it.
//
// `ps -ax -o pid= -o ppid=` rather than `-o pid=,ppid=`: FreeBSD's ps
// takes everything after the `=` as that one column's header, so the
// comma form asks for a single `pid` column headed ",ppid=" -- which it
// printed, on beastie, as a header line and one column. Checked against
// the real tool before this was written down.
func processAncestry(c *exec.Context, pid int) (map[int]bool, error) {
	res, err := c.Run(exec.Command{Argv: []string{"ps", "-ax", "-o", "pid=", "-o", "ppid="}})
	if err != nil {
		return nil, err
	}
	parent := make(map[int]int)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		child, cerr := strconv.Atoi(fields[0])
		up, perr := strconv.Atoi(fields[1])
		if cerr != nil || perr != nil {
			continue
		}
		parent[child] = up
	}
	ancestry := map[int]bool{pid: true}
	for depth := 0; depth < maxProcessAncestryDepth; depth++ {
		up, ok := parent[pid]
		if !ok || up == 0 || ancestry[up] {
			break
		}
		ancestry[up] = true
		pid = up
	}
	return ancestry, nil
}
