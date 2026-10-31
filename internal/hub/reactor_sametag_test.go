package hub

import (
	"context"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

// Two entries with the same tag each get the event.
//
// A reactor's per-entry controls (the rate bucket, the dedupe window, the
// debounce timer) were kept in a map keyed by the entry's tag glob, and two
// entries can have the same glob: Salt's own `reactor:` list repeats a tag to
// run two things on it, `Matching` returns both, and nothing refuses the
// repeat. They then shared one set of controls. With `dedupe_window` on both,
// the first entry's reaction recorded the key and the second entry's event was
// judged a duplicate of it, so the second reaction never ran and nothing said
// so. DIVERGENCE 5.497.
func TestTwoEntriesWithTheSameTagAreEachReactedTo(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"a.sls": "ping_a:\n  local.test.ping:\n    - tgt: '*'\n",
		"b.sls": "ping_b:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls: ['$DIR/a.sls']
    principal: 'cert:CN=ed'
    dedupe_window: 1h
  - tag: 'halite/beacon/**'
    sls: ['$DIR/b.sls']
    principal: 'cert:CN=ed'
    dedupe_window: 1h
`)
	l.server.Policy = labPolicy(t)
	l.enrolled(t, "web1.example")
	handled := make(chan []ReactionResult, 4)
	r.Handled = func(_ string, res []ReactionResult) { handled <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	r.Offer(&eventbus.Event{
		Tag: "halite/beacon/web1.example/inotify/etc/thing", Node: "web1.example",
		Data: map[string]any{},
	})

	ran := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(ran) < 2 {
		select {
		case res := <-handled:
			for _, x := range res {
				ran[x.Reaction.ID] = true
			}
		case <-deadline:
			t.Fatalf("only %v ran; both entries match the tag and each should react", ran)
		}
	}
}

// The same holds for debounce: the timer's closure was the first entry's, so
// when two entries shared a key the burst ended in the first entry's reaction
// only.
func TestTwoDebouncedEntriesWithTheSameTagAreEachReactedTo(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"a.sls": "ping_a:\n  local.test.ping:\n    - tgt: '*'\n",
		"b.sls": "ping_b:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls: ['$DIR/a.sls']
    principal: 'cert:CN=ed'
    debounce: 200ms
  - tag: 'halite/beacon/**'
    sls: ['$DIR/b.sls']
    principal: 'cert:CN=ed'
    debounce: 200ms
`)
	l.server.Policy = labPolicy(t)
	l.enrolled(t, "web1.example")
	handled := make(chan []ReactionResult, 4)
	r.Handled = func(_ string, res []ReactionResult) { handled <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	r.Offer(&eventbus.Event{
		Tag: "halite/beacon/web1.example/inotify/etc/thing", Node: "web1.example",
		Data: map[string]any{},
	})

	ran := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(ran) < 2 {
		select {
		case res := <-handled:
			for _, x := range res {
				ran[x.Reaction.ID] = true
			}
		case <-deadline:
			t.Fatalf("only %v ran; both debounced entries match the tag and each should react", ran)
		}
	}
}
