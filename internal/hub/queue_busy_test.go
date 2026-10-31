package hub

import (
	"context"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// A queued job is delivered however many other jobs have been run since.
//
// deliverQueued looked for the node's queued jobs among the 200 most recent
// jobs only, on the comment's say-so that "two hundred covers a busy hub's hour
// many times over". A job queued for an hour is gone from that window once 200
// others have been run, which a hub with a reactor or a scheduler doing one
// job every ten seconds does in under an hour: the node came back, was given
// nothing, and the job was not expired or reported either, because it was
// never looked at. DIVERGENCE 5.500.
func TestAQueuedJobIsDeliveredAfterManyOtherJobs(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	tr2 := &tracker{seen: map[string]time.Time{}}
	defer l.connectTracking(t, web2, "web2.example", tr2, nil)()
	op := l.operator(t, "ed")

	queued, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", TargetKind: "L", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 230; i++ {
		if _, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: "web2.example", TargetKind: "L", Fun: "test.ping",
		}); err != nil {
			t.Fatal(err)
		}
	}

	tr1 := &tracker{seen: map[string]time.Time{}}
	defer l.connectTracking(t, web1, "web1.example", tr1, nil)()
	deadline := time.Now().Add(5 * time.Second)
	for tr1.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if tr1.count() == 0 {
		t.Fatalf("job %s, queued for web1.example, was not delivered when it connected", queued.JID)
	}
}

// A hub that restarts finds the jobs queued before it did, however many
// others were run since: the index is filled from the whole cache, not from
// the most recent of it.
func TestTheQueueIndexIsSeededFromTheWholeCache(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	tr := &tracker{seen: map[string]time.Time{}}
	defer l.connectTracking(t, web2, "web2.example", tr, nil)()
	op := l.operator(t, "ed")

	queued, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", TargetKind: "L", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 230; i++ {
		if _, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: "web2.example", TargetKind: "L", Fun: "test.ping",
		}); err != nil {
			t.Fatal(err)
		}
	}

	var fresh queueIndex // a hub that has just started
	if err := fresh.seed(l.server.Jobs); err != nil {
		t.Fatal(err)
	}
	got := fresh.candidates("web1.example")
	if len(got) != 1 || string(got[0]) != queued.JID {
		t.Errorf("a freshly seeded index holds %v for web1.example, want %s", got, queued.JID)
	}
}
