package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// **One node cannot flood the bus** (issue 321, M-13). The bus is
// bounded by size and pruned oldest first, and nothing bounded what one
// node could post, so a node posting large events as fast as the hub
// took them erased everybody's history at the next prune. The budget is
// per node: web1 running out of it does not touch web2.
func TestANodeOverItsEventBudgetIsRefusedAndOthersAreNot(t *testing.T) {
	l := metricsLab(t, metricsPolicy).withEvents(t)
	l.server.NodeEventRate = 1 // a byte a second: nothing refills in the test
	l.server.NodeEventBurst = 4096
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")

	data, _ := json.Marshal(map[string]any{"blob": strings.Repeat("z", 3000)})
	send := func(c *transport.Client) error {
		_, err := c.SendEvent(context.Background(), transport.EventRequest{Tag: "flood", Data: data})
		return err
	}
	if err := send(web1); err != nil {
		t.Fatalf("the first event, inside the burst: %v", err)
	}
	err := send(web1)
	if err == nil || !strings.Contains(err.Error(), "node_event_rate") {
		t.Fatalf("the second event, past the burst: %v", err)
	}
	if err := send(web2); err != nil {
		t.Errorf("another node was refused for web1's flood: %v", err)
	}

	got := readEvents(t, l.operator(t, "ed"), []string{"halite/node/*/flood"})
	if len(got) != 2 {
		t.Errorf("the bus holds %d flood events, want web1's first and web2's", len(got))
	}
	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `halite_events_dropped_total{reason="node_rate"} 1`) {
		t.Errorf("the refusal was not counted:\n%s", grepLines(out, "events_dropped"))
	}
}

// **A return is held to SPEC 6.5's 16 MiB**, which was written down
// and not enforced: the handler read 64 MiB, against every job a node
// was sent.
func TestAReturnPastTheBoundIsRefused(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connect(t, web1, "web1.example", `{}`)()
	res, err := l.operator(t, "ed").Submit(context.Background(), transport.SubmitRequest{Target: "web1.example", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}
	big, _ := json.Marshal(strings.Repeat("r", transport.MaxReturnBody))
	// The lab's node may already have answered, which would make this a
	// retry; it is refused by its size before anything looks at that.
	err = web1.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web1.example", Fun: "test.ping",
		Return: big, Schema: job.ReturnSchema,
	})
	if err == nil {
		t.Fatalf("a %d-byte return was taken", len(big))
	}
}

// **A return refused for its size is said on the hub**, and the node's
// stand-in for it settles the job.
//
// The refusal used to be a 400 and nothing else: the hub logged nothing,
// the node logged one line and dropped the return, and the job -- which
// had run, in the case that found it a node replacing its own binary --
// never answered. The jid is recovered from the start of the body,
// because the hub stops reading long before it could decode the rest.
func TestAnOversizedReturnIsLoggedAndItsStandInSettlesTheJob(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connectSilent(t, web1, "web1.example", `{}`)()
	res, err := l.operator(t, "ed").Submit(context.Background(), transport.SubmitRequest{Target: "web1.example", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}
	big, _ := json.Marshal(strings.Repeat("r", transport.MaxReturnBody))
	ret := &job.Return{
		JID: job.ID(res.JID), NodeID: "web1.example", Fun: "test.ping",
		Success: true, Return: big, Schema: job.ReturnSchema,
	}
	if err := web1.Return(context.Background(), ret); err == nil {
		t.Fatalf("a %d-byte return was taken", len(big))
	}
	logged := l.logs.String()
	line := ""
	for _, l := range strings.Split(logged, "\n") {
		if strings.Contains(l, "refused for its size") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("the hub refused a return and logged nothing about it:\n%s", logged)
	}
	for _, want := range []string{res.JID, "web1.example"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refusal does not name %s: %s", want, line)
		}
	}

	if missing, _ := l.server.Jobs.Missing(job.ID(res.JID)); len(missing) != 1 {
		t.Fatalf("before the stand-in, missing = %v; want web1 still owed", missing)
	}
	if err := web1.Return(context.Background(), ret.Oversized(len(big), transport.MaxReturnBody)); err != nil {
		t.Fatalf("the hub refused the stand-in: %v", err)
	}
	if missing, err := l.server.Jobs.Missing(job.ID(res.JID)); err != nil || len(missing) != 0 {
		t.Errorf("after the stand-in, missing = %v (%v); the job should be answered", missing, err)
	}
	got, err := l.server.Jobs.Returns(job.ID(res.JID))
	if err != nil || len(got) != 1 {
		t.Fatalf("returns = %v (%v)", got, err)
	}
	var body struct {
		Limit int `json:"limit"`
	}
	if err := json.Unmarshal(got[0].Return, &body); err != nil || got[0].Success || body.Limit != transport.MaxReturnBody {
		t.Errorf("the stored return is not the stand-in: success=%v %s", got[0].Success, got[0].Return)
	}
}

// A return of exactly the limit, as the node's client encodes it, is
// taken over a real connection; one byte more is not. The node decides
// whether to send a stand-in by transport.EncodeBody's length, so this is
// what that decision rests on.
func TestAReturnOfExactlyTheLimitIsTaken(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	// The deadline a real node has, not the lab's. The lab gives its
	// clients 5 s, which is ample for the short requests it was chosen
	// for; this is the one test that sends the largest body the hub
	// takes and waits for the hub to read and store all of it. halite-node
	// sets no Timeout on its hub client, so its returns get the 30 s
	// default of transport.Client. Under -race on a shared ubuntu-24.04
	// runner the first post here ran past 5 s (PR 701's race job,
	// 2026-11-09: "context deadline exceeded", 9 s for the test), which
	// failed a test about size on a question of speed. Set before
	// connectSilent, whose stream goroutine reads the client.
	web1.Timeout = 0
	defer l.connectSilent(t, web1, "web1.example", `{}`)()
	op := l.operator(t, "ed")
	sized := func(extra int) *job.Return {
		res, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "web1.example", Fun: "test.ping"})
		if err != nil {
			t.Fatal(err)
		}
		ret := &job.Return{JID: job.ID(res.JID), NodeID: "web1.example", Fun: "test.ping",
			Success: true, Return: json.RawMessage(`"<"`), Schema: job.ReturnSchema}
		raw, err := transport.EncodeBody(ret)
		if err != nil {
			t.Fatal(err)
		}
		ret.Return = json.RawMessage(`"<` + strings.Repeat("r", transport.MaxReturnBody-len(raw)+extra) + `"`)
		return ret
	}
	if err := web1.Return(context.Background(), sized(0)); err != nil {
		t.Errorf("a return of exactly %d bytes was refused: %v", transport.MaxReturnBody, err)
	}
	if err := web1.Return(context.Background(), sized(1)); err == nil {
		t.Errorf("a return of %d bytes was taken", transport.MaxReturnBody+1)
	}
}

func TestPeekJID(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"jid":"20261108T1","id":"web1"`, "20261108T1"},
		{`{"id":"web1","fun":"x","jid":"J","return":"rrrr`, "J"},
		{`{"id":"web1","return":"rrrrrrrrrr`, ""},
		{`{"jid":"2026110`, ""},
		{`["jid","x"]`, ""},
		{``, ""},
		{`{"nested":{"jid":"no"},"jid":"yes"}`, "yes"},
	} {
		if got := peekJID([]byte(c.in)); got != c.want {
			t.Errorf("peekJID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
