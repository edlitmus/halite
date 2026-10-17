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
