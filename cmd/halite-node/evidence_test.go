package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// nodeForEvidence builds a node whose state directory is a temporary one,
// with whatever extra configuration a test needs.
//
// Assembled rather than faked: the records are written from inside
// executor.Offer and node.refuse, so a test that called the recording
// functions directly would establish that they work and nothing about
// whether anything calls them. That is the shape `internal/hub`'s stub
// lesson recorded.
func nodeForEvidence(t *testing.T, extra string) *node {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Load(config.Node, config.LoadOptions{
		Path: writeConfig(t, dir, "state_dir: "+dir+"\n"+extra),
	})
	if err != nil {
		t.Fatal(err)
	}
	logger, err := log.New(log.Options{Level: log.Error, Format: log.JSON, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	n := &node{
		cfg:       cfg,
		registry:  builtin.New(),
		grains:    value.NewMap(0),
		nodeID:    "web1.example",
		env:       "base",
		pillarEnv: "base",
		format:    cli.Nested,
		log:       logger,
		secrets:   redact.New(),
		metrics:   newNodeMetrics(cfg),
		evidence:  &evidenceState{},
		pillars:   fileserver.NewRoots(map[string][]string{"base": {dir}}),
	}
	t.Cleanup(func() { n.stopEvidence("the test finished") })
	return n
}

func evidenceDirOf(t *testing.T, n *node) string {
	t.Helper()
	dir := n.evidenceDir()
	if dir == "" {
		t.Fatal("the node reports no evidence directory")
	}
	return dir
}

func chainOf(t *testing.T, n *node) *nodeevidence.Result {
	t.Helper()
	res, err := nodeevidence.Verify(evidenceDirOf(t, n))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("the chain this node wrote does not verify: %v", res.Breaks)
	}
	return res
}

// records reads every record of the chain in order, for a test that cares
// which kinds were written and what they hold.
func records(t *testing.T, n *node) []nodeevidence.Record {
	t.Helper()
	chainOf(t, n)
	dir := evidenceDirOf(t, n)
	segments, err := nodeevidence.Segments(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []nodeevidence.Record
	for _, segment := range segments {
		data, err := os.ReadFile(segment)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			var r nodeevidence.Record
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("%s: %v", segment, err)
			}
			out = append(out, r)
		}
	}
	return out
}

func jobMessage(t *testing.T, jid string) transport.Message {
	t.Helper()
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	return transport.Message{
		T:          transport.MsgJob,
		JID:        jid,
		Fun:        "test.ping",
		Nonce:      nonce,
		Expires:    time.Now().Add(job.DefaultTTL).UTC().Format(time.RFC3339Nano),
		Submitter:  "cert:CN=ops.example",
		OnBehalfOf: "alice@example",
	}
}

// The record exists before the job can have run, which is the only
// property that makes it worth keeping.
func TestAnAcceptedJobIsRecordedBeforeItRuns(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	// Depth 4 and nothing draining it: the job is queued and cannot have
	// executed, so a record that exists now is a record written first.
	n.executor = newExecutor(n, 4, func(*job.Return) {
		t.Error("a job ran; this test asserts the record is written before anything does")
	})

	n.acceptJob(jobMessage(t, "20260923T070000000001"))
	if got := n.executor.Depth(); got != 1 {
		t.Fatalf("the job is not waiting on the queue: depth %d", got)
	}

	recs := wantRecords(t, n, 1)
	if recs[0].Kind != nodeevidence.KindJobAccepted {
		t.Errorf("the record is %q, expected %q", recs[0].Kind, nodeevidence.KindJobAccepted)
	}
	if recs[0].Detail["jid"] != "20260923T070000000001" {
		t.Errorf("the record names job %q", recs[0].Detail["jid"])
	}
	if recs[0].NodeID != "web1.example" {
		t.Errorf("the record names node %q", recs[0].NodeID)
	}
}

// The principal the hub claimed is in the record, and is labelled as a
// claim.
//
// It could not be before this: the job message carried no submitter at
// all, so a node's own account of what it ran could say only that a hub
// had asked for it. SPEC 25.7 requires the principal.
func TestTheRecordHoldsTheClaimedPrincipal(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})

	n.acceptJob(jobMessage(t, "20260923T070000000002"))

	recs := wantRecords(t, n, 1)
	detail := recs[0].Detail
	if detail["claimed_submitter"] != "cert:CN=ops.example" {
		t.Errorf("claimed_submitter is %q", detail["claimed_submitter"])
	}
	if detail["claimed_on_behalf_of"] != "alice@example" {
		t.Errorf("claimed_on_behalf_of is %q", detail["claimed_on_behalf_of"])
	}
	// Named as a claim, not as `submitter`. A node authenticates its hub
	// and nothing behind it, and a field that reads as established fact
	// would be read as one by whoever finds the file.
	if _, plain := detail["submitter"]; plain {
		t.Error("the record calls the hub's assertion `submitter`, as though the node had checked it")
	}
}

// A refusal is recorded, whichever of the four refusal paths produced it.
func TestARefusedJobIsRecordedWithItsReason(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})

	msg := jobMessage(t, "20260923T070000000003")
	n.acceptJob(msg)
	// The same message again: SPEC 6.3's replay check refuses it.
	n.acceptJob(msg)

	recs := wantRecords(t, n, 2)
	if recs[1].Kind != nodeevidence.KindJobRefused {
		t.Fatalf("the second record is %q", recs[1].Kind)
	}
	if !strings.Contains(recs[1].Detail["refused"], "already run") {
		t.Errorf("the refusal does not say why: %q", recs[1].Detail["refused"])
	}
}

// A job with an unparseable expiry never reaches the executor, and is
// still a thing the hub asked this node to do.
func TestAMalformedJobIsRecorded(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})

	msg := jobMessage(t, "20260923T070000000004")
	msg.Expires = "not a timestamp"
	n.acceptJob(msg)

	recs := wantRecords(t, n, 1)
	if recs[0].Kind != nodeevidence.KindJobRefused {
		t.Fatalf("expected a refusal, got %v", kindsOf(recs))
	}
	if !strings.Contains(recs[0].Detail["refused"], "not a timestamp") {
		t.Errorf("the refusal does not name the problem: %q", recs[0].Detail["refused"])
	}
}

// The outcome is recorded too, so the chain answers what ran as well as
// what arrived.
func TestAFinishedJobIsRecorded(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	done := make(chan *job.Return, 1)
	e := newExecutor(n, 4, func(ret *job.Return) { done <- ret })
	n.executor = e
	stop := make(chan struct{})
	defer close(stop)
	go e.Run(stop)

	n.acceptJob(jobMessage(t, "20260923T070000000005"))
	select {
	case ret := <-done:
		if !ret.Success {
			t.Fatalf("test.ping failed: %s", ret.Return)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the job did not finish")
	}

	recs := wantRecords(t, n, 2)
	result := recs[1]
	if result.Kind != nodeevidence.KindJobResult {
		t.Fatalf("the second record is %q", result.Kind)
	}
	if result.Detail["success"] != "true" || result.Detail["retcode"] != "0" {
		t.Errorf("the result record says success=%q retcode=%q",
			result.Detail["success"], result.Detail["retcode"])
	}
}

// A secret that reached a job's arguments does not reach the file.
//
// SPEC 26.1 applies redaction at the sink, and this is a sink: the whole
// purpose of the record is that somebody else reads it later.
func TestArgumentsAreScrubbedBeforeTheyAreRecorded(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.secrets.Add("hunter2")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})

	msg := jobMessage(t, "20260923T070000000006")
	msg.Fun = "cmd.run"
	msg.Arg = []string{"mysql -p hunter2"}
	n.acceptJob(msg)

	recs := wantRecords(t, n, 1)
	if strings.Contains(recs[0].Detail["arg"], "hunter2") {
		t.Errorf("a known secret was written to the record: %q", recs[0].Detail["arg"])
	}
	if !strings.Contains(recs[0].Detail["arg"], "mysql") {
		t.Errorf("the arguments were not recorded at all: %q", recs[0].Detail["arg"])
	}
	if recs[0].Detail["arg_digest"] == "" {
		t.Error("no digest of the arguments was recorded")
	}
}

// The configuration record holds a digest and the files, and no values.
func TestTheConfigurationRecordHoldsNoValues(t *testing.T) {
	n := nodeForEvidence(t, "token: swordfish-not-in-the-record\n")
	n.startEvidence()

	recs := wantRecords(t, n, 2)
	if recs[0].Kind != nodeevidence.KindStart || recs[1].Kind != nodeevidence.KindConfig {
		t.Fatalf("got %v", kindsOf(recs))
	}
	if recs[1].Detail["digest"] == "" {
		t.Error("the configuration record carries no digest")
	}
	if !strings.Contains(recs[1].Detail["files"], "node.yaml") {
		t.Errorf("the configuration record does not name the files it came from: %q",
			recs[1].Detail["files"])
	}

	// The strongest form of the claim: the secret is nowhere in the
	// directory, whatever shape a record might have taken.
	dir := evidenceDirOf(t, n)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "swordfish") {
			t.Errorf("%s holds a configured value", e.Name())
		}
	}
}

// A configuration change moves the digest, which is the whole mechanism:
// one record per run, and a run whose digest differs from the last is a
// node whose configuration was edited in between.
func TestAChangedConfigurationChangesTheRecordedDigest(t *testing.T) {
	first := nodeForEvidence(t, "log_level: info\n")
	first.startEvidence()
	before := wantRecords(t, first, 2)[1].Detail["digest"]

	second := nodeForEvidence(t, "log_level: debug\n")
	second.startEvidence()
	after := wantRecords(t, second, 2)[1].Detail["digest"]

	if before == "" || after == "" {
		t.Fatal("no digest was recorded")
	}
	if before == after {
		t.Error("two different configurations recorded the same digest")
	}
}

// `evidence: false` writes nothing, and the directory is not created: a
// setting that is off must not leave a trace that looks like a record.
func TestEvidenceOffWritesNothing(t *testing.T) {
	n := nodeForEvidence(t, "evidence: false\n")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	n.startEvidence()
	n.acceptJob(jobMessage(t, "20260923T070000000007"))

	if _, err := os.Stat(n.evidenceDir()); !os.IsNotExist(err) {
		t.Errorf("the evidence directory exists with the setting off: %v", err)
	}
}

// And the diagnostic says so rather than passing.
func TestDoctorReportsAnUnwrittenChain(t *testing.T) {
	off := nodeForEvidence(t, "evidence: false\n")
	res := nodeEvidenceCheck(off).Run(t.Context())
	if res.Status != doctor.Warn {
		t.Errorf("a node keeping no record reports %s", res.Status)
	}

	empty := nodeForEvidence(t, "")
	res = nodeEvidenceCheck(empty).Run(t.Context())
	if res.Status != doctor.Warn {
		t.Errorf("a node whose chain is empty reports %s: %s", res.Status, res.Detail)
	}
	// And a diagnostic must not create what it is reporting on, or the
	// run that was asked "is anything being recorded" makes the answer
	// yes.
	if _, err := os.Stat(empty.evidenceDir()); !os.IsNotExist(err) {
		t.Error("the diagnostic created the evidence directory")
	}

	keeping := nodeForEvidence(t, "")
	keeping.startEvidence()
	res = nodeEvidenceCheck(keeping).Run(t.Context())
	if res.Status != doctor.Pass {
		t.Errorf("a node that is recording reports %s: %s", res.Status, res.Detail)
	}
	// A pass that says only "pass" is the thing this project's doctor
	// guard exists to stop: the count and the last record are the answer.
	if !strings.Contains(res.Detail, "2 records") || !strings.Contains(res.Detail, "config") {
		t.Errorf("the pass does not say what it found: %q", res.Detail)
	}
}

// wantRecords reads the chain and insists on a count.
//
// Every assertion below indexes into the records, and an index into an
// empty slice panics -- which takes the whole test binary down and loses
// every other test's result at the moment something is broken. Found by
// removing the acceptance record on purpose and watching one test panic
// instead of three tests failing.
func wantRecords(t *testing.T, n *node, want int) []nodeevidence.Record {
	t.Helper()
	recs := records(t, n)
	if len(recs) != want {
		t.Fatalf("expected %d record(s), got %d: %v", want, len(recs), kindsOf(recs))
	}
	return recs
}

// The branch that matters: `evidence` on, the directory there, and this
// account unable to write in it. It is the failure that looks like
// nothing at all, and `doctor` is a different process from the agent, so
// it has to ask the question itself rather than read the agent's answer.
func TestDoctorReportsADirectoryItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory with no write bit, so this proves nothing as root")
	}
	if runtime.GOOS == "windows" {
		// See the same skip in internal/nodeevidence: a mode does not
		// make a directory unwritable there, and the check's probe is
		// the same code on every platform.
		t.Skip("a mode cannot make a directory unwritable on Windows; the probe itself is not platform-specific")
	}
	n := nodeForEvidence(t, "")
	dir := n.evidenceDir()
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}

	res := nodeEvidenceCheck(n).Run(t.Context())
	if res.Status != doctor.Fail {
		t.Fatalf("an unwritable evidence directory reports %s: %s", res.Status, res.Detail)
	}
	if !strings.Contains(res.Remedy, dir) {
		t.Errorf("the remedy does not name the directory: %q", res.Remedy)
	}
}

func kindsOf(recs []nodeevidence.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Kind
	}
	return out
}
