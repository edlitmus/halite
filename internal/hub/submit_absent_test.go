package hub

import (
	"context"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// `halite-hub run` waits for one return from each node the hub says the job
// reached: the submit response's nodes less its absent ones. Dispatch decided
// who was reached from one look at the connected set and the sends it made;
// the handler then worked out `absent` again from a second look, after
// Dispatch returned. A node that connected in between, or one connected and
// too far behind to take the job, was sent nothing and yet was reported
// present, so the operator's command waited for a return that could never
// come, to the end of its gather window (issue 904). It was seen once in CI,
// when a node reconnected 131 microseconds after a dispatch that had found it
// absent, and `run` blocked for five minutes.
func TestTheSubmitResponseNamesTheNodesTheJobWasNotSentTo(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connectSilent(t, web1, "web1.example", `{}`)()

	// Connected, and too far behind to take another message: in the
	// connected set, and a send to it is refused. It is the state a node
	// is in for the instant between connecting and its stream being read,
	// and it makes the two looks disagree without a race.
	l.enrolled(t, "web2.example")
	behind := l.server.fleet().attach("web2.example", time.Now())
	for i := 0; i < queueDepth; i++ {
		behind.out <- &transport.Message{}
	}

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Nodes) != 2 {
		t.Fatalf("the job matched %v, want both nodes", res.Nodes)
	}
	if len(res.Absent) != 1 || res.Absent[0] != "web2.example" {
		t.Errorf("the response says the job was not sent to %v; it was sent to web1 and refused by web2, "+
			"so `run` would wait for two returns and get one", res.Absent)
	}
}
