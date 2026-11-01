package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// `halite/state/<jid>/<node>/<result>` says a state run completed, and it is
// the tag reactors and change feeds act on. It was published for any return
// whose `Out` was "highstate", a field the node supplies and also sets for
// the `show_*` functions, so a `test.ping` that claimed it, or a
// `state.show_lowstate`, announced a state run that never happened.
// DIVERGENCE 5.517.
func TestAStateResultIsPublishedOnlyForAJobThatAppliesStates(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	node := l.enrolled(t, "web1.example")
	stop := l.connectSilent(t, node, "web1.example", `{}`)
	defer stop()
	op := l.operator(t, "ed")

	for _, tc := range []struct {
		fun       string
		wantEvent bool
	}{
		{"test.ping", false},
		{"state.show_lowstate", false},
		{"state.apply", true},
		{"state.highstate", true},
	} {
		res, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: "web1.example", TargetKind: "L", Fun: tc.fun,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.fun, err)
		}
		if err := node.Return(context.Background(), job.Return{
			JID: job.ID(res.JID), NodeID: "web1.example", Fun: tc.fun, Success: true,
			Out: "highstate", Return: json.RawMessage(`{}`), Schema: job.ReturnSchema,
		}); err != nil {
			t.Fatalf("%s: %v", tc.fun, err)
		}
		events, _, err := l.server.Events.Read(eventbus.Earliest,
			[]string{"halite/state/" + res.JID + "/**"}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(events) > 0; got != tc.wantEvent {
			t.Errorf("%s: a state result was published = %v, want %v", tc.fun, got, tc.wantEvent)
		}
	}
}
