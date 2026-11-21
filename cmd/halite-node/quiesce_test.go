package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/schedule"
)

// quiescedNode is a node built from configuration text, with a queue
// nothing drains, so that depth says whether a job was admitted.
func quiescedNode(t *testing.T, extra string) *node {
	t.Helper()
	n := nodeForEvidence(t, extra)
	n.refusals = make(chan *job.Return, 8)
	n.executor = newExecutor(n, 8, func(*job.Return) {
		t.Error("a job ran; nothing in these tests drains the queue")
	})
	return n
}

// offer sends one job off the stream, the path a job dispatched live, a
// job the hub queued for a node that was away, and a job resent after a
// reconnect all take, and says whether it was queued and what came back.
func offer(t *testing.T, n *node, jid, fun string) (queued bool, refusal *job.Return) {
	t.Helper()
	before := n.executor.Depth()
	msg := jobMessage(t, jid)
	msg.Fun = fun
	n.acceptJob(msg)
	select {
	case refusal = <-n.refusals:
	default:
	}
	return n.executor.Depth() > before, refusal
}

// Issue 763: `quiesce` was accepted, warned about as inert, and changed
// nothing, so a machine an operator had taken out of automation went on
// running whatever it was sent.
func TestAQuiescedNodeRunsOnlyItsAllowlist(t *testing.T) {
	cases := []struct {
		name       string
		config     string
		fun        string
		wantQueued bool
	}{
		{"an allowlisted function is run", "quiesce: true\nquiesce_allowlist: [test.ping, grains.items]\n", "test.ping", true},
		{"a function off the allowlist is refused", "quiesce: true\nquiesce_allowlist: [test.ping, grains.items]\n", "state.apply", false},
		{"the match is exact, not by prefix", "quiesce: true\nquiesce_allowlist: [test.ping]\n", "test.ping_extra", false},
		{"an empty allowlist refuses everything", "quiesce: true\n", "test.ping", false},
		{"a node that is not quiesced runs anything", "quiesce: false\nquiesce_allowlist: [test.ping]\n", "state.apply", true},
		{"the default is not quiesced", "", "state.apply", true},
		{"a value that is neither true nor false quiesces", "quiesce: maybe\nquiesce_allowlist: [test.ping]\n", "state.apply", false},
		{"a number quiesces too, since 1 is not read as true", "quiesce: 1\n", "state.apply", false},
		{"a quoted false is read as false", "quiesce: \"false\"\n", "state.apply", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := quiescedNode(t, tc.config)

			queued, refusal := offer(t, n, "20261121T10000000000"+string(rune('0'+i)), tc.fun)

			if queued != tc.wantQueued {
				t.Fatalf("queued=%v, want %v", queued, tc.wantQueued)
			}
			if tc.wantQueued {
				if refusal != nil {
					t.Errorf("an admitted job was also refused: %s", refusal.Return)
				}
				return
			}
			if refusal == nil {
				t.Fatal("the job was refused and no return was sent; the operator would wait for ever")
			}
			if refusal.Success || refusal.RetCode == 0 {
				t.Errorf("the refusal reads as a success: success=%v retcode=%d", refusal.Success, refusal.RetCode)
			}
			if !strings.Contains(string(refusal.Return), job.ReasonQuiesced) {
				t.Errorf("the refusal does not say the node is quiesced: %s", refusal.Return)
			}
		})
	}
}

// A quiesced node's own record says it refused the job, not that it
// accepted it: the quiesce check is before the acceptance record.
func TestAQuiescedRefusalIsRecordedAsARefusal(t *testing.T) {
	n := quiescedNode(t, "quiesce: true\nquiesce_allowlist: [test.ping]\n")

	offer(t, n, "20261121T100000000100", "state.apply")

	recs := wantRecords(t, n, 1)
	if recs[0].Kind != nodeevidence.KindJobRefused {
		t.Fatalf("the record is %q, want %q", recs[0].Kind, nodeevidence.KindJobRefused)
	}
	if !strings.Contains(recs[0].Detail["refused"], job.ReasonQuiesced) {
		t.Errorf("the record's reason does not name quiesce: %q", recs[0].Detail["refused"])
	}
}

// A job refused while quiesced stays refused once the node is lifted.
//
// Otherwise a hub could hold the job, wait for the operator to lift the
// node, and deliver it then as though it were new -- a job the operator
// was told had been refused would run anyway. The configuration is
// swapped under the same executor, which is the replay cache a restart
// would empty; what a restart does to an unsigned job is SPEC 6.3's
// accepted limit, and is not what this tests.
func TestAJobRefusedWhileQuiescedIsNotRunOnceLifted(t *testing.T) {
	n := quiescedNode(t, "quiesce: true\n")
	msg := jobMessage(t, "20261121T100000000200")
	msg.Fun = "state.apply"
	n.acceptJob(msg)
	select {
	case <-n.refusals:
	default:
		t.Fatal("the quiesced node did not refuse the job, so there is nothing to redeliver")
	}

	n.cfg = nodeForEvidence(t, "").cfg
	n.acceptJob(msg)

	if got := n.executor.Depth(); got != 0 {
		t.Fatalf("the refused job was queued once the node was lifted: depth %d", got)
	}
	select {
	case ret := <-n.refusals:
		if !strings.Contains(string(ret.Return), job.ReasonReplayed) {
			t.Errorf("the redelivery was refused, but not as a replay: %s", ret.Return)
		}
	default:
		t.Fatal("the redelivery was neither queued nor refused")
	}
}

// Salt refuses a scheduled job in blackout too (utils/schedule.py at
// v3006.9), and a quiesced machine that still applies its own highstate
// every hour has not been taken out of automation.
func TestAQuiescedNodeDoesNotRunItsSchedule(t *testing.T) {
	n := quiescedNode(t, "quiesce: true\nquiesce_allowlist: [test.ping]\n")
	run := schedule.Run{Job: &schedule.Job{Name: "grains", Function: "grains.items"}, Fire: time.Now()}

	err := n.runScheduled(context.Background(), run)

	var refusal *job.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != job.ReasonQuiesced {
		t.Fatalf("the scheduled job was not refused as quiesced: %v", err)
	}
	recs := wantRecords(t, n, 1)
	if recs[0].Kind != nodeevidence.KindJobRefused {
		t.Errorf("the record is %q, want %q", recs[0].Kind, nodeevidence.KindJobRefused)
	}
}

// The allowlist applies to a schedule as it does to the hub: a scheduled
// function on it still runs.
func TestAQuiescedNodeRunsAnAllowlistedScheduledJob(t *testing.T) {
	n := quiescedNode(t, "quiesce: true\nquiesce_allowlist: [test.ping]\n")
	run := schedule.Run{Job: &schedule.Job{Name: "ping", Function: "test.ping"}, Fire: time.Now()}

	if err := n.runScheduled(context.Background(), run); err != nil {
		t.Fatalf("an allowlisted scheduled job did not run: %v", err)
	}
}
