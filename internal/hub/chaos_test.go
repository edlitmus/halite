package hub

import (
	"context"
	"crypto"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/chaos"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// SPEC 31's chaos layer, the scenarios that live in the hub.
//
// Each names its scenario through `chaos.Exercises`, which is what ties
// it to the behaviour written down in internal/chaos and what the guard
// there reads to find out whether a scenario has a test at all. The
// behaviour is logged rather than restated here: a failure should say
// what was supposed to happen, and the place that says it is the
// registry, so the two cannot drift.

// says logs the behaviour a scenario is defined to have, so a failing
// run shows the contract next to the failure.
func says(t *testing.T, s chaos.Scenario) {
	t.Helper()
	t.Logf("%s\n  defined: %s\n  not established: %s", s.Key, s.Behaviour, s.Limit)
}

// A hub that stops part way through a batch has, on disk, both what it
// did and what it did not — and resuming does not repeat the first half.
//
// The record is written before delivery with the whole node set, which
// is SPEC 9.1 step 4 and is what makes this answerable at all. What this
// adds to `TestABatchIsResumable` is a genuinely different Server over
// the same cache, and the assertion that matters: no node appears in
// `Delivered` twice. A resume that re-sent the first slice would apply a
// change to half the estate for a second time, which is the failure a
// resumable batch exists to prevent rather than to cause.
func TestChaosHubRestartMidJob(t *testing.T) {
	s := chaos.Exercises(chaos.HubRestartMidJob)
	says(t, s)

	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")
	const nodes = 4
	for i := 1; i <= nodes; i++ {
		l.enrolled(t, fmt.Sprintf("web%d.example", i))
	}

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "2", BatchTimeoutSecs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// First: the record names every node before any of them is reached.
	// That is SPEC 9.1 step 4 and is the whole reason a stopped hub can
	// be asked what it had not done yet.
	j, err := l.server.Jobs.Get(job.ID(res.JID))
	if err != nil {
		t.Fatalf("no record for a job that was accepted: %v", err)
	}
	if len(j.Nodes) != nodes {
		t.Fatalf("the record names %d nodes, want %d: the whole set is written before "+
			"delivery so that a stopped hub knows who it never reached", len(j.Nodes), nodes)
	}
	l.stop(t)

	// The interrupted state is written rather than produced by stopping
	// the hub, and that is deliberate. Stopping it *drains*: `Serve`
	// waits for the batch goroutine, so a graceful stop always leaves a
	// finished batch and never the half-done one this scenario is
	// about. A hub that was killed leaves whatever the last write put
	// there, which is what this constructs.
	half := j.Nodes[:nodes/2]
	if _, err := l.server.Jobs.Update(j.JID, func(cur *job.Job) error {
		cur.Delivered = append([]string(nil), half...)
		cur.State = job.Batching
		cur.Expires = time.Now().Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("killed with %d of %d delivered: %v", len(half), nodes, half)

	// A different Server over the same cache. No listener: Resume is
	// about the record and the remainder, and giving it a network would
	// test the network.
	restarted := &Server{
		Log:     l.server.Log,
		Jobs:    l.server.Jobs,
		Nodes:   l.server.Nodes,
		Policy:  l.server.Policy,
		Now:     l.server.Now,
		Metrics: l.server.Metrics,
	}
	if _, err := restarted.Resume(context.Background(), job.ID(res.JID)); err != nil {
		t.Fatalf("resuming after a restart: %v", err)
	}
	restarted.background.Wait()

	j, err = l.server.Jobs.Get(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, node := range j.Delivered {
		seen[node]++
	}
	for node, n := range seen {
		if n > 1 {
			t.Errorf("%s was delivered to %d times across the restart; resuming re-sent "+
				"a slice that had already gone out", node, n)
		}
	}
	if len(seen) != nodes {
		t.Errorf("%d of %d nodes reached after the resume: %v", len(seen), nodes, j.Delivered)
	}

	// And a job whose window closed while the hub was down is not
	// resumed at all. An instruction arriving long after the operator
	// stopped expecting it is the hazard SPEC 9.5 is explicit about.
	stale, err := l.server.Jobs.Update(job.ID(res.JID), func(cur *job.Job) error {
		cur.Delivered = nil
		cur.Expires = time.Now().Add(-time.Minute)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resume(context.Background(), stale.JID); err == nil {
		t.Error("a job whose window had closed was resumed anyway")
	}
}

// A node that goes away and comes back runs a queued job once.
//
// The partition is a closed stream, which is what the hub sees whichever
// way the network broke. Three things are asserted and the third is the
// one that was a defect: the job is reported absent rather than
// delivered while the node is away; it is delivered when the node
// returns; and **reconnecting a second time delivers nothing**, because
// the spool entry was cleared and stayed cleared. DIVERGENCE 4.11 is
// what happens when it does not.
func TestChaosNetworkPartition(t *testing.T) {
	s := chaos.Exercises(chaos.NetworkPartition)
	says(t, s)

	l := newLab(t).withJobs(t).withEvents(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	// Connected, then partitioned.
	stop := l.connect(t, client, "web1.example", "{}")
	waitFor(t, 5*time.Second, "the node to be connected", func() bool {
		return len(l.server.fleet().Connected()) == 1
	})
	stop()
	waitFor(t, 5*time.Second, "the node to be seen as gone", func() bool {
		return len(l.server.fleet().Connected()) == 0
	})

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Absent) != 1 || res.Absent[0] != "web1.example" {
		t.Fatalf("a partitioned node was not reported absent: %v", res.Absent)
	}

	// The partition heals.
	stop = l.connect(t, client, "web1.example", "{}")
	status := waitForReturns(t, op, res.JID, 1)
	if len(status.Returns) != 1 {
		t.Fatalf("%d returns after the partition healed", len(status.Returns))
	}
	stop()

	// And it does not run again. This is the assertion the scenario is
	// for: the spool entry has to have been cleared and stayed cleared
	// through the return that arrived at the same moment.
	j, err := l.server.Jobs.Get(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if j.IsQueuedFor("web1.example") {
		t.Fatal("the node is still owed a job it has already run; the next reconnection " +
			"would run it a second time")
	}
	waitFor(t, 5*time.Second, "the node to be seen as gone again", func() bool {
		return len(l.server.fleet().Connected()) == 0
	})

	// The same node connects again. A second delivery cannot be seen in
	// the return count -- returns are idempotent by (jid, node, chunk),
	// so the hub would file one return for two runs -- which is exactly
	// why this watches the messages instead.
	delivered, stopWatch := l.watching(t, client, "web1.example")
	defer stopWatch()
	select {
	case jid := <-delivered:
		t.Errorf("reconnecting delivered %s a second time; the node would run an "+
			"instruction the operator issued once, twice", jid)
	case <-time.After(2 * time.Second):
	}
}

// A write that fails is reported at submission and shrugged off after
// delivery, and those are different on purpose.
//
// A job the hub cannot record is a job that cannot be resumed, audited
// or killed, so refusing it is the only answer that does not lie. A
// bookkeeping write that fails *after* the instruction has gone out is
// different: the node has it, and failing the job would be a second
// untruth on top of the first.
//
// The injected failure is a directory that cannot be created, which is
// the error class ENOSPC lands in without being ENOSPC. Filling a disk
// in a unit test is not something to do to somebody's machine.
func TestChaosDiskFull(t *testing.T) {
	s := chaos.Exercises(chaos.DiskFull)
	says(t, s)

	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")
	l.enrolled(t, "web1.example")

	// The cache segments by day, so a plain file where today's
	// directory belongs makes every write for today fail.
	blocked := filepath.Join(l.server.Jobs.Dir(), job.NewID(time.Now()).Day())
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err == nil {
		t.Error("a job the hub could not record was dispatched anyway; it could not " +
			"afterwards be resumed, killed, or audited, and the operator was told it ran")
	} else {
		t.Logf("submission refused: %v", err)
	}

	// The other half: a bookkeeping write that fails after delivery is a
	// warning, not a failure. `deliver` still reports what it sent.
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	j := &job.Job{
		JID: job.NewID(time.Now()), Fun: "test.ping",
		Created: time.Now(), Expires: time.Now().Add(time.Hour),
		Nodes: []string{"web1.example"}, State: job.Dispatched,
	}
	if err := l.server.Jobs.Put(j); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(l.server.Jobs.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.server.Jobs.Dir(), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nothing is connected, so nothing is sent; what is being checked is
	// that a failed record does not become a failed call.
	sent := l.server.deliver(j, messageFor(j), []string{"web1.example"})
	if sent != 0 {
		t.Errorf("deliver reported %d sent to a node that is not connected", sent)
	}
	l.server.setState(j, job.Complete)
	if j.State != job.Complete {
		t.Error("a state change was abandoned because the record could not be written; " +
			"the instruction had already gone out and the hub's own view should still move")
	}
}

// A queued job whose window closed while its node was away is dropped,
// and the node is told why rather than finding that nothing happened.
//
// The clock moves rather than the test waiting: the window is an hour by
// default and a suite that waited one out would not be run. Skew is the
// hub's own — a job's expiry is an absolute time written when the job is
// created, so this is the same code path a hub whose clock jumped takes.
func TestChaosClockSkew(t *testing.T) {
	s := chaos.Exercises(chaos.ClockSkew)
	says(t, s)

	l := newLab(t).withJobs(t).withEvents(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	j, err := l.server.Jobs.Get(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if !j.IsQueuedFor("web1.example") {
		t.Fatalf("the job was not spooled: %v", j.Queued)
	}

	// The hub's clock jumps past the job's window.
	l.advance(QueuedTTL + time.Minute)

	delivered, stopWatch := l.watching(t, client, "web1.example")
	defer stopWatch()

	select {
	case jid := <-delivered:
		t.Errorf("an expired queued job was delivered anyway: %s", jid)
	case <-time.After(2 * time.Second):
	}

	// Dropped, and the spool entry cleared, so it is not reconsidered
	// on every subsequent connection for ever.
	waitFor(t, 5*time.Second, "the expired spool entry to be cleared", func() bool {
		j, err := l.server.Jobs.Get(job.ID(res.JID))
		return err == nil && !j.IsQueuedFor("web1.example")
	})

	// And said so. A node that comes back to find nothing happened, and
	// no reason why, is the failure SPEC 9.5 is explicit about.
	tag := "halite/job/" + res.JID + "/expired"
	waitFor(t, 5*time.Second, "the expiry event", func() bool {
		events, _, err := l.server.Events.Read(eventbus.Earliest, []string{tag}, 10)
		return err == nil && len(events) > 0
	})
}

// A certificate that has expired is refused, and one that expires while
// its connection is open does not tear that connection down.
//
// The second half is a property of TLS rather than a decision this build
// made — nothing re-verifies a peer mid-connection — and it is recorded
// because an operator planning a rotation needs to know which of the two
// they are relying on. The renewal has to happen before the *next*
// connection, not before the certificate's own expiry.
func TestChaosCertificateExpiryMidRun(t *testing.T) {
	s := chaos.Exercises(chaos.CertificateExpiry)
	says(t, s)

	l := newLab(t).withJobs(t)

	// An operator certificate issued by a CA whose clock is in the
	// past, so it is already expired without anything having to wait.
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	past := &pki.CA{Cert: l.ca.Cert, Key: l.ca.Key, Now: func() time.Time {
		return time.Now().Add(-48 * time.Hour)
	}}
	der, err := past.IssueOperator(key, "ed", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expired := clientWith(t, l, key, der)

	if _, err := expired.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	}); err == nil {
		t.Error("an expired certificate was accepted")
	} else {
		t.Logf("refused at the handshake: %v", err)
	}

	// A live one works, which is what makes the refusal above about
	// expiry rather than about the lab.
	op := l.operator(t, "ed")
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	}); err != nil {
		t.Fatalf("a valid certificate was refused: %v", err)
	}

	// Mid-run: a node connected with a certificate that expires while
	// it is connected keeps the stream. Modelled by connecting with a
	// short-lived certificate and letting it lapse.
	client := l.enrolled(t, "web1.example")
	stop := l.connect(t, client, "web1.example", "{}")
	defer stop()
	waitFor(t, 5*time.Second, "the node to be connected", func() bool {
		return len(l.server.fleet().Connected()) == 1
	})
	// Nothing re-checks the peer's certificate on an open connection,
	// so advancing past any expiry cannot end this stream. Asserting it
	// stays up is what records that: the renewal deadline is the next
	// connection, not the expiry.
	l.advance(365 * 24 * time.Hour)
	time.Sleep(200 * time.Millisecond)
	if len(l.server.fleet().Connected()) != 1 {
		t.Error("an established stream ended when the clock moved past a certificate's " +
			"lifetime; if that is now enforced, this scenario's behaviour needs rewriting")
	}
}

// The reactor's queue is bounded, drops the oldest, and says so three
// times over.
//
// Dropping the newest would be the other choice and is worse for what a
// reactor mostly watches: the newest event is the current state of the
// world, and the one it would discard. What matters more than which end
// is that the loss is visible — a reaction that did not happen is
// otherwise indistinguishable from an event that never arrived.
func TestChaosReactorQueueOverflow(t *testing.T) {
	s := chaos.Exercises(chaos.ReactorQueueOverflow)
	says(t, s)

	q := newBoundedQueue(3)
	for i := 0; i < 3; i++ {
		if dropped := q.push(reactorJob{event: &eventbus.Event{Tag: fmt.Sprintf("t%d", i)}}); dropped != 0 {
			t.Fatalf("a queue with room dropped %d", dropped)
		}
	}
	dropped := q.push(reactorJob{event: &eventbus.Event{Tag: "t3"}})
	if dropped != 1 {
		t.Errorf("pushing past the limit dropped %d, want 1", dropped)
	}

	// The oldest went and the newest stayed.
	var got []string
	for i := 0; i < 3; i++ {
		j, ok := q.pop(context.Background())
		if !ok {
			t.Fatalf("the queue emptied after %d", i)
		}
		got = append(got, j.event.Tag)
	}
	want := []string{"t1", "t2", "t3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the queue holds %v, want %v: it drops the oldest, not the newest", got, want)
	}

	// It never blocks and never refuses: a burst larger than the whole
	// queue is absorbed by discarding, not by making the caller wait.
	q2 := newBoundedQueue(2)
	total := 0
	for i := 0; i < 100; i++ {
		total += q2.push(reactorJob{event: &eventbus.Event{Tag: "burst"}})
	}
	if total != 98 {
		t.Errorf("a burst of 100 into a queue of 2 dropped %d, want 98", total)
	}
}

// watching subscribes an already-enrolled client and reports every job
// the hub sends it.
//
// Its own helper rather than `connectTracking`, because both of the
// scenarios here need to know that a job was delivered *again* and the
// tracker records a node once. It also takes the client rather than
// enrolling one, so a node can leave and come back as the same node --
// which is the whole of what a partition is.
func (l *lab) watching(t *testing.T, client *transport.Client, nodeID string) (<-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	jobs := make(chan string, 16)
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = client.Subscribe(ctx, transport.SubscribeRequest{NodeID: nodeID},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-ready:
					default:
						close(ready)
					}
				case transport.MsgJob:
					select {
					case jobs <- msg.JID:
					default:
					}
					// Answer it, so the hub's own bookkeeping runs the
					// same path it would for a real node.
					_ = client.Return(ctx, job.Return{
						JID: job.ID(msg.JID), NodeID: nodeID, Fun: msg.Fun,
						Success: true, Schema: job.ReturnSchema,
					})
				}
				return nil
			})
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		cancel()
		<-stopped
		t.Fatalf("%s never connected", nodeID)
	}
	return jobs, func() { cancel(); <-stopped }
}

// clientWith builds an operator client holding a certificate a test
// issued for itself, which is how an expired one gets in front of the
// hub without waiting for a real one to lapse.
func clientWith(t *testing.T, l *lab, key crypto.Signer, der []byte) *transport.Client {
	t.Helper()
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pki.EncodeCert(der), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &transport.Client{HubURL: l.url, CA: l.ca.Cert, Cert: &pair, Timeout: 5 * time.Second}
}

// waitFor polls until a condition holds, and names what it was waiting
// for when it does not.
func waitFor(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if ok() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
