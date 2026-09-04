package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/transport"
)

// A node's beacon queue is bounded, and SPEC 16.3 has it report what it
// discarded as an event rather than losing it silently. SPEC 26.2 wants
// a number behind that, and the number is the one in the event: a hub
// counting the overflow events themselves would report one drop where
// forty happened.
func TestBeaconOverflowIsCountedByHowMuchWasLost(t *testing.T) {
	l := metricsLab(t, metricsPolicy).withEvents(t)
	node := l.enrolled(t, "web1.example")

	data, err := json.Marshal(map[string]any{"dropped": 40})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := node.SendEvent(context.Background(), transport.EventRequest{
		Tag: "beacon/diskusage/overflow", Data: data,
	}); err != nil {
		t.Fatal(err)
	}

	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `halite_beacon_dropped_total{beacon="diskusage"} 40`) {
		t.Errorf("the overflow was not counted by what it lost:\n%s",
			section(out, "halite_beacon"))
	}
	// And the event itself is still counted as an event, because it is
	// one: the counters answer different questions.
	if !strings.Contains(out, `halite_beacon_events_total{beacon="diskusage"} 1`) {
		t.Errorf("the overflow event was not counted as an event:\n%s",
			section(out, "halite_beacon"))
	}
}

// An overflow event with no count is still a loss. Counting it as zero
// would report a queue that overflowed as one that did not.
func TestAnOverflowWithNoCountIsStillOne(t *testing.T) {
	l := metricsLab(t, metricsPolicy).withEvents(t)
	node := l.enrolled(t, "web1.example")

	if _, err := node.SendEvent(context.Background(), transport.EventRequest{
		Tag: "beacon/inotify/overflow",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `halite_beacon_dropped_total{beacon="inotify"} 1`) {
		t.Errorf("an overflow with no count was not counted:\n%s",
			section(out, "halite_beacon_dropped"))
	}
}

// An ordinary beacon event is not a drop. The two share a tag prefix,
// and a check on the prefix alone would count every reading a beacon
// ever took as a loss.
func TestAnOrdinaryBeaconEventIsNotADrop(t *testing.T) {
	l := metricsLab(t, metricsPolicy).withEvents(t)
	node := l.enrolled(t, "web1.example")

	if _, err := node.SendEvent(context.Background(), transport.EventRequest{
		Tag: "beacon/diskusage/used",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `halite_beacon_dropped_total{beacon="diskusage"}`) {
		t.Errorf("a reading was counted as a drop:\n%s", section(out, "halite_beacon"))
	}
}

// The hub's own service metrics. Without them "the API is slow" and
// "the hub the API waits on is slow" are the same graph.
func TestTheHubTimesItsOwnRequests(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	l.enrolled(t, "web1.example")

	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`halite_hub_requests_total{route="/v1/enroll",code="200"}`,
		`halite_hub_request_duration_seconds_count{route="/v1/enroll"}`,
		`halite_hub_enrollments_total{result="issued"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the exposition is missing %q:\n%s", want,
				section(out, "halite_hub_request")+"\n"+section(out, "halite_hub_enrollments"))
		}
	}
}

// A route label carries the route and not the identifier in it. One
// series per job or per file served is the unbounded label the
// registry's own cap exists to survive.
func TestTheRouteLabelDropsTheIdentifier(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"/v1/health", "/v1/health"},
		{"/v1/jobs", "/v1/jobs"},
		{"/v1/jobs/20260904T101010101010", "/v1/jobs/{jid}"},
		{"/v1/jobs/20260904T101010101010/kill", "/v1/jobs/{jid}/kill"},
		{"/v1/files/base/web/nginx.conf", "/v1/files/{path}"},
		{"/v1/files/", "/v1/files/"},
	} {
		if got := hubRoute(c.path); got != c.want {
			t.Errorf("hubRoute(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// An estate discovers certificate expiry all at once, when a batch
// issued on one afternoon a year ago stops authenticating. keys_accepted
// does not fall until the record is removed, so it says nothing.
func TestExpiringCertificatesAreCountedApartFromExpiredOnes(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	records := []*keystore.Record{
		{NodeID: "fresh", State: keystore.Accepted, NotAfter: now.Add(200 * 24 * time.Hour)},
		{NodeID: "soon", State: keystore.Accepted, NotAfter: now.Add(3 * 24 * time.Hour)},
		{NodeID: "gone", State: keystore.Accepted, NotAfter: now.Add(-time.Hour)},
		{NodeID: "waiting", State: keystore.Pending, NotAfter: now.Add(time.Hour)},
		{NodeID: "undated", State: keystore.Accepted},
	}

	if got := countExpiring(records, now, 0); got != 1 {
		t.Errorf("expired = %d, want 1", got)
	}
	if got := countExpiring(records, now, 30*24*time.Hour); got != 1 {
		t.Errorf("expiring within thirty days = %d, want 1; "+
			"the one that has already gone is counted by the other gauge", got)
	}
	// The one already gone is the soonest, and it reads as negative
	// rather than as zero: zero is what "no certificate at all" says,
	// and an alert cannot tell those apart if both floor at it.
	if got := soonestExpiry(records, now); got != -3600 {
		t.Errorf("soonest = %v seconds, want -3600", got)
	}
	if got := soonestExpiry(nil, now); got != 0 {
		t.Errorf("with no records soonest = %v, want 0", got)
	}
}

// Every response now goes through a counting writer so the hub can time
// its own requests, and the file server's already went through one. Two
// wrappers deep, the bytes still have to add up and the request still
// has to be counted under its route.
//
// The byte total is right either way — the fallback path counts through
// Write. What the wrapper decides is whether http.ServeContent can take
// the sendfile path at all, which the check below this one is about.
func TestTheFileServerStillCountsEveryByteItSends(t *testing.T) {
	body := strings.Repeat("nginx config line\n", 500)
	l := metricsLab(t, metricsPolicy).withFiles(t, map[string]string{"web/nginx.conf": body})
	node := l.enrolled(t, "web1.example")

	got, _, _, err := node.FetchFile(context.Background(), "base", "web/nginx.conf", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("the file came back %d bytes, want %d", len(got), len(body))
	}

	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	want := "halite_fileserver_bytes_total " + strconv.Itoa(len(body))
	if !strings.Contains(out, want) {
		t.Errorf("the byte total is not %d:\n%s", len(body), section(out, "halite_fileserver"))
	}
	// And the outer wrapper counted the same request as a request.
	if !strings.Contains(out, `halite_hub_requests_total{route="/v1/files/{path}",code="200"} 1`) {
		t.Errorf("the fetch was not counted by route:\n%s", section(out, "halite_hub_requests_total"))
	}
}

// http.ServeContent sends a file with sendfile when its writer offers
// ReadFrom and copies it through userspace when it does not. The
// wrapper has to offer it or every file the estate fetches becomes a
// copy — and the wrapper is on every response now, not just this one.
//
// Nothing observable changes when it is missing, which is why this is
// checked here: the file arrives, the bytes add up, and the only
// difference is a syscall nobody counts.
func TestTheCountingWriterDoesNotHideSendfile(t *testing.T) {
	var w any = &countingWriter{}
	if _, ok := w.(io.ReaderFrom); !ok {
		t.Error("countingWriter does not offer ReadFrom, so http.ServeContent " +
			"copies every file through userspace instead of sending it")
	}
	if _, ok := w.(http.Flusher); !ok {
		t.Error("countingWriter does not offer Flush, so a streaming handler " +
			"behind it buffers")
	}
}
