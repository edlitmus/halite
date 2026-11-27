package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// A batched step sets how long its waves wait (issue 872, DIVERGENCE 5.758).
//
// Every wave waited DefaultBatchTimeout, five minutes, whatever the step
// said, so a `batch: 1` step whose function ran longer handed on to the next
// node while the first was still working. The step had no key for it and its
// own `timeout` was not read.

func batchedStep(options ...string) string {
	s := "long:\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n    - batch: 1\n"
	for _, option := range options {
		s += "    - " + option + "\n"
	}
	return s
}

func batchedLab(t *testing.T, sls string) (*lab, map[string]*transport.Client) {
	t.Helper()
	l := orchLab(t, map[string]string{"deploy.sls": sls})
	clients := map[string]*transport.Client{}
	for _, id := range []string{"a.example", "b.example"} {
		clients[id] = l.enrolled(t, id)
		t.Cleanup(l.connectSilent(t, clients[id], id, `{}`))
	}
	return l, clients
}

func deliveredTo(t *testing.T, l *lab, jid string) []string {
	t.Helper()
	j, err := l.server.Jobs.Get(job.ID(jid))
	if err != nil {
		t.Fatal(err)
	}
	return j.Delivered
}

// finishBatched answers each wave in turn, so the run ends and the lab can
// close: a batched run left waiting holds it for the whole wave wait.
func finishBatched(t *testing.T, l *lab, clients map[string]*transport.Client, done <-chan *OrchRun, jid, first string) {
	t.Helper()
	second := map[string]string{"a.example": "b.example", "b.example": "a.example"}[first]
	answer(t, clients[first], jid, first, true)
	deadline := time.Now().Add(10 * time.Second)
	for len(deliveredTo(t, l, jid)) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the second wave was not sent after the first answered: delivered %v", deliveredTo(t, l, jid))
		}
		time.Sleep(20 * time.Millisecond)
	}
	answer(t, clients[second], jid, second, true)
	finished(t, done)
}

// Shown before the change by the first case: the second wave is not sent
// within the time the step allows, because the wave waits five minutes.
func TestABatchedStepsWaveWaitsAsLongAsItsBatchTimeout(t *testing.T) {
	l, clients := batchedLab(t, batchedStep("batch_timeout: 1s"))
	done := orchestrate(t, l)
	jid, first := firstJob(t, l, "long")
	defer finishBatched(t, l, clients, done, jid, first)

	if got := deliveredTo(t, l, jid); len(got) != 1 {
		t.Fatalf("the first wave reached %v, want one node", got)
	}
	// Short of the wait, the second wave has not been sent: neither node
	// has answered, and a wave that handed on early is the defect.
	time.Sleep(400 * time.Millisecond)
	if got := deliveredTo(t, l, jid); len(got) != 1 {
		t.Fatalf("the second wave was sent after 0.4s of a 1s wait: delivered %v", got)
	}
	deadline := time.Now().Add(6 * time.Second)
	for len(deliveredTo(t, l, jid)) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the second wave was not sent within 6s of a 1s batch_timeout: delivered %v",
				deliveredTo(t, l, jid))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The wave wait the job carries is what the step asked for, or follows the
// step's own timeout once that is longer than the default wait, and is left
// to the default when neither applies.
func TestABatchedStepsWaveWaitFollowsTheStep(t *testing.T) {
	for _, c := range []struct {
		name    string
		options []string
		timeout time.Duration
		wait    time.Duration
	}{
		{"nothing set", nil, 0, 0},
		{"a timeout longer than the default wait", []string{"timeout: 3600"}, time.Hour, 0},
		{"a timeout shorter than the default wait", []string{"timeout: 60"}, 0, 0},
		{"batch_timeout beats the step timeout", []string{"timeout: 3600", "batch_timeout: 20m"}, 20 * time.Minute, 0},
		{"batch_timeout as a bare number of seconds", []string{"batch_timeout: 90"}, 90 * time.Second, 0},
		{"batch_wait", []string{"batch_wait: 2s"}, 0, 2 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, clients := batchedLab(t, batchedStep(c.options...))
			done := orchestrate(t, l)
			jid, first := firstJob(t, l, "long")
			defer finishBatched(t, l, clients, done, jid, first)
			j, err := l.server.Jobs.Get(job.ID(jid))
			if err != nil {
				t.Fatal(err)
			}
			wantTimeout := c.timeout
			// A step's timeout is what is left of its deadline when it
			// dispatches, so a few seconds short of what was written.
			if c.timeout == time.Hour {
				if j.Batch.Timeout < time.Hour-5*time.Second || j.Batch.Timeout > time.Hour {
					t.Errorf("the waves wait %s, want about an hour", j.Batch.Timeout)
				}
			} else if j.Batch.Timeout != wantTimeout {
				t.Errorf("the waves wait %s, want %s", j.Batch.Timeout, wantTimeout)
			}
			if j.Batch.Wait != c.wait {
				t.Errorf("the pause between waves is %s, want %s", j.Batch.Wait, c.wait)
			}
		})
	}
}

// A duration the step cannot be read as is refused, and nothing is sent: a
// rolling step that asked for a pause and got none was told the opposite of
// what it will do.
func TestABatchedStepWithAnUnreadableWaveTimeIsRefused(t *testing.T) {
	for _, option := range []string{"batch_timeout: soon", "batch_wait: -5s"} {
		t.Run(option, func(t *testing.T) {
			l, _ := batchedLab(t, batchedStep(option))
			run := finished(t, orchestrate(t, l))
			if run.State == OrchComplete {
				t.Fatalf("a step with %q completed:\n%s", option, describeSteps(run))
			}
			if got := describeSteps(run); !strings.Contains(got, strings.SplitN(option, ":", 2)[0]) {
				t.Errorf("the failure does not name the option %q:\n%s", option, got)
			}
			if jobs, _ := l.server.Jobs.List(100); len(jobs) != 0 {
				t.Errorf("%d jobs were sent for a refused step", len(jobs))
			}
		})
	}
}
