package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A node revoked while it was running exits 1 once, on the hub's revoke
// message. Started again -- by its supervisor, a reboot or by hand -- it has
// no stream to hear that on: the hub refuses its certificate in the
// handshake, and connect read the alert as an outage and retried it for
// ever under the default `hub_tries: 0`. The supervisor's rules for exit 1
// (systemd's RestartPreventExitStatus=1, OpenRC's respawn cap) never fired,
// because the process never exited. Issue 839.
//
// Real binaries, because the alert is what a real hub's TLS stack sends and
// the node's reading of it is the whole of the defect: a fake error built in
// a unit test would have tested what somebody thought the hub sends.
func TestARevokedNodeStartedAgainExitsOneAndSaysToReEnroll(t *testing.T) {
	lab := newRevokeLab(t, "revoked1")
	lab.startHub()
	lab.enroll()

	running, runningLog := lab.startNode()
	lab.waitHubLog(`"msg":"node connected","node_id":"revoked1"`)
	lab.hubCommand("keys", "revoke", "revoked1", "--reason", "decommissioned")
	if code := waitExit(t, running, 30*time.Second, runningLog); code != 1 {
		t.Fatalf("the running node exited %d on its revocation, want 1\n%s", code, runningLog.String())
	}

	restarted, restartedLog := lab.startNode()
	if code := waitExit(t, restarted, 30*time.Second, restartedLog); code != 1 {
		t.Fatalf("a revoked node started again exited %d, want 1\n%s", code, restartedLog.String())
	}
	log := restartedLog.String()
	// The hint must be the whole of what works: the hub refuses a request
	// for a name it already knows under another key, so `enroll --force`
	// alone is refused and the operator has to delete the record first
	// (issue 880; TestARevokedNodeEnrollsAgainOnceItsRecordIsDeleted).
	for _, want := range []string{"refused this node's certificate", "halite-hub keys delete revoked1", "halite-node enroll --force"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log does not say %q:\n%s", want, log)
		}
	}
	// Once, nearly always. Go's HTTP/2 client sometimes loses the alert
	// and reports only that the connection could not be established
	// (transport's lostAlert); that attempt is retried, and the next one
	// reads the alert. Three allows for two such losses in a row while
	// still failing the retry for ever this test was written for.
	if n := strings.Count(log, `"msg":"connecting"`); n > 3 {
		t.Errorf("the revoked node tried %d times, want at most 3:\n%s", n, log)
	}
}

// What an operator has to do to bring a revoked node back, run end to end: the
// node's log says to enrol again, and `enroll --force` on its own died with
// `tls: bad certificate`, because it presented the revoked node.crt to a hub
// that ends such a handshake (issue 880). It now reaches the hub, and the hub
// refuses a request for a name it holds under another key -- on purpose, so
// that nobody can claim a node's name -- until the record is deleted. The
// sequence is the one the log line and handbook chapter 37 give, and it ends
// with the node connected on its new certificate.
func TestARevokedNodeEnrollsAgainOnceItsRecordIsDeleted(t *testing.T) {
	lab := newRevokeLab(t, "revoked3")
	lab.startHub()
	lab.enroll()

	running, runningLog := lab.startNode()
	lab.waitHubLog(`"msg":"node connected","node_id":"revoked3"`)
	lab.hubCommand("keys", "revoke", "revoked3", "--reason", "decommissioned")
	if code := waitExit(t, running, 30*time.Second, runningLog); code != 1 {
		t.Fatalf("the running node exited %d on its revocation, want 1\n%s", code, runningLog.String())
	}

	forced := &syncBuffer{}
	flags := append([]string{"enroll", "--force", "--hub-fingerprint", lab.hubFingerprint()}, lab.nodeArgs...)
	code := waitExit(t, lab.start(forced, lab.nodeBin, flags...), 30*time.Second, forced)
	if strings.Contains(forced.String(), "bad certificate") {
		t.Errorf("enroll --force presented the revoked certificate to the hub:\n%s", forced.String())
	}
	if !strings.Contains(forced.String(), "enrollment refused") {
		t.Errorf("enroll --force did not reach the hub's decision on the request:\n%s", forced.String())
	}
	if code != 1 {
		t.Errorf("enroll --force on a name the hub still holds exited %d, want 1\n%s", code, forced.String())
	}

	lab.hubCommand("keys", "delete", "revoked3")
	lab.enrollWith("--force")

	_, againLog := lab.startNode()
	lab.waitHubLogTimes(`"msg":"node connected","node_id":"revoked3"`, 2)
	if strings.Contains(againLog.String(), "refused this node's certificate") {
		t.Errorf("the node was refused on the certificate it was just issued:\n%s", againLog.String())
	}
}

// The other half of the distinction: a node started while its hub is down,
// or restarting, goes on trying until the hub is back. Exiting on anything
// but the hub's refusal of the certificate would turn a hub restart into a
// fleet its supervisors had stopped restarting.
func TestANodeStartedWhileItsHubIsDownConnectsWhenItComesBack(t *testing.T) {
	lab := newRevokeLab(t, "outage2")
	stopHub := lab.startHub()
	lab.enroll()
	stopHub()

	node, nodeLog := lab.startNode()
	// Several refused dials and their backoff, so that what is tested is a
	// node that has been retrying rather than one that has not tried yet.
	deadline := time.Now().Add(20 * time.Second)
	for strings.Count(nodeLog.String(), `"msg":"connecting"`) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the node did not retry a hub that was down:\n%s", nodeLog.String())
		}
		select {
		case err := <-node.done:
			t.Fatalf("the node exited while its hub was down: %v\n%s", err, nodeLog.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	lab.startHub()
	lab.waitHubLog(`"msg":"node connected","node_id":"outage2"`)
	select {
	case err := <-node.done:
		t.Fatalf("the node exited after its hub came back: %v\n%s", err, nodeLog.String())
	default:
	}
}

type revokeLab struct {
	t               *testing.T
	dir, addr       string
	hubBin, nodeBin string
	hubArgs         []string
	nodeArgs        []string
	nodeID          string
	hubLogs         []*syncBuffer
}

type runningProcess struct {
	cmd  *exec.Cmd
	done chan error
}

func newRevokeLab(t *testing.T, nodeID string) *revokeLab {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs a hub and a node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("stops the hub with a signal and uses Unix paths")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, cmd := range []string{"halite-hub", "halite-node"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, cmd),
			"github.com/edlitmus/halite/cmd/"+cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", cmd, err, out)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	lab := &revokeLab{
		t: t, dir: dir, addr: addr, nodeID: nodeID,
		hubBin:   filepath.Join(bin, "halite-hub"),
		nodeBin:  filepath.Join(bin, "halite-node"),
		hubArgs:  []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"},
		nodeArgs: []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"},
	}
	lab.write("hub/states/top.sls", "base: {}\n")
	lab.write("hub/pillar/top.sls", "base: {}\n")
	lab.write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n", addr, dir))
	lab.write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: %s\npki_dir: %[3]s/node/pki\nstate_dir: %[3]s/node/state\n"+
		"cache_dir: %[3]s/node/cache\nfile_roots: {base: [%[3]s/node/local]}\npillar_roots: {base: [%[3]s/node/localp]}\n",
		addr, nodeID, dir))
	return lab
}

func (l *revokeLab) write(rel, body string) {
	l.t.Helper()
	p := filepath.Join(l.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		l.t.Fatal(err)
	}
}

func (l *revokeLab) start(log *syncBuffer, name string, args ...string) *runningProcess {
	l.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		l.t.Fatal(err)
	}
	p := &runningProcess{cmd: cmd, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	l.t.Cleanup(func() { _ = cmd.Process.Kill() })
	return p
}

// startHub starts the hub and returns once it is listening: a fingerprint on
// disk is not a listening hub (TestAScheduledHighstateSurvivesAHubOutage says
// how that was learned).
func (l *revokeLab) startHub() (stop func()) {
	l.t.Helper()
	log := &syncBuffer{}
	l.hubLogs = append(l.hubLogs, log)
	p := l.start(log, l.hubBin, append([]string{"serve", "--names", "127.0.0.1,localhost"}, l.hubArgs...)...)
	for i := 0; i < 200; i++ {
		c, err := net.DialTimeout("tcp", l.addr, time.Second)
		if err == nil {
			c.Close()
			return func() { _ = p.cmd.Process.Kill(); <-p.done }
		}
		time.Sleep(100 * time.Millisecond)
	}
	l.t.Fatalf("the hub never listened on %s\n%s", l.addr, log.String())
	return nil
}

func (l *revokeLab) hubCommand(args ...string) string {
	l.t.Helper()
	out, err := exec.Command(l.hubBin, append(args, l.hubArgs...)...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("halite-hub %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (l *revokeLab) enroll() { l.enrollWith() }

// hubFingerprint is the pin a node enrolling against this hub is given.
func (l *revokeLab) hubFingerprint() string {
	l.t.Helper()
	fields := strings.Fields(l.hubCommand("keys", "fingerprint"))
	if len(fields) == 0 {
		l.t.Fatal("the hub printed no fingerprint")
	}
	return fields[len(fields)-1]
}

// enrollWith enrolls the node and has the hub accept it, with extra flags for
// `halite-node enroll` such as --force.
func (l *revokeLab) enrollWith(extra ...string) {
	l.t.Helper()
	log := &syncBuffer{}
	flags := append([]string{"enroll", "--wait", "--hub-fingerprint", l.hubFingerprint()}, extra...)
	p := l.start(log, l.nodeBin, append(flags, l.nodeArgs...)...)
	for i := 0; i < 200; i++ {
		select {
		case err := <-p.done:
			l.t.Fatalf("enroll ended before its request was accepted: %v\n%s", err, log.String())
		default:
		}
		if exec.Command(l.hubBin, append([]string{"keys", "accept", l.nodeID}, l.hubArgs...)...).Run() == nil {
			if err := <-p.done; err != nil {
				l.t.Fatalf("enroll: %v\n%s", err, log.String())
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	l.t.Fatalf("the node's request was never accepted\n%s", log.String())
}

func (l *revokeLab) startNode() (*runningProcess, *syncBuffer) {
	l.t.Helper()
	log := &syncBuffer{}
	return l.start(log, l.nodeBin, append([]string{"connect"}, l.nodeArgs...)...), log
}

// waitHubLog waits for the most recently started hub to log want.
func (l *revokeLab) waitHubLog(want string) { l.waitHubLogTimes(want, 1) }

// waitHubLogTimes waits for the most recently started hub to have logged want
// at least times times, for an event that has already happened once.
func (l *revokeLab) waitHubLogTimes(want string, times int) {
	l.t.Helper()
	log := l.hubLogs[len(l.hubLogs)-1]
	deadline := time.Now().Add(30 * time.Second)
	for strings.Count(log.String(), want) < times {
		if time.Now().After(deadline) {
			l.t.Fatalf("the hub logged %q %d time(s), want %d\n%s", want, strings.Count(log.String(), want), times, log.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitExit returns a process's exit code, and fails the test, with the
// process's log, if it is still running after bound.
func waitExit(t *testing.T, p *runningProcess, bound time.Duration, log *syncBuffer) int {
	t.Helper()
	select {
	case err := <-p.done:
		if err == nil {
			return 0
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		t.Fatalf("waiting for the process: %v", err)
	case <-time.After(bound):
		_ = p.cmd.Process.Kill()
		t.Fatalf("the process was still running after %s\n%s", bound, log.String())
	}
	return -1
}
