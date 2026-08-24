package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/transport"
)

func (l *lab) withEvents(t *testing.T) *lab {
	t.Helper()
	bus, err := eventbus.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Close() })
	l.server.Events = bus
	return l
}

// read pulls what is on the bus, without following.
func readEvents(t *testing.T, op *transport.Client, tags []string) []eventbus.Event {
	t.Helper()
	var out []eventbus.Event
	err := op.FollowEvents(context.Background(), tags, eventbus.Earliest, false, 500,
		func(raw json.RawMessage) error {
			var e eventbus.Event
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			out = append(out, e)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The tags of SPEC 17.1 that this build fires, fired by the things that
// actually happen rather than by a test calling emit.
func TestTheHubRecordsWhatHappens(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	stop, _ := l.fleetOf(t, 2, nil)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}
	waitForReturns(t, op, res.JID, 2)

	events := readEvents(t, op, nil)
	byTag := map[string]int{}
	for _, e := range events {
		byTag[e.Tag]++
		if e.Schema != eventbus.Schema {
			t.Errorf("%s carries schema %q", e.Tag, e.Schema)
		}
		if e.Stamp.IsZero() {
			t.Errorf("%s carries no stamp", e.Tag)
		}
	}

	want := []string{
		"halite/node/web1.example/enroll/pending",
		"halite/node/web1.example/enroll/accepted",
		"halite/node/web1.example/start",
		"halite/presence/change",
		"halite/job/" + res.JID + "/new",
		"halite/job/" + res.JID + "/ret/web1.example",
		"halite/job/" + res.JID + "/ret/web2.example",
	}
	for _, tag := range want {
		if byTag[tag] == 0 {
			t.Errorf("nothing fired %s; the bus holds %v", tag, tagsOf(events))
		}
	}

	// The job event carries what a reactor needs to act on it.
	for _, e := range events {
		if e.Tag != "halite/job/"+res.JID+"/new" {
			continue
		}
		if e.Data["fun"] != "test.ping" {
			t.Errorf("the job event says fun=%v", e.Data["fun"])
		}
		if e.Data["tgt"] != "*" {
			t.Errorf("the job event says tgt=%v", e.Data["tgt"])
		}
	}
}

// A node that could write any tag could fire the event a reactor
// watches for, which in Salt means fleet-wide execution.
func TestANodeCannotForgeAnotherTag(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	node := l.enrolled(t, "web1.example")

	for _, asked := range []string{
		"deploy/finished",
		"halite/job/20260823T010101010101/ret/web2.example",
		"/halite/key/web2.example/accept",
		"halite/presence/change",
	} {
		res, err := node.SendEvent(context.Background(), transport.EventRequest{Tag: asked})
		if err != nil {
			t.Fatalf("%q: %v", asked, err)
		}
		if !strings.HasPrefix(res.Tag, "halite/node/web1.example/") {
			t.Errorf("%q landed at %q, outside this node's namespace", asked, res.Tag)
		}
	}

	op := l.operator(t, "ed")
	// Nothing the node sent is outside its own prefix.
	for _, e := range readEvents(t, op, nil) {
		if strings.HasPrefix(e.Tag, "halite/job/") || e.Tag == "halite/presence/change" {
			if e.Node == "web1.example" && e.Data != nil {
				t.Errorf("a node's event landed at %q", e.Tag)
			}
		}
	}
	// A tag that is not usable is refused rather than sanitised into
	// something else.
	if _, err := node.SendEvent(context.Background(), transport.EventRequest{Tag: ""}); err == nil {
		t.Error("an empty tag was accepted")
	}
	if _, err := node.SendEvent(context.Background(), transport.EventRequest{Tag: "a/../../b"}); err == nil {
		t.Error("a traversal was accepted as a tag")
	}
}

// The property Salt's bus does not have.
func TestAFollowerResumesFromWhereItStopped(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	first := readEvents(t, op, nil)
	if len(first) == 0 {
		t.Fatal("enrolling a node fired nothing")
	}
	from := first[len(first)-1].Offset
	if from == "" {
		t.Fatal("a record read back carries no offset to resume from")
	}

	l.enrolled(t, "web2.example")

	var after []eventbus.Event
	err := op.FollowEvents(context.Background(), nil, from, false, 100,
		func(raw json.RawMessage) error {
			var e eventbus.Event
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			after = append(after, e)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 {
		t.Fatal("resuming from an offset returned nothing after two more events")
	}
	for _, e := range after {
		if strings.Contains(e.Tag, "web1.example") {
			t.Errorf("resuming replayed %s, which was before the offset", e.Tag)
		}
	}
}

// Following blocks on the bus rather than polling, and delivers what
// arrives next.
func TestFollowingDeliversWhatHappensNext(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	op := l.operator(t, "ed")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan string, 8)
	go func() {
		op.FollowEvents(ctx, []string{"halite/node/**"}, eventbus.Latest, true, 100,
			func(raw json.RawMessage) error {
				var e eventbus.Event
				if err := json.Unmarshal(raw, &e); err != nil {
					return err
				}
				select {
				case got <- e.Tag:
				default:
				}
				return nil
			})
	}()
	time.Sleep(100 * time.Millisecond)
	l.enrolled(t, "web1.example")

	select {
	case tag := <-got:
		if !strings.HasPrefix(tag, "halite/node/") {
			t.Errorf("the follower was given %q, outside its glob", tag)
		}
	case <-ctx.Done():
		t.Fatal("a follower saw nothing after a node enrolled")
	}
}

// SPEC 17.1's transition setting: a consumer that cannot be changed at
// the same time as the estate.
func TestSaltTagCompatibility(t *testing.T) {
	cases := map[string]string{
		"halite/job/20260823T01/new":       "salt/job/20260823T01/new",
		"halite/job/20260823T01/ret/web1":  "salt/job/20260823T01/ret/web1",
		"halite/node/web1/start":           "salt/minion/web1/start", // lexicon:allow
		"halite/state/20260823T01/web1/ok": "salt/state_result/20260823T01/ok/web1",
		"halite/presence/change":           "salt/presence/change",
		"something/else":                   "",
	}
	for in, want := range cases {
		if got := eventbus.SaltTag(in); got != want {
			t.Errorf("SaltTag(%q) = %q, want %q", in, got, want)
		}
	}

	l := newLab(t).withJobs(t).withEvents(t)
	l.server.EventTagCompat = true
	l.enrolled(t, "web1.example")

	op := l.operator(t, "ed")
	salted := readEvents(t, op, []string{"salt/**"})
	if len(salted) == 0 {
		t.Error("event_tag_compat fired no salt/ events")
	}
	// And with it off, nothing under salt/.
	plain := newLab(t).withJobs(t).withEvents(t)
	plain.enrolled(t, "web1.example")
	if got := readEvents(t, plain.operator(t, "ed"), []string{"salt/**"}); len(got) != 0 {
		t.Errorf("%d salt/ events with the setting off", len(got))
	}
}

func tagsOf(events []eventbus.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Tag)
	}
	return out
}

// SPEC 17.1 puts a beacon event under `halite/beacon/<node_id>/`, and
// SPEC 18.1's own reactor example matches on that tag. They arrived
// under `halite/node/<node_id>/` instead, so a reactor written from the
// specification matched nothing and said nothing about it.
func TestABeaconEventLandsUnderTheBeaconNamespace(t *testing.T) {
	cases := []struct{ sent, want string }{
		{"beacon/diskusage/var", "halite/beacon/web1.example/diskusage/var"},
		{"beacon/diskusage/overflow", "halite/beacon/web1.example/diskusage/overflow"},
		// The node's id is added here and never read from the request,
		// so a node that spells the whole tag out still lands under
		// its own: what it sent is the beacon name, not the node.
		{"halite/beacon/db1.example/x", "halite/beacon/web1.example/db1.example/x"},
		// Everything that is not a beacon keeps the node namespace.
		{"diskusage/var", "halite/node/web1.example/diskusage/var"},
		{"deploy/finished", "halite/node/web1.example/deploy/finished"},
		// `beacon/` alone names no beacon, so it is not the
		// namespace's root but an ordinary node event.
		{"beacon/", "halite/node/web1.example/beacon/"},
	}
	for _, c := range cases {
		got, err := nodeEventTag("web1.example", c.sent)
		if err != nil {
			t.Errorf("%q: %v", c.sent, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q became %q, want %q", c.sent, got, c.want)
		}
	}

	// Traversal is refused rather than resolved, by the bus's own tag
	// check, so neither namespace can be walked out of.
	if _, err := nodeEventTag("web1.example", "beacon/../db1.example/x"); err == nil {
		t.Error("a beacon tag walked out of its namespace")
	}
}
