package hub

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/metrics"
)

// An event the hub publishes is counted, forwarded to a relay's upstream, and
// written under its `salt/...` spelling when `event_tag_compat` is on.
// `event.send`, the runner an orchestration or a reactor uses to put an event
// on the bus, appended to the bus itself and did none of the three: a relay's
// upstream never heard it, `halite_events_published_total` did not count it,
// and a consumer on the compatibility spelling missed it. DIVERGENCE 5.512.
func TestEventSendPublishesLikeAnyOtherEvent(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.server.EventTagCompat = true
	reg := metrics.NewRegistry()
	l.server.Metrics = reg
	var mu sync.Mutex
	var forwarded []string
	l.server.OnEvent = func(e *eventbus.Event) {
		mu.Lock()
		forwarded = append(forwarded, e.Tag)
		mu.Unlock()
	}

	out, err := l.server.CallRunner(context.Background(), RunnerCall{
		Principal: "cert:CN=ed", Fun: "event.send",
		Kwarg: map[string]any{"tag": "halite/node/web1.example/start", "data": map[string]any{"version": "x"}},
	})
	if err != nil || !out.Success {
		t.Fatalf("event.send: %v %+v", err, out)
	}

	eventually(t, "the event to be forwarded", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, tag := range forwarded {
			if tag == "halite/node/web1.example/start" {
				return true
			}
		}
		return false
	})
	events, _, err := l.server.Events.Read(eventbus.Earliest, []string{"salt/**"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Error("event_tag_compat is on and the event has no salt/... spelling on the bus")
	}
	var text strings.Builder
	reg.Write(&text)
	if !strings.Contains(text.String(), `halite_events_published_total{tag_prefix="halite/node"} 1`) {
		t.Errorf("the event was not counted:\n%s", section(text.String(), "halite_events_published_total"))
	}
}
