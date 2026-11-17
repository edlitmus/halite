package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Four results.jsonl lines from the AWS runs of 5.668.1 (2026-11-16,
// archived on the build host under reactor-2026-11-16), copied as
// simfleet events wrote them. They are the runs the verdict got wrong
// and the run it got right, so the test holds the verdict to what the
// hub actually did rather than to records written to suit it.
var capturedReactorRuns = map[string]string{
	"5000/s, 2048 posters": `{"reaction":"noop","nodes":8000,"rate":5000,"seconds":60,"concurrency":2048,"offered":300000,"not_offered":0,"accepted":300000,"rate_limited":0,"failed":0,"latency":{"count":300000,"p50_seconds":0.001,"p95_seconds":0.002,"last_seconds":0.896},"accepted_per_second":5000,"slowest_second":5000,"published":300000,"reacted":300000,"reactor_dropped":0,"reactor_failures":0,"node_rate_dropped":0,"max_queue_depth":4,"drain_seconds":0.590882836,"drained":true}`,
	"5000/s, 256 posters":  `{"reaction":"noop","nodes":8000,"rate":5000,"seconds":60,"concurrency":256,"offered":298325,"not_offered":1675,"accepted":298325,"rate_limited":0,"failed":0,"latency":{"count":298325,"p50_seconds":0,"p95_seconds":0.002,"last_seconds":0.177},"accepted_per_second":4972.083333333333,"slowest_second":4112,"published":298325,"reacted":298325,"reactor_dropped":0,"reactor_failures":0,"node_rate_dropped":0,"max_queue_depth":17,"drain_seconds":0.594169204,"drained":true}`,
	"10000/s":              `{"reaction":"noop","nodes":8000,"rate":10000,"seconds":30,"concurrency":2048,"offered":292901,"not_offered":7099,"accepted":292901,"rate_limited":0,"failed":0,"latency":{"count":292901,"p50_seconds":0.001,"p95_seconds":0.013,"last_seconds":0.973},"accepted_per_second":9763.366666666667,"slowest_second":5682,"published":292901,"reacted":292901,"reactor_dropped":0,"reactor_failures":0,"node_rate_dropped":0,"max_queue_depth":112,"drain_seconds":0.727910122,"drained":true}`,
	"20000/s":              `{"reaction":"noop","nodes":8000,"rate":20000,"seconds":30,"concurrency":2048,"offered":573707,"not_offered":26293,"accepted":573707,"rate_limited":0,"failed":0,"latency":{"count":573707,"p50_seconds":0.002,"p95_seconds":0.031,"last_seconds":1.346},"accepted_per_second":19123.566666666666,"slowest_second":7305,"published":573707,"reacted":573707,"reactor_dropped":0,"reactor_failures":0,"node_rate_dropped":0,"max_queue_depth":256,"drain_seconds":0.711712028,"drained":true}`,
}

func capturedRun(t *testing.T, name string) eventsResult {
	t.Helper()
	var r eventsResult
	if err := json.Unmarshal([]byte(capturedReactorRuns[name]), &r); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

func verdictLine(r eventsResult) string {
	var b bytes.Buffer
	r.print(&b)
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.Contains(l, "SPEC 30 reactor row") {
			return l
		}
	}
	return ""
}

// Above the row's rate, events the harness could not offer do not fail
// the row; the slowest second is judged against 5,000. The 10,000 and
// 20,000 runs had slowest seconds of 5,682 and 7,305 and reacted to
// everything they accepted, so they met it, and printed NOT MET.
func TestEventsAboveTheRowAreJudgedOnTheSlowestSecond(t *testing.T) {
	for _, name := range []string{"5000/s, 2048 posters", "10000/s", "20000/s"} {
		if l := verdictLine(capturedRun(t, name)); !strings.HasSuffix(l, ": MET") {
			t.Errorf("%s: %q; want MET", name, l)
		}
	}
}

// At the row's own rate, a harness that fell behind has not measured the
// row: the 256-poster run left 1,675 events unoffered and its slowest
// second was 4,112, and it says both.
func TestEventsAtTheRowFailOnWhatWasNotOffered(t *testing.T) {
	l := verdictLine(capturedRun(t, "5000/s, 256 posters"))
	for _, want := range []string{"NOT MET", "1675 events not offered", "slowest second 4112"} {
		if !strings.Contains(l, want) {
			t.Errorf("%q; want it to contain %q", l, want)
		}
	}
}

// Above the row's rate, a slow second still fails it, and so does
// anything the hub accepted and did not react to. Each is taken from the
// 20,000 run with one thing changed, so it is the one reason given.
func TestEventsAboveTheRowStillFailOnTheHub(t *testing.T) {
	cases := map[string]struct {
		change func(*eventsResult)
		want   string
	}{
		"slow second": {func(r *eventsResult) { r.SlowestSecond = 4999 }, "slowest second 4999 accepted, under 5000"},
		"unreacted":   {func(r *eventsResult) { r.Reacted-- }, "reacted to 573706 of 573707 accepted"},
		"dropped":     {func(r *eventsResult) { r.ReactorDropped = 1 }, "1 dropped from the queue"},
		"failed":      {func(r *eventsResult) { r.ReactorFailures = 1 }, "1 reactions failed"},
		"not drained": {func(r *eventsResult) { r.Drained = false }, "the queue did not drain"},
	}
	for name, c := range cases {
		r := capturedRun(t, "20000/s")
		c.change(&r)
		if got := r.reactorRowFailures(); len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: %q; want only %q", name, got, c.want)
		}
		if l := verdictLine(r); !strings.Contains(l, "NOT MET: "+c.want) {
			t.Errorf("%s: printed %q", name, l)
		}
	}
}
