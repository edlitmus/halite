package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Issue 765 through the real binaries: a hub, a node that requires a
// signature on every job, and `halite-hub orch plan` signing on the
// operator's side with a key the hub never sees.
//
// The node is unchanged by issue 765 and is the control. So the test
// shows it refusing the orchestration unsigned, running it from a plan,
// and the hub refusing -- before anything reaches the node -- a plan whose
// step changed on the hub's side, a plan that has expired, and a resume
// given the original plan; and then a resume on a fresh plan running the
// step that failed and nothing before it.
func TestAPlannedOrchestrationRunsOnANodeThatRequiresSignatures(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and a node")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix paths in the tree")
	}
	bin := t.TempDir()
	for _, cmd := range []string{"halite-hub", "halite-node"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, cmd),
			"github.com/edlitmus/halite/cmd/"+cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", cmd, err, out)
		}
	}
	p := newPlanLab(t, bin)

	// Unsigned, the node refuses the state step: this is what issue 765
	// is about.
	out, code := p.hub("orch", "run", "deploy", "--pillar", `{"version":"1.4.2"}`, "--out", "json")
	if code == 0 {
		t.Fatalf("the unsigned orchestration succeeded on a node that requires signatures:\n%s", out)
	}
	p.waitForNodeLog("requires a signature")
	if _, err := os.Stat(p.marker); err == nil {
		t.Fatal("the unsigned state step changed the node")
	}

	// Planned and signed off the hub, it runs.
	plan := filepath.Join(p.dir, "deploy.plan.json")
	out, code = p.hub("orch", "plan", "deploy", "--tree", p.states, "--pillar", `{"version":"1.4.2"}`,
		"--sign-key", p.signerKey, "--out", plan)
	if code != 0 {
		t.Fatalf("orch plan: %s", out)
	}
	t.Logf("orch plan:\n%s", out)
	out, code = p.hub("orch", "run", "deploy", "--pillar", `{"version":"1.4.2"}`, "--plan", plan, "--out", "json")
	if code != 0 {
		t.Fatalf("the planned orchestration failed:\n%s\nnode:\n%s", out, tail(p.nodeLog.String(), 15))
	}
	p.wantMarker("1.4.2 app_version=1.4.2")
	run := decodeRun(t, out)
	for _, id := range []string{"warm", "apply"} {
		if step := run.step(t, id); !step.Signed {
			t.Errorf("the record does not say %s sent a signed job: %+v", id, step)
		}
	}

	// A step changed on the hub's side after the plan was signed: the hub
	// refuses the whole run, naming the step, and sends nothing. A fresh
	// plan, because the first one's jobs are spent.
	fresh := filepath.Join(p.dir, "fresh.plan.json")
	if out, code := p.hub("orch", "plan", "deploy", "--tree", p.states, "--pillar", `{"version":"1.4.3"}`,
		"--sign-key", p.signerKey, "--out", fresh); code != 0 {
		t.Fatalf("orch plan: %s", out)
	}
	p.write("hub/states/deploy.sls", strings.Replace(deploySLS, "- tgt: plan1\n    - sls", "- tgt: 'plan*'\n    - sls", 1))
	before := p.jobsSeen()
	out, code = p.hub("orch", "run", "deploy", "--pillar", `{"version":"1.4.3"}`, "--plan", fresh)
	if code == 0 || !strings.Contains(out, "refused before it started") || !strings.Contains(out, `apply is signed for target "plan1"`) {
		t.Fatalf("a changed target was not refused by name:\n%s\nnode:\n%s", out, tail(p.nodeLog.String(), 6))
	}
	p.wantNoNewJob(before)
	p.write("hub/states/deploy.sls", deploySLS)

	// An expired plan is refused before any step.
	short := filepath.Join(p.dir, "short.plan.json")
	if out, code := p.hub("orch", "plan", "deploy", "--tree", p.states, "--pillar", `{"version":"1.4.3"}`,
		"--sign-key", p.signerKey, "--ttl", "1s", "--out", short); code != 0 {
		t.Fatalf("orch plan: %s", out)
	}
	time.Sleep(1500 * time.Millisecond)
	before = p.jobsSeen()
	out, code = p.hub("orch", "run", "deploy", "--pillar", `{"version":"1.4.3"}`, "--plan", short)
	if code == 0 || !strings.Contains(out, "the plan expired at") {
		t.Fatalf("an expired plan was not refused:\n%s", out)
	}
	p.wantNoNewJob(before)

	// A step fails: the state cannot be written yet.
	p.write("hub/states/v2_0.sls", p.brokenRelease())
	failing := filepath.Join(p.dir, "failing.plan.json")
	if out, code := p.hub("orch", "plan", "deploy", "--tree", p.states, "--pillar", `{"version":"2.0"}`,
		"--sign-key", p.signerKey, "--out", failing); code != 0 {
		t.Fatalf("orch plan: %s", out)
	}
	out, code = p.hub("orch", "run", "deploy", "--pillar", `{"version":"2.0"}`, "--plan", failing, "--out", "json")
	if code == 0 {
		t.Fatalf("the broken state did not fail the run:\n%s", out)
	}
	failed := decodeRun(t, out)
	if step := failed.step(t, "apply"); step.Result == nil || *step.Result {
		t.Fatalf("apply did not fail: %+v", step)
	}

	// The cause is fixed. The original plan cannot resume it.
	p.write("hub/states/v2_0.sls", p.release("2.0"))
	before = p.jobsSeen()
	out, code = p.hub("orch", "resume", failed.JID, "--from", "apply", "--pillar", `{"version":"2.0"}`, "--plan", failing)
	if code == 0 || !strings.Contains(out, "a resume always needs a new plan") {
		t.Fatalf("a resume on the original plan was not refused:\n%s", out)
	}
	p.wantNoNewJob(before)

	// A fresh resume plan signs `apply` and what follows, and the resumed
	// run sends that and does not send `warm` again.
	resume := filepath.Join(p.dir, "resume.plan.json")
	if out, code := p.hub("orch", "plan", "deploy", "--tree", p.states, "--pillar", `{"version":"2.0"}`,
		"--sign-key", p.signerKey, "--resume-of", failed.JID, "--from", "apply", "--out", resume); code != 0 {
		t.Fatalf("orch plan --resume-of: %s", out)
	}
	before = p.jobsSeen()
	out, code = p.hub("orch", "resume", failed.JID, "--from", "apply", "--pillar", `{"version":"2.0"}`,
		"--plan", resume, "--out", "json")
	if code != 0 {
		t.Fatalf("the resume on a fresh plan failed:\n%s\nnode:\n%s", out, tail(p.nodeLog.String(), 15))
	}
	p.wantMarker("2.0 app_version=2.0")
	resumed := decodeRun(t, out)
	if step := resumed.step(t, "warm"); step.JID != "" && step.Signed {
		t.Errorf("the carried-forward step was sent again: %+v", step)
	}
	if got := p.jobsSeen() - before; got != 1 {
		t.Errorf("the resume sent %d job(s) to the node, want only apply's", got)
	}
}

const deploySLS = `warm:
  salt.function:
    - name: test.ping
    - tgt: plan1

apply:
  salt.state:
    - tgt: plan1
    - sls: [{{ 'v' ~ pillar['version'] | replace('.', '_') }}]
    - pillar:
        app_version: "{{ pillar['version'] }}"
    - require:
      - salt: warm

announce:
  salt.runner:
    - name: event.send
    - arg: [halite/audit/deployed]
    - require:
      - salt: apply
`

// release is the state one version applies. It writes both the version
// in the SLS name the orchestration picked and the `app_version` the
// step's signed `pillar:` override carries, so a marker reading
// `1.4.2 app_version=1.4.2` says the right file ran and the override
// reached it. Writing this test is how issue 820 was found: the node read
// no override at all, and the second half came out `MISSING`.
func (p *planLab) release(version string) string {
	return "marker:\n  file.managed:\n    - name: " + p.marker + "\n    - makedirs: True\n" +
		"    - contents: \"" + version + " app_version={{ pillar.get('app_version', 'MISSING') }}\"\n"
}

// brokenRelease writes beneath a regular file, which no node can do.
func (p *planLab) brokenRelease() string {
	return "marker:\n  file.managed:\n    - name: " + filepath.Join(p.dir, "out", "blocker", "inside") +
		"\n    - contents: \"2.0\"\n"
}

type planLab struct {
	t                   *testing.T
	dir, states, marker string
	hubBin, nodeBin     string
	hubArgs, nodeArgs   []string
	signerKey           string
	nodeLog             *syncBuffer
}

func newPlanLab(t *testing.T, bin string) *planLab {
	t.Helper()
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	p := &planLab{
		t: t, dir: dir, states: filepath.Join(dir, "hub", "states"),
		marker: filepath.Join(dir, "out", "marker"),
		hubBin: filepath.Join(bin, "halite-hub"), nodeBin: filepath.Join(bin, "halite-node"),
		hubArgs:  []string{"--config", dir + "/hub/hub.yaml", "--root", dir + "/hub", "--pki-dir", dir + "/hub/pki"},
		nodeArgs: []string{"--config", dir + "/node/node.yaml", "--root", dir + "/node"},
	}
	p.write("out/blocker", "a file, not a directory\n")
	p.write("hub/states/deploy.sls", deploySLS)
	p.write("hub/states/v1_4_2.sls", p.release("1.4.2"))
	p.write("hub/states/v1_4_3.sls", p.release("1.4.3"))
	p.write("hub/pillar/top.sls", "base: {}\n")
	p.write("hub/hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/hub/state\ncache_dir: %[2]s/hub/cache\n"+
		"enrollment_mode: manual\nfile_roots: {base: [%[2]s/hub/states]}\npillar_roots: {base: [%[2]s/hub/pillar]}\n",
		addr, dir))

	// The signer's key is made where the operator keeps it, not in the
	// hub's directory; the node is told its public half.
	p.signerKey = filepath.Join(dir, "operator", "ops.key")
	if err := os.MkdirAll(filepath.Dir(p.signerKey), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(p.hubBin, append([]string{"keys", "signer", "create", "ops",
		"--out", strings.TrimSuffix(p.signerKey, ".key")}, p.hubArgs...)...).CombinedOutput(); err != nil {
		t.Fatalf("keys signer create: %v\n%s", err, out)
	}
	public, err := os.ReadFile(strings.TrimSuffix(p.signerKey, ".key") + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	p.write("node/node.yaml", fmt.Sprintf("hub: %s\nnode_id: plan1\npki_dir: %[2]s/node/pki\nstate_dir: %[2]s/node/state\n"+
		"cache_dir: %[2]s/node/cache\nfile_roots: {base: [%[2]s/node/local]}\npillar_roots: {base: [%[2]s/node/localp]}\n"+
		"require_job_signature: true\njob_signer_keys:\n  - '%[3]s'\n", addr, dir, strings.TrimSpace(string(public))))

	// The first serve makes the enrollment CA, which the operator's
	// certificate is issued from; the hub reads the policy `--admin`
	// writes only when it starts, so it is started again after.
	serve := append([]string{"serve", "--names", "127.0.0.1,localhost"}, p.hubArgs...)
	first := exec.Command(p.hubBin, serve...)
	var firstLog syncBuffer
	first.Stdout, first.Stderr = &firstLog, &firstLog
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(filepath.Join(dir, "hub", "pki", "ca.crt")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = first.Process.Kill()
	_ = first.Wait()
	if out, err := exec.Command(p.hubBin, append([]string{"keys", "operator", "create", "ed", "--admin"},
		p.hubArgs...)...).CombinedOutput(); err != nil {
		t.Fatalf("keys operator create: %v\n%s\nfirst serve:\n%s", err, out, firstLog.String())
	}
	p.start(p.hubBin, nil, serve...)
	fingerprint := ""
	for i := 0; i < 100 && fingerprint == ""; i++ {
		if out, err := exec.Command(p.hubBin, append([]string{"keys", "fingerprint"}, p.hubArgs...)...).Output(); err == nil {
			if fields := strings.Fields(string(out)); len(fields) > 0 {
				fingerprint = fields[len(fields)-1]
			}
		}
		if fingerprint == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	for i := 0; i < 200; i++ {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	enroll := exec.Command(p.nodeBin, append([]string{"enroll", "--wait", "--hub-fingerprint", fingerprint}, p.nodeArgs...)...)
	var enrollLog syncBuffer
	enroll.Stdout, enroll.Stderr = &enrollLog, &enrollLog
	if err := enroll.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = enroll.Process.Kill() })
	for i := 0; i < 200; i++ {
		if exec.Command(p.hubBin, append([]string{"keys", "accept", "plan1"}, p.hubArgs...)...).Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := enroll.Wait(); err != nil {
		t.Fatalf("enroll: %v\n%s", err, enrollLog.String())
	}
	p.nodeLog = &syncBuffer{}
	p.start(p.nodeBin, p.nodeLog, append([]string{"connect"}, p.nodeArgs...)...)
	// Connected when the hub lists it: an orchestration started before
	// would find no node and fail for the wrong reason.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if out, _ := p.hub("runner", "manage.up"); strings.Contains(out, "plan1") {
			return p
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the node never connected:\n%s", tail(p.nodeLog.String(), 12))
	return nil
}

func (p *planLab) write(rel, body string) {
	p.t.Helper()
	path := filepath.Join(p.dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *planLab) start(name string, log *syncBuffer, args ...string) {
	p.t.Helper()
	cmd := exec.Command(name, args...)
	if log == nil {
		log = &syncBuffer{}
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
}

// hub runs one operator command and returns its combined output and exit
// code.
func (p *planLab) hub(args ...string) (string, int) {
	p.t.Helper()
	cmd := exec.Command(p.hubBin, append(args, p.hubArgs...)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			p.t.Fatalf("%v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// jobsSeen counts the jobs this node has been offered, accepted or not,
// from its own log.
func (p *planLab) jobsSeen() int {
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`"jid":"([0-9T]+)"`).FindAllStringSubmatch(p.nodeLog.String(), -1) {
		seen[m[1]] = true
	}
	return len(seen)
}

func (p *planLab) wantNoNewJob(before int) {
	p.t.Helper()
	time.Sleep(time.Second)
	if after := p.jobsSeen(); after != before {
		p.t.Fatalf("the node was sent something after the hub refused the run:\n%s", tail(p.nodeLog.String(), 10))
	}
}

func (p *planLab) wantMarker(want string) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(p.marker)
		if strings.TrimSpace(string(got)) == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.t.Fatalf("the marker holds %q, want %q; the node's log ends:\n%s", got, want, tail(p.nodeLog.String(), 12))
}

func (p *planLab) waitForNodeLog(line string) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.nodeLog.String(), line) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	p.t.Fatalf("the node never logged %q:\n%s", line, tail(p.nodeLog.String(), 12))
}

type planRunStep struct {
	ID     string `json:"id"`
	JID    string `json:"job_jid"`
	Result *bool  `json:"result"`
	Signed bool   `json:"signed"`
}

type planRun struct {
	JID   string        `json:"jid"`
	Steps []planRunStep `json:"steps"`
}

func (r planRun) step(t *testing.T, id string) planRunStep {
	t.Helper()
	for _, s := range r.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("the run has no step %s: %+v", id, r)
	return planRunStep{}
}

// decodeRun reads `orch run --out json`, whose JSON is followed, on a
// failed run, by the error line on stderr.
func decodeRun(t *testing.T, out string) planRun {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON in:\n%s", out)
	}
	var run planRun
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&run); err != nil {
		t.Fatalf("decoding the run: %v\n%s", err, out)
	}
	return run
}
