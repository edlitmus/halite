package hub

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// A job's creation and a runner call's carry their arguments and have no node
// of their own, so `event.replay` and `event.listen` kept them for every
// caller: a caller scoped to web* was handed `cmd.run` and its arguments for
// a job sent to db1, and the pillar name another operator had asked for
// (issue 886). They are now judged by what they are about, by the rule the API's
// stream uses.
//
// This goes through the hub's own runner path with real certificates: the run
// events here are made by the callers themselves, so the principal in each is
// the one the hub recorded and not one the test wrote.
func TestAReplayShowsAJobOrARunOnlyToACallerItFitsInside(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.server.Policy = runnerScopePolicy(t)
	alice := l.operator(t, "alice")
	ed := l.operator(t, "ed")

	l.server.emit("halite/job/J1/new", "", map[string]any{
		"jid": "J1", "fun": "test.ping", "nodes": []string{"web1.example"}})
	l.server.emit("halite/job/J2/new", "", map[string]any{
		"jid": "J2", "fun": "cmd.run", "arg": []string{"mysql -psecret"},
		"nodes": []string{"web1.example", "db1.example"}})
	l.server.emit("halite/job/J3/new", "", map[string]any{
		"jid": "J3", "fun": "cmd.run", "nodes": []string{"db1.example"}})
	l.server.emit("salt/job/J2/new", "", map[string]any{
		"jid": "J2", "fun": "cmd.run", "nodes": []string{"web1.example", "db1.example"}})

	// Each makes a runner call, which emits its own `new` and `ret`.
	if _, err := ed.Runner(context.Background(), transport.RunnerRequest{Fun: "manage.status"}); err != nil {
		t.Fatalf("ed's runner call: %v", err)
	}
	if _, err := alice.Runner(context.Background(), transport.RunnerRequest{Fun: "manage.status"}); err != nil {
		t.Fatalf("alice's runner call: %v", err)
	}

	tagsFor := func(who *transport.Client) []string {
		res, err := who.Runner(context.Background(), transport.RunnerRequest{
			Fun:   "event.replay",
			Kwarg: map[string]any{"tag": "halite/job/**,salt/job/**,halite/run/**", "from": "earliest", "limit": 100},
		})
		if err != nil {
			t.Fatal(err)
		}
		events, _ := mapOf(t, returned(t, res)).Get("events")
		items, _ := events.([]any)
		var tags []string
		for _, it := range items {
			event := mapOf(t, it)
			tag, _ := event.Get("_tag")
			// The replay is a runner call too, and its own events are not
			// what is being counted.
			if data, ok := event.Get("data"); ok {
				if fun, _ := mapOf(t, data).Get("fun"); fun == "event.replay" {
					continue
				}
			}
			tags = append(tags, fmt.Sprint(tag))
		}
		sort.Strings(tags)
		return tags
	}

	// Alice is scoped to web*: she sees the job that stayed inside it, and
	// her own runner call's creation and end; she does not see the other
	// two jobs, the compatibility copy of one of them, or ed's runner call.
	var jobs, runs int
	for _, tag := range tagsFor(alice) {
		switch {
		case tag == "halite/job/J1/new":
			jobs++
		case strings.HasPrefix(tag, "halite/job/") || strings.HasPrefix(tag, "salt/job/"):
			t.Errorf("a caller scoped to web nodes was shown %s, a job that reached db1", tag)
		case strings.HasPrefix(tag, "halite/run/"):
			runs++
		}
	}
	if jobs != 1 {
		t.Errorf("the job that stayed inside the web nodes was shown %d times, want 1", jobs)
	}
	if runs != 2 {
		t.Errorf("alice was shown %d of the events of her own runner call (its new and its ret), want 2", runs)
	}

	// Ed's target is `*`, so he sees every one of them: three jobs, the
	// compatibility copy, and both callers' runner calls (two events each).
	if got := len(tagsFor(ed)); got != 8 {
		t.Errorf("a caller with a target of * was shown %d of these events, want 8: %v", got, tagsFor(ed))
	}
}
