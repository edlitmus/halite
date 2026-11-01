package hub

import (
	"context"
	"fmt"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// A scoped caller's page is made of events it may see.
//
// `event.replay` took `limit` events off the bus and dropped the ones about
// nodes outside the caller's scope afterwards, so a caller bound to `web*`
// asking for ten events on a bus whose next ten were about `db1` was handed
// an empty page and a `next` offset. The page was well formed and said
// nothing wrong; a caller that reads an empty page as the end of the log
// stopped there, with its own events further on. DIVERGENCE 5.525.
func TestAScopedReplayPageIsFilledWithWhatTheCallerMaySee(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.server.Policy = runnerScopePolicy(t)
	alice := l.operator(t, "alice")

	for i := 0; i < 10; i++ {
		l.server.emit(fmt.Sprintf("halite/node/db1.example/note%d", i), "", map[string]any{"i": i})
	}
	for i := 0; i < 3; i++ {
		l.server.emit(fmt.Sprintf("halite/node/web1.example/note%d", i), "", map[string]any{"i": i})
	}

	res, err := alice.Runner(context.Background(), transport.RunnerRequest{
		Fun:   "event.replay",
		Kwarg: map[string]any{"tag": "halite/node/**", "from": "earliest", "limit": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := mapOf(t, returned(t, res))
	events, _ := page.Get("events")
	items, _ := events.([]any)
	if len(items) != 3 {
		t.Errorf("a caller bound to web nodes asked for five events and the bus holds three about them "+
			"behind ten it may not see: it was handed %d", len(items))
	}
	for _, it := range items {
		if tag, _ := mapOf(t, it).Get("_tag"); tag == nil || fmt.Sprint(tag)[:len("halite/node/web")] != "halite/node/web" {
			t.Errorf("a caller bound to web nodes was handed %v", tag)
		}
	}
}

// The page stops at the limit even when most of what the bus holds is hidden
// from the caller: the scan does not read on for ever to fill it.
func TestAScopedReplayPageStopsAtTheLimit(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.server.Policy = runnerScopePolicy(t)
	alice := l.operator(t, "alice")

	for i := 0; i < 20; i++ {
		l.server.emit(fmt.Sprintf("halite/node/web1.example/note%d", i), "", map[string]any{"i": i})
		l.server.emit(fmt.Sprintf("halite/node/db1.example/note%d", i), "", map[string]any{"i": i})
	}
	res, err := alice.Runner(context.Background(), transport.RunnerRequest{
		Fun:   "event.replay",
		Kwarg: map[string]any{"tag": "halite/node/**", "from": "earliest", "limit": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := mapOf(t, returned(t, res))
	events, _ := page.Get("events")
	if items, _ := events.([]any); len(items) != 5 {
		t.Errorf("limit 5 returned %d events", len(items))
	}
}
