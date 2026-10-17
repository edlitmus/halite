package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// **A node cannot answer for a job it was not sent** (issue 321, M-12).
// web2 is enrolled and in good standing; the job goes to web1 only. On
// main web2's return was stored and emitted as `ret/web2.example`, which
// is what a reactor and the API's synchronous run both read.
func TestAReturnFromANodeOutsideTheJobIsRefused(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	defer l.connect(t, web1, "web1.example", `{}`)()

	res, err := l.operator(t, "ed").Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = web2.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web2.example", Fun: "test.ping", Success: true,
		Return: json.RawMessage(`true`), Schema: job.ReturnSchema,
	})
	if err == nil || !strings.Contains(err.Error(), "not sent to this node") {
		t.Fatalf("web2's return for a job sent to web1: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		returns, err := l.server.Jobs.Returns(job.ID(res.JID))
		if err != nil {
			t.Fatal(err)
		}
		if len(returns) == 1 {
			if returns[0].NodeID != "web1.example" {
				t.Errorf("the return filed is from %s", returns[0].NodeID)
			}
			break
		}
		if len(returns) > 1 || time.Now().After(deadline) {
			t.Fatalf("%d returns filed", len(returns))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, e := range readEvents(t, l.operator(t, "ed"), []string{"halite/job/" + res.JID + "/ret/*"}) {
		if strings.HasSuffix(e.Tag, "/web2.example") {
			t.Errorf("the bus carries %s", e.Tag)
		}
	}
}
