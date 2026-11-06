package main

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
)

// What a node does with a job that has expired before it arrives (issue
// 540).
//
// A batch used to deliver its later slices to an expired job: with no
// `--batch-timeout` it waited the job's whole lifetime per slice. Reading
// the code said the node refuses; this runs the node's own path for a job
// off the stream and finds out. It refuses, does not run anything, and
// sends the hub a return that failed and says why -- which is a failed
// return as far as the hub's safe limit is concerned. So a slice sent to
// an expired job changed nothing on its nodes and counted each of them
// as broken. The hub no longer sends one (runBatches), and this test is
// what that comment relies on.
func TestANodeRefusesAJobThatHasExpired(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {
		t.Error("an expired job ran")
	})

	msg := jobMessage(t, "20260923T070000000540")
	msg.Expires = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	n.acceptJob(msg)

	if got := n.executor.Depth(); got != 0 {
		t.Fatalf("an expired job was queued: depth %d", got)
	}
	var ret *job.Return
	select {
	case ret = <-n.refusals:
	default:
		t.Fatal("the node refused the job and sent no return; the hub would wait on it")
	}
	if ret.Success || ret.RetCode == 0 {
		t.Errorf("the refusal reads as a success: success=%v retcode=%d", ret.Success, ret.RetCode)
	}
	if !strings.Contains(string(ret.Return), job.ReasonExpired) {
		t.Errorf("the refusal does not say the job expired: %s", ret.Return)
	}
}
