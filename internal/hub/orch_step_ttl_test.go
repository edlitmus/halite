package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// An unsigned orchestration step's job lives as long as the step waits for
// it, never less than the default (issue 810, DIVERGENCE 5.735).
//
// It got the fifteen-minute default whatever the step's `timeout`. Run here
// before the change, with the hub's clock moved sixteen minutes on: a step
// with `timeout: 3600` whose node answered late succeeded, and its job read
// partial and counted expired; the same step batched one node at a time
// stopped sending at the expiry, logging "batch stopped: the job expired
// before every node was reached", and the second node was never sent it.

func stepWithTimeout(timeout, batch string) string {
	s := "long:\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n"
	if timeout != "" {
		s += "    - timeout: " + timeout + "\n"
	}
	if batch != "" {
		s += "    - batch: " + batch + "\n"
	}
	return s
}

// orchestrate starts the run and returns a channel for its outcome.
func orchestrate(t *testing.T, l *lab) <-chan *OrchRun {
	t.Helper()
	done := make(chan *OrchRun, 1)
	go func() {
		run, err := l.server.Orchestrate(context.Background(), OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}})
		if err != nil {
			t.Error(err)
		}
		done <- run
	}()
	return done
}

func finished(t *testing.T, done <-chan *OrchRun) *OrchRun {
	t.Helper()
	select {
	case run := <-done:
		return run
	case <-time.After(15 * time.Second):
		t.Fatal("the orchestration did not finish")
		return nil
	}
}

func TestAStepsJobLivesAsLongAsTheStepWaitsForIt(t *testing.T) {
	for _, c := range []struct {
		timeout string
		want    time.Duration
	}{
		{"3600", time.Hour},
		{"", job.DefaultTTL},   // no timeout: the step waits five minutes
		{"60", job.DefaultTTL}, // never shorter than the default
	} {
		t.Run(fmt.Sprintf("timeout %q", c.timeout), func(t *testing.T) {
			l := orchLab(t, map[string]string{"deploy.sls": stepWithTimeout(c.timeout, "")})
			node := l.enrolled(t, "web1.example")
			defer l.connectSilent(t, node, "web1.example", `{}`)()
			done := orchestrate(t, l)
			jid, _ := firstJob(t, l, "long")
			j, err := l.server.Jobs.Get(job.ID(jid))
			if err != nil {
				t.Fatal(err)
			}
			// The step's timeout is what is left of its deadline when it
			// dispatches, so a second either side.
			if got := j.Expires.Sub(j.Created); got < c.want-2*time.Second || got > c.want+time.Second {
				t.Errorf("the job lives %s, want %s", got.Round(time.Second), c.want)
			}
			answer(t, node, jid, "web1.example", true)
			finished(t, done)
		})
	}
}

// The step of the first probe, after the change: sixteen minutes on, the
// job has not expired, so nothing settles it; the node's answer completes
// both the job and the step, which now agree.
func TestALongStepsJobIsNotSettledExpiredWhileTheStepStillWaits(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": stepWithTimeout("3600", "")})
	node := l.enrolled(t, "web1.example")
	defer l.connectSilent(t, node, "web1.example", `{}`)()
	done := orchestrate(t, l)
	jid, _ := firstJob(t, l, "long")

	l.advance(16 * time.Minute)
	if n, err := l.server.Settle(); err != nil || n != 0 {
		t.Fatalf("sixteen minutes into a step of an hour, Settle closed %d jobs (%v)", n, err)
	}
	answer(t, node, jid, "web1.example", true)
	run := finished(t, done)
	j, _ := l.server.Jobs.Get(job.ID(jid))
	if run.State != OrchComplete || j.State != job.Complete {
		t.Errorf("run %s, job %s; want both complete:\n%s", run.State, j.State, describeSteps(run))
	}
}

// The second probe's step: batched one node at a time, the first wave
// answering sixteen minutes in. The second wave is sent, and the rollout
// finishes.
func TestABatchedStepKeepsSendingWavesPastFifteenMinutes(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": stepWithTimeout("3600", "1")})
	clients := map[string]*transport.Client{}
	for _, id := range []string{"a.example", "b.example"} {
		clients[id] = l.enrolled(t, id)
		defer l.connectSilent(t, clients[id], id, `{}`)()
	}
	done := orchestrate(t, l)
	jid, first := firstJob(t, l, "long")
	second := map[string]string{"a.example": "b.example", "b.example": "a.example"}[first]

	l.advance(16 * time.Minute)
	answer(t, clients[first], jid, first, true)
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, _ := l.server.Jobs.Get(job.ID(jid))
		if len(j.Delivered) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second wave was not sent sixteen minutes in: job %s, delivered %v", j.State, j.Delivered)
		}
		time.Sleep(20 * time.Millisecond)
	}
	answer(t, clients[second], jid, second, true)
	run := finished(t, done)
	j, _ := l.server.Jobs.Get(job.ID(jid))
	if run.State != OrchComplete || j.State != job.Complete {
		t.Errorf("run %s, job %s; want both complete:\n%s", run.State, j.State, describeSteps(run))
	}
}
