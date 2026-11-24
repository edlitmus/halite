package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// A step waits for the nodes its job was sent to, not for the ones a
// stopped batch never reached (DIVERGENCE 5.734).
//
// awaitReturns waited for every node to answer or for the step's own
// timeout. When a batch stops -- its safe limit aborts it, or the job
// expires part-way and the hub stops sending what a node would refuse --
// the nodes it never reached are missing for ever, so the step sat out
// the rest of its timeout on a job the hub had already closed: an hour,
// here, before saying anything.

const abortingBatchStep = `
roll:
  salt.function:
    - name: test.ping
    - tgt: '*'
    - batch: 1
    - batch_safe_limit: 1
    - timeout: 3600
`

// firstJob is the jid of the step's job and the node its first wave went
// to.
func firstJob(t *testing.T, l *lab, step string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if run, _ := newestRun(l); run != nil {
			for _, st := range run.Steps {
				if st.ID == step && st.JID != "" {
					if j, err := l.server.Jobs.Get(job.ID(st.JID)); err == nil && len(j.Delivered) > 0 {
						return st.JID, j.Delivered[0]
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("step %s never sent its first wave", step)
	return "", ""
}

func answer(t *testing.T, client *transport.Client, jid, nodeID string, ok bool) {
	t.Helper()
	if err := client.Return(context.Background(), job.Return{
		JID: job.ID(jid), NodeID: nodeID, Fun: "test.ping", Success: ok,
		Return: json.RawMessage(`{}`), Schema: job.ReturnSchema,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAStepWhoseBatchIsAbortedStopsWaitingForTheNodesItNeverReached(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": abortingBatchStep})
	clients := map[string]*transport.Client{}
	for _, id := range []string{"a.example", "b.example"} {
		clients[id] = l.enrolled(t, id)
		defer l.connectSilent(t, clients[id], id, `{}`)()
	}

	done := make(chan *OrchRun, 1)
	go func() {
		run, err := l.server.Orchestrate(context.Background(), OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}})
		if err != nil {
			t.Error(err)
		}
		done <- run
	}()
	jid, first := firstJob(t, l, "roll")
	other := map[string]string{"a.example": "b.example", "b.example": "a.example"}[first]
	answer(t, clients[first], jid, first, false)

	var run *OrchRun
	select {
	case run = <-done:
	case <-time.After(15 * time.Second):
		j, _ := l.server.Jobs.Get(job.ID(jid))
		t.Fatalf("the step was still waiting 15s after its batch stopped (job %s, delivered %v); it would "+
			"have waited out its hour for %s, which was never sent the job", j.State, j.Delivered, other)
	}
	j, _ := l.server.Jobs.Get(job.ID(jid))
	if j.State != job.Aborted {
		t.Errorf("the job is %s, want aborted by its safe limit", j.State)
	}
	if run == nil || run.State == OrchComplete {
		t.Fatalf("a step that failed one node and never reached the other completed:\n%s", describeSteps(run))
	}
	step := stepByID(t, run, "roll")
	for _, want := range []string{"1 of 2 node(s) failed (" + first + ")",
		"were never sent the job, because the batch was aborted by its safe limit (" + other + ")"} {
		if !strings.Contains(step.Comment, want) {
			t.Errorf("the step does not say %q: %s", want, step.Comment)
		}
	}
}

// The other way a batch stops: the job expires part-way, and the hub stops
// sending waves a node would refuse. Asked of awaitReturns directly with a
// batched job of one minute.
func TestAWaitEndsWhenAnExpiredBatchStopsShortOfItsNodes(t *testing.T) {
	l := orchLab(t, nil)
	clients := map[string]*transport.Client{}
	for _, id := range []string{"a.example", "b.example"} {
		clients[id] = l.enrolled(t, id)
		defer l.connectSilent(t, clients[id], id, `{}`)()
	}
	j, err := l.server.Dispatch(Submission{Target: "*", Fun: "test.ping", BatchSpec: "1", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	jid, first := firstJob2(t, l, string(j.JID))
	l.advance(2 * time.Minute)
	answer(t, clients[first], jid, first, true)

	type waited struct {
		j       *job.Job
		missing []string
	}
	got := make(chan waited, 1)
	start := time.Now()
	go func() {
		j, _, missing, err := l.server.awaitReturns(context.Background(), job.ID(jid), time.Hour)
		if err != nil {
			t.Error(err)
		}
		got <- waited{j, missing}
	}()
	select {
	case w := <-got:
		if len(w.missing) != 1 || w.missing[0] == first {
			t.Errorf("the wait ended missing %v, want only the node never sent the job", w.missing)
		}
		t.Logf("the wait ended after %s; job %s, missing %v", time.Since(start).Round(time.Millisecond), w.j.State, w.missing)
	case <-time.After(15 * time.Second):
		cur, _ := l.server.Jobs.Get(job.ID(jid))
		t.Fatalf("the wait was still going 15s after the batch stopped at its expiry (job %s, delivered %v of %v)",
			cur.State, cur.Delivered, cur.Nodes)
	}
}

// firstJob2 waits for a job dispatched outside an orchestration to send its
// first wave.
func firstJob2(t *testing.T, l *lab, jid string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, err := l.server.Jobs.Get(job.ID(jid)); err == nil && len(j.Delivered) > 0 {
			return jid, j.Delivered[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never sent its first wave", jid)
	return "", ""
}

// What the step says about a node it is missing. A node that was sent the
// job reads as it always has; one never sent it says why, and the expiry is
// the reason when it applies, though an expired batch is closed as aborted
// as well.
func TestAStepSaysWhyANodeWasNeverSentItsJob(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	batched := func(state job.State, expires time.Time) *job.Job {
		return &job.Job{Fun: "test.ping", Nodes: []string{"a.example", "b.example"},
			Delivered: []string{"a.example"}, Batch: job.Batch{Size: 1}, State: state, Expires: expires}
	}
	for _, c := range []struct {
		what string
		j    *job.Job
		want string
	}{
		{"expired", batched(job.Aborted, now.Add(-time.Minute)),
			"test.ping: of 2 node(s), 1 were never sent the job, because the job expired before the batch reached them (b.example)."},
		{"aborted by its safe limit", batched(job.Aborted, now.Add(time.Hour)),
			"test.ping: of 2 node(s), 1 were never sent the job, because the batch was aborted by its safe limit (b.example)."},
	} {
		if got := onlyUnanswered(c.j, []string{"b.example"}, now); got != c.want {
			t.Errorf("%s:\ngot  %s\nwant %s", c.what, got, c.want)
		}
	}

	// Sent, and silent: the comment every orchestration has printed.
	sent := batched(job.Partial, now.Add(-time.Minute))
	sent.Delivered = []string{"a.example", "b.example"}
	if got, want := onlyUnanswered(sent, []string{"b.example"}, now),
		"test.ping: 1 of 2 node(s) never answered: b.example."; got != want {
		t.Errorf("a node sent the job:\ngot  %s\nwant %s", got, want)
	}
	if got, want := failedAndUnanswered(sent, []string{"a.example"}, []string{"b.example"}, now),
		"test.ping: 1 of 2 node(s) failed (a.example) and 1 never answered (b.example)."; got != want {
		t.Errorf("a node sent the job, beside a failure:\ngot  %s\nwant %s", got, want)
	}
}
