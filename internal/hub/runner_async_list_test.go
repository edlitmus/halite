package hub

import (
	"slices"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// The callers that wait for a runner's outcome -- `halite-hub runner`,
// `orch run`, halite-api -- decide which calls to start `async` from
// transport's list, and the hub decides which calls accept it from each
// runner's signature. Two paths that must agree: a runner given `async`
// and left off the list would be waited for on one request again (issue
// 822), and a name on the list whose runner lost the parameter would be
// refused by every caller, because the hub binds keywords strictly.
func TestTheRunnersThatTakeAsyncAreTheOnesCallersWaitFor(t *testing.T) {
	reg := NewRunners()
	var takesAsync []string
	for _, name := range reg.Names() {
		sig, _ := reg.Signatures().Lookup(name)
		if _, ok := sig.Param("async"); ok {
			takesAsync = append(takesAsync, name)
		}
	}
	listed := transport.AsyncRunners()
	slices.Sort(takesAsync)
	slices.Sort(listed)
	if !slices.Equal(takesAsync, listed) {
		t.Fatalf("the runners that take async are %v; the callers wait for %v", takesAsync, listed)
	}
}
