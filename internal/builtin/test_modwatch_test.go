package builtin

import (
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

func testStateWatch(t *testing.T, name string, c *exec.Context, args *value.Map) states.Result {
	t.Helper()
	r := &Registries{Exec: exec.NewRegistry(), States: states.NewRegistry()}
	registerTest(r)
	if !r.States.SupportsWatch(name) {
		t.Fatalf("%s has no mod_watch", name)
	}
	res, err := r.States.CallWatch(c, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// Salt's test.mod_watch, in Salt's order (DIVERGENCE 5.478): a watch
// that fires runs the function and reacts only if it changed nothing; a
// listen reaction goes to mod_watch directly.
func TestTestModWatchFollowsSaltsOrder(t *testing.T) {
	watch := &exec.Context{Reaction: exec.ReactionWatch, WatchChanged: []string{"test: a"}}
	listen := &exec.Context{Reaction: exec.ReactionListen}

	// No changes of its own: mod_watch's answer, naming what changed.
	res := testStateWatch(t, "test.succeed_without_changes", watch, value.MapOf("name", "x"))
	if res.Comment != "Watch statement fired." || res.Result == nil || !*res.Result {
		t.Errorf("watch on an unchanged state = %+v", res)
	}
	got, _ := res.Changes.Get("Requisites with changes")
	if l, ok := got.([]any); !ok || len(l) != 1 || l[0] != "test: a" {
		t.Errorf("Requisites with changes = %#v, want [test: a]", got)
	}

	// Changes of its own: the function's answer stands, mod_watch is not called.
	res = testStateWatch(t, "test.succeed_with_changes", watch, value.MapOf("name", "x"))
	if res.Comment == "Watch statement fired." {
		t.Errorf("watch on a changed state called mod_watch: %+v", res)
	}
	if !res.HasChanges() {
		t.Errorf("the function's own changes were lost: %+v", res)
	}

	// A listen reaction skips the function: even a state that would have
	// changed answers with mod_watch's.
	res = testStateWatch(t, "test.succeed_with_changes", listen, value.MapOf("name", "x"))
	if res.Comment != "Watch statement fired." {
		t.Errorf("listen on a changing state = %+v, want mod_watch's answer", res)
	}
}

// What was written on the state replaces mod_watch's result and comment.
func TestTestModWatchTakesWhatWasWritten(t *testing.T) {
	listen := &exec.Context{Reaction: exec.ReactionListen}
	res := testStateWatch(t, "test.configurable_test_state", listen,
		value.MapOf("name", "x", "result", false, "comment", "mine"))
	if res.Result == nil || *res.Result || res.Comment != "mine" {
		t.Errorf("= %+v, want result false and comment mine", res)
	}
}
