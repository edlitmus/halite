package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The record tests' reads say what stood in the way of a record, rather
// than returning nil and polling again (DIVERGENCE 5.733). An unreadable
// record stands in here for one a replace is holding on Windows, which
// this host cannot produce: both come back from List as unreadable, and
// the point is that the account names it.
func TestTheRecordTestsSayWhatStoodInTheWayOfARecord(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": threeStepDeploy})

	if run, problem := newestRun(l); run != nil || problem != "no record yet" {
		t.Errorf("an empty store: %v, %q", run, problem)
	}

	const jid = "20261124T000000000001"
	bad := filepath.Join(l.server.orchStore().dir, jid+".json")
	if err := os.WriteFile(bad, []byte("{not a record"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, problem := newestRun(l)
	if run != nil || !strings.Contains(problem, "record "+jid+" unreadable") {
		t.Errorf("an unreadable newest record: %v, %q; want it named", run, problem)
	}

	run, account := recordNamingWithin(l, "migrate", "a-jid", 200*time.Millisecond)
	if run != nil {
		t.Errorf("recordNaming returned a run from a store with none readable: %+v", run)
	}
	for _, want := range []string{"migrate was never named as a-jid", "record " + jid + " unreadable", "from +"} {
		if !strings.Contains(account, want) {
			t.Errorf("the account does not say %q: %s", want, account)
		}
	}
}

// A failed run's steps print as steps, with the comment that says why,
// where `%+v` of the slice printed pointers.
func TestAFailedRunsStepsPrintAsSteps(t *testing.T) {
	ok, failed := true, false
	run := &OrchRun{Steps: []*OrchStep{
		{ID: "drain", Fun: "lb.drain", Result: &ok, JID: "j1", DurationMS: 12},
		{ID: "migrate", Fun: "test.echo", Result: &failed, JID: "j2", Missing: []string{"web1.example"},
			Comment: "the job did not return within its timeout"},
	}}
	got := describeSteps(run)
	for _, want := range []string{
		"drain (lb.drain): result true",
		`migrate (test.echo): result false, pending false, job "j2"`,
		"missing [web1.example]",
		": the job did not return within its timeout",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the steps do not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "0x") {
		t.Errorf("the steps print a pointer:\n%s", got)
	}
}
