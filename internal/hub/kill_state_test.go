package hub

import (
	"context"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// Killing a job that has finished must not unfinish it.
//
// `kill` set the record to Aborted whatever it held, and expired it, so an
// operator who killed a job that had already completed (because the job list
// was a minute old, say) turned a successful run into an aborted one, and
// every node was sent a kill for work that was over. Complete is the one
// state with nothing left to stop.
func TestKillingACompletedJobLeavesItComplete(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	quiet := l.enrolled(t, "quiet.example")
	stop := l.connectSilent(t, quiet, "quiet.example", `{}`)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	lateReturn(t, quiet, res.JID, "quiet.example")
	waitForState(t, op, res.JID, string(job.Complete))

	if killed, err := op.KillJob(context.Background(), res.JID); err == nil {
		t.Errorf("a completed job was killed: %+v", killed)
	}
	st, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != string(job.Complete) {
		t.Errorf("the job's state is %q after a refused kill, want it left %q", st.State, job.Complete)
	}
}

// A kill is not undone by the answer of a node that was already running the
// job. completeIfDone marked the record Complete whenever nothing was
// missing, which a late return from the last node makes true, so a job the
// operator had stopped read as having completed.
func TestAKilledJobStaysAbortedWhenAWorkingNodeAnswersLate(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	quiet := l.enrolled(t, "quiet.example")
	stop := l.connectSilent(t, quiet, "quiet.example", `{}`)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.KillJob(context.Background(), res.JID); err != nil {
		t.Fatal(err)
	}
	lateReturn(t, quiet, res.JID, "quiet.example")
	waitForReturns(t, op, res.JID, 1)

	st, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != string(job.Aborted) {
		t.Errorf("a killed job reads %q after a late return, want %q", st.State, job.Aborted)
	}
}
