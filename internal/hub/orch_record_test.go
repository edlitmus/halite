package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// A run's record is written as it goes, not only at its start and end
// (issue 802, DIVERGENCE 5.705). The binary test in cmd/halite-node kills a
// real hub mid-run; these hold the parts of it a unit can see exactly: what
// the record says at the moment a step is out on the fleet, and what a
// resume does with a step that was.

const threeStepDeploy = `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

migrate:
  salt.function:
    - name: test.echo
    - tgt: '*'
    - timeout: 1

deploy_web:
  salt.function:
    - name: pkg.install
    - tgt: '*'
`

// answeringWith connects a node that hands each job to onJob before
// answering it, and answers only when onJob says to.
func (l *lab) answeringWith(t *testing.T, client *transport.Client, nodeID string,
	onJob func(msg transport.Message) (answer bool)) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: nodeID},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-ready:
					default:
						close(ready)
					}
				case transport.MsgJob:
					if onJob != nil && !onJob(msg) {
						return nil
					}
					client.Return(ctx, job.Return{
						JID: job.ID(msg.JID), NodeID: nodeID, Fun: msg.Fun,
						Success: true, Return: json.RawMessage(`{}`), Schema: job.ReturnSchema,
					})
				}
				return nil
			})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("%s never connected", nodeID)
	}
	return func() { cancel(); <-stopped }
}

// newestRun is the most recent record in the store, as it is on disk at
// the moment of the call, or nil. Called from the node's goroutine, so it
// reports nothing itself: the test's assertions on a nil say what is
// missing.
func newestRun(l *lab) *OrchRun {
	runs, bad, err := l.server.orchStore().List(1)
	if err != nil || len(bad) > 0 || len(runs) == 0 {
		return nil
	}
	return runs[0]
}

// recordNaming waits for the newest record to name step as dispatched as
// jid, and returns it, or the last record read if it never does.
//
// A wait, because the job reaches the node before dispatchFor returns to
// the step that sent it: the record written before the dispatch names the
// step with no jid, and the one written after it names the jid, and a node
// that reads the record the instant its job arrives can see either.
func recordNaming(l *lab, step, jid string) *OrchRun {
	deadline := time.Now().Add(5 * time.Second)
	for {
		run := newestRun(l)
		if run != nil {
			for _, st := range run.Steps {
				if st.ID == step && st.JID == jid {
					return run
				}
			}
		}
		if time.Now().After(deadline) {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// seen is what the node's goroutine saw, handed to the test's. The two
// meet only through a socket, which the race detector cannot see through.
type seen struct {
	mu  sync.Mutex
	run *OrchRun
	jid string
	fun []string
}

func (s *seen) set(run *OrchRun, jid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run, s.jid = run, jid
}

func (s *seen) get() (*OrchRun, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run, s.jid
}

func (s *seen) sent(fun string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fun = append(s.fun, fun)
}

func (s *seen) funs() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.fun, ",")
}

func TestTheRecordNamesFinishedAndWaitingStepsWhileAStepIsOut(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": threeStepDeploy})
	var saw seen
	defer l.answeringWith(t, l.enrolled(t, "web1.example"), "web1.example", func(msg transport.Message) bool {
		if msg.Fun == "test.echo" {
			saw.set(recordNaming(l, "migrate", msg.JID), msg.JID)
		}
		return true
	})()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchComplete {
		t.Fatalf("the run is %s: %+v", run.State, run.Steps)
	}
	during, migrateJob := saw.get()
	if during == nil {
		t.Fatal("there was no record while the migrate job was on the node")
	}
	if during.State != OrchRunning {
		t.Errorf("the record during the run says %s", during.State)
	}
	if got := stepIDs(during); strings.Join(got, ",") != "drain,migrate" {
		t.Fatalf("the record during migrate names %v, want drain then migrate", got)
	}
	if drain := during.Steps[0]; drain.Pending || drain.Result == nil || !*drain.Result {
		t.Errorf("drain had returned, and the record says %+v", drain)
	}
	if migrate := during.Steps[1]; !migrate.Pending || migrate.Result != nil || migrate.JID != migrateJob {
		t.Errorf("migrate was out as job %s, and the record says %+v", migrateJob, migrate)
	}
	// And the finished record has nothing still waiting.
	for _, step := range run.Steps {
		if step.Pending {
			t.Errorf("the finished run still has %s waiting", step.ID)
		}
	}
}

// A resumed run's record names the steps it carried forward from the
// moment it starts its first real one, or a hub that died during that
// step would leave a record of the resumed run that a resume of it would
// read as "never ran" -- the same defect one resume later.
func TestAResumedRunRecordsItsCarriedStepsBeforeItsFirstStepReturns(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": threeStepDeploy})
	node := l.enrolled(t, "web1.example")
	stop := l.answeringWith(t, node, "web1.example", nil)
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}})
	if err != nil {
		t.Fatal(err)
	}
	stop()

	var saw seen
	defer l.answeringWith(t, node, "web1.example", func(msg transport.Message) bool {
		if msg.Fun == "pkg.install" {
			saw.set(recordNaming(l, "deploy_web", msg.JID), msg.JID)
		}
		return true
	})()
	if _, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, ResumeOf: first.JID, ResumeFrom: "deploy_web",
	}); err != nil {
		t.Fatal(err)
	}
	during, _ := saw.get()
	if during == nil || during.ResumedOf != first.JID {
		t.Fatalf("the resumed run's record was not the newest while deploy_web was out: %+v", during)
	}
	if got := stepIDs(during); strings.Join(got, ",") != "drain,migrate,deploy_web" {
		t.Fatalf("the resumed run's record during deploy_web names %v", got)
	}
	for _, id := range []string{"drain", "migrate"} {
		if step := stepByID(t, during, id); !step.Skipped || !strings.Contains(step.Comment, "Carried forward") {
			t.Errorf("%s is not recorded as carried forward: %+v", id, step)
		}
	}
}

// A step that was out on the fleet when the hub stopped is decided by its
// job record: refused while a node has not answered, and carried forward
// once every node has, without being sent again.
func TestAResumePastAStepThatWasOutReadsItsJob(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": threeStepDeploy})
	node := l.enrolled(t, "web1.example")
	var saw seen
	stop := l.answeringWith(t, node, "web1.example", func(msg transport.Message) bool {
		if msg.Fun == "test.echo" {
			// What was on disk at this moment is what a hub killed now
			// would have left.
			saw.set(recordNaming(l, "migrate", msg.JID), msg.JID)
			return false
		}
		return true
	})
	if _, err := l.server.Orchestrate(context.Background(), OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}}); err != nil {
		t.Fatal(err)
	}
	stop()
	atCrash, migrateJob := saw.get()
	if atCrash == nil {
		t.Fatal("there was no record while the migrate job was on the node")
	}
	if err := l.server.orchStore().Put(atCrash); err != nil {
		t.Fatal(err)
	}

	var after seen
	defer l.answeringWith(t, node, "web1.example", func(msg transport.Message) bool {
		after.sent(msg.Fun)
		return true
	})()
	resume := func() (*OrchRun, error) {
		return l.server.Orchestrate(context.Background(), OrchRequest{
			Principal: "cert:CN=ed", SLS: []string{"deploy"}, ResumeOf: atCrash.JID, ResumeFrom: "deploy_web",
		})
	}

	// The node has not answered migrate: nothing on the hub knows whether
	// it ran, so the resume is refused and says which job to look at.
	_, err := resume()
	if err == nil {
		t.Fatalf("a resume past a step with no return was accepted; the node was sent %v", after.funs())
	}
	for _, want := range []string{`"migrate"`, migrateJob, "have not returned", "--from migrate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if got := after.funs(); got != "" {
		t.Errorf("the refused resume sent %v", got)
	}

	// The node's return arrives late, as a node's retry does after the hub
	// is back.
	if err := node.Return(context.Background(), job.Return{
		JID: job.ID(migrateJob), NodeID: "web1.example", Fun: "test.echo",
		Success: true, Return: json.RawMessage(`{}`), Schema: job.ReturnSchema,
	}); err != nil {
		t.Fatal(err)
	}
	run, err := resume()
	if err != nil {
		t.Fatal(err)
	}
	if got := after.funs(); got != "pkg.install" {
		t.Errorf("the resume sent %v, want only pkg.install", got)
	}
	migrate := stepByID(t, run, "migrate")
	if !migrate.Skipped || !migrate.Succeeded() || !strings.Contains(migrate.Comment, "in flight when the hub stopped") {
		t.Errorf("migrate is not carried forward from its job: %+v", migrate)
	}
	if migrate.JID != migrateJob {
		t.Errorf("the carried step names job %q, want %s", migrate.JID, migrateJob)
	}
	if run.State != OrchComplete {
		t.Errorf("the resumed run is %s", run.State)
	}
}

// A hub that stopped between writing that a step was dispatching and
// learning its job's jid leaves a step that may or may not have gone out,
// with nothing to look it up by. A resume past it is refused rather than
// guessed, and resuming at it is allowed.
func TestAResumePastAStepCaughtDispatchingIsRefused(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": threeStepDeploy})
	node := l.enrolled(t, "web1.example")
	var saw seen
	stop := l.answeringWith(t, node, "web1.example", func(msg transport.Message) bool {
		if msg.Fun == "test.echo" {
			saw.set(recordNaming(l, "migrate", msg.JID), msg.JID)
		}
		return true
	})
	if _, err := l.server.Orchestrate(context.Background(), OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}}); err != nil {
		t.Fatal(err)
	}
	stop()
	atCrash, _ := saw.get()
	if atCrash == nil {
		t.Fatal("there was no record while the migrate job was on the node")
	}
	migrate := stepByID(t, atCrash, "migrate")
	migrate.JID, migrate.Nodes = "", nil
	if err := l.server.orchStore().Put(atCrash); err != nil {
		t.Fatal(err)
	}

	var after seen
	defer l.answeringWith(t, node, "web1.example", func(msg transport.Message) bool {
		after.sent(msg.Fun)
		return true
	})()
	_, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, ResumeOf: atCrash.JID, ResumeFrom: "deploy_web",
	})
	if err == nil || !strings.Contains(err.Error(), "being dispatched") || !strings.Contains(err.Error(), "--from migrate") {
		t.Fatalf("a resume past a step caught dispatching was not refused as one: %v (sent %s)", err, after.funs())
	}
	if got := after.funs(); got != "" {
		t.Errorf("the refused resume sent %v", got)
	}
	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, ResumeOf: atCrash.JID, ResumeFrom: "migrate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := after.funs(); got != "test.echo,pkg.install" || run.State != OrchComplete {
		t.Errorf("resuming at the step sent %v and is %s", got, run.State)
	}
}
