package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// A job for a node behind a relay goes down the relay's stream, naming
// the node it is for.
func TestAJobForARelayedNodeGoesDownTheRelaysStream(t *testing.T) {
	f := newFleet()
	relay := f.attach("relay1.example", nowForTest())
	f.Relay("relay1.example", []string{"web1.example", "web2.example"})

	if !f.Send("web1.example", transport.Message{T: transport.MsgJob, JID: "20260826T150532427456"}) {
		t.Fatal("a job for a relayed node was not delivered")
	}
	select {
	case msg := <-relay.out:
		if msg.Node != "web1.example" {
			t.Errorf("the relay was not told which node: %+v", msg)
		}
		if msg.JID != "20260826T150532427456" {
			t.Errorf("the job is %+v", msg)
		}
	default:
		t.Fatal("nothing reached the relay's stream")
	}

	// And a node behind no relay is still not reachable.
	if f.Send("db1.example", transport.Message{T: transport.MsgJob}) {
		t.Error("a job for an unknown node reported delivery")
	}
}

// A relayed node is connected in the sense that matters: a job for it
// will be delivered.
func TestARelayedNodeCountsAsConnected(t *testing.T) {
	f := newFleet()
	f.attach("relay1.example", nowForTest())
	f.Relay("relay1.example", []string{"web1.example"})

	connected := f.Connected()
	if _, ok := connected["web1.example"]; !ok {
		t.Errorf("a relayed node is not connected: %v", keysOf(connected))
	}
	if _, ok := connected["relay1.example"]; !ok {
		t.Error("the relay itself is not connected")
	}
}

// A subordinate that has gone must stop being dispatched to, or the
// hub reports it unresponsive rather than absent.
func TestAnUpdateReplacesTheSubordinateSet(t *testing.T) {
	f := newFleet()
	f.attach("relay1.example", nowForTest())
	f.Relay("relay1.example", []string{"web1.example", "web2.example"})
	f.Relay("relay1.example", []string{"web1.example"})

	if _, ok := f.RelayFor("web2.example"); ok {
		t.Error("a departed subordinate is still routed to")
	}
	if via, ok := f.RelayFor("web1.example"); !ok || via != "relay1.example" {
		t.Errorf("web1 routes via %q %v", via, ok)
	}
}

// Otherwise a relay could take over delivery for any node in the estate
// by naming it, which is a job going somewhere the operator did not
// intend.
func TestARelayCannotClaimADirectlyConnectedNode(t *testing.T) {
	f := newFleet()
	direct := f.attach("web1.example", nowForTest())
	f.attach("relay1.example", nowForTest())
	f.Relay("relay1.example", []string{"web1.example"})

	if via, ok := f.RelayFor("web1.example"); ok {
		t.Errorf("a relay claimed a directly connected node, via %q", via)
	}
	if !f.Send("web1.example", transport.Message{T: transport.MsgJob}) {
		t.Fatal("the direct node became unreachable")
	}
	select {
	case <-direct.out:
	default:
		t.Error("the job did not go to the direct stream")
	}
}

// A relay that goes away takes its routes with it.
func TestDroppingARelayForgetsItsSubordinates(t *testing.T) {
	f := newFleet()
	f.attach("relay1.example", nowForTest())
	f.Relay("relay1.example", []string{"web1.example"})
	f.dropRelay("relay1.example")

	if _, ok := f.RelayFor("web1.example"); ok {
		t.Error("a departed relay's routes survived it")
	}
}

// A relay may file returns for the nodes it proxies for and for nobody
// else. Without that, a relay could file a return for any node in the
// estate — a job that looks like it succeeded on a machine it never
// reached.
func TestARelayMayOnlyReturnForItsOwnSubordinates(t *testing.T) {
	s := &Server{}
	s.fleet().attach("relay1.example", nowForTest())
	s.fleet().Relay("relay1.example", []string{"web1.example"})

	if !s.relayMayReturn("relay1.example", "web1.example") {
		t.Error("a relay cannot return for its own subordinate")
	}
	if !s.relayMayReturn("web1.example", "web1.example") {
		t.Error("a node cannot return for itself")
	}
	if s.relayMayReturn("relay1.example", "db1.example") {
		t.Error("a relay returned for a node it does not proxy for")
	}
	if s.relayMayReturn("relay2.example", "web1.example") {
		t.Error("one relay returned for another's subordinate")
	}
}

// SPEC 5.3 caps depth because unbounded nesting is how syndic estates
// become undebuggable.
func TestRelayDepthIsCapped(t *testing.T) {
	s := &Server{AcceptRelays: true, Policy: allowEverything(t)}
	err := s.acceptRelay("relay1.example", transport.SubscribeRequest{
		Relay: true, Depth: transport.MaxRelayDepth,
	})
	if err == nil {
		t.Fatal("a relay past the depth limit was accepted")
	}
	if !strings.Contains(err.Error(), "deep") {
		t.Errorf("the refusal is %v", err)
	}
}

// An estate that has not decided to run relays should not acquire one
// because somebody set a flag on a hub in a branch office.
func TestARelayIsRefusedUnlessTheHubAcceptsThem(t *testing.T) {
	s := &Server{Policy: allowEverything(t)}
	err := s.acceptRelay("relay1.example", transport.SubscribeRequest{Relay: true})
	if err == nil {
		t.Fatal("a relay connected to a hub that does not accept them")
	}
	if !strings.Contains(err.Error(), "accept_relays") {
		t.Errorf("the refusal does not say what to set: %v", err)
	}
}

// A relay claiming itself as its own subordinate would route its jobs
// to itself for ever.
func TestARelayCannotNameItselfASubordinate(t *testing.T) {
	s := &Server{AcceptRelays: true, Policy: allowEverything(t)}
	err := s.acceptRelay("relay1.example", transport.SubscribeRequest{
		Relay:        true,
		Subordinates: []transport.Subordinate{{NodeID: "relay1.example"}},
	})
	if err == nil {
		t.Fatal("a relay named itself")
	}
	if !strings.Contains(err.Error(), "itself") {
		t.Errorf("the refusal is %v", err)
	}
}

// The id becomes a cache path and a tag segment.
func TestAnUnusableSubordinateIdIsRefused(t *testing.T) {
	s := &Server{AcceptRelays: true, Policy: allowEverything(t)}
	for _, id := range []string{"", "../escape", "with space", "a/b"} {
		err := s.acceptRelay("relay1.example", transport.SubscribeRequest{
			Relay: true, Subordinates: []transport.Subordinate{{NodeID: id}},
		})
		if err == nil {
			t.Errorf("%q was accepted as a subordinate id", id)
		}
	}
}

func keysOf(m map[string]time.Time) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func nowForTest() time.Time {
	return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
}

// allowEverything is a policy that permits any principal anything, so
// these tests exercise the relay checks rather than the RBAC ones.
func allowEverything(t *testing.T) *policy.Policy {
	t.Helper()
	loaded, _, err := policy.Load([]byte(`
roles:
  everything:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'node:relay1.example'
    roles: ['everything']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// A relayed node has no key on the upstream and never will: the relay
// issued it. Resolving targets against the keystore alone left every
// relayed node untargetable while Connected reported it as up, so a job
// aimed at one matched nothing and read as if the node were absent.
func TestARelayedNodeIsTargetableWithoutAKeyOnThisHub(t *testing.T) {
	store, err := keystore.Open(t.TempDir() + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(&keystore.Record{
		NodeID: "db1.example", State: keystore.Accepted,
		Fingerprint: "aa", NotAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{Authority: &keystore.Authority{Store: store}}
	s.fleet().attach("relay1.example", nowForTest())
	s.fleet().Relay("relay1.example", []string{"web1.example"})

	ids, err := s.targetableNodes()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found["web1.example"] {
		t.Errorf("a relayed node is not targetable: %v", ids)
	}
	if !found["db1.example"] {
		t.Errorf("a directly accepted node stopped being targetable: %v", ids)
	}
}

// A return is attributed to the node that ran the job, not to the
// certificate it arrived on.
//
// Behind a relay the two differ, and using the certificate tagged every
// relayed return halite/job/<jid>/ret/relay1.example. A reactor
// watching for its own node never fired, and the whole estate behind a
// relay looked like one machine.
func TestARelayedReturnIsTaggedWithTheNodeThatRanIt(t *testing.T) {
	dir := t.TempDir()
	bus, err := eventbus.Open(dir + "/events")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := job.OpenCache(dir + "/jobs")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Put(&job.Job{
		JID: "20260826T150532427456", Fun: "test.ping", Nodes: []string{"web1.example"},
		Expires: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{Events: bus, Jobs: jobs}
	s.fleet().attach("relay1.example", nowForTest())
	s.fleet().Relay("relay1.example", []string{"web1.example"})

	body, err := json.Marshal(job.Return{
		JID: "20260826T150532427456", NodeID: "web1.example", Fun: "test.ping", Success: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", transport.PathReturn, bytes.NewReader(body))
	// The certificate says the relay, because that is who dialled.
	s.returned(rec, req, "relay1.example")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("the return was refused: %d %s", rec.Code, rec.Body.String())
	}
	events, _, err := bus.Read(eventbus.Earliest, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, e := range events {
		tags = append(tags, e.Tag)
		if strings.HasSuffix(e.Tag, "/ret/web1.example") && e.Node != "web1.example" {
			t.Errorf("the event names %q as the node, not the one that ran it", e.Node)
		}
	}
	want := "halite/job/20260826T150532427456/ret/web1.example"
	for _, tag := range tags {
		if tag == want {
			return
		}
	}
	t.Errorf("no event is tagged %q; the bus holds %v", want, tags)
}
