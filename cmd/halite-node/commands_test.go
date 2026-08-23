package main

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func TestTraverseAllAnswersEveryKeyAsked(t *testing.T) {
	m := value.MapOf(
		"os", "FreeBSD",
		"osrelease", "15.1",
		"nested", value.MapOf("inner", "deep"),
	)

	got := traverseAll(m, []string{"osrelease", "nested:inner", "absent", "os"})

	// Salt's grains.item answers about every key it was given, in the
	// order it was given them. Dropping one silently is how a caller
	// reads the wrong grain and never finds out.
	want := [][2]string{
		{"osrelease", "15.1"},
		{"nested:inner", "deep"},
		{"absent", ""},
		{"os", "FreeBSD"},
	}
	if got.Len() != len(want) {
		t.Fatalf("answered %d keys, asked about %d", got.Len(), len(want))
	}
	for i, w := range want {
		k := got.Keys()[i]
		if k != w[0] {
			t.Errorf("key %d = %q, want %q", i, k, w[0])
			continue
		}
		if v, _ := got.Get(k); v != w[1] {
			t.Errorf("%s = %v, want %q", k, v, w[1])
		}
	}
}

// A job runs against a copy of the node, because a job that carries
// `test: true` used to set the flag on the agent itself and every job
// after it on that node was a dry run. `halite-hub run '*' state.apply`
// reported what it would do and an operator would have believed it had
// done it.
func TestAJobCannotChangeTheAgentItRunsIn(t *testing.T) {
	agent := &node{env: "base", pillarEnv: "base", test: false}
	worker := agent.forJob()
	worker.test = true
	worker.env = "staging"
	worker.pillarEnv = "staging"

	if agent.test {
		t.Error("a job put the agent into test mode")
	}
	if agent.env != "base" || agent.pillarEnv != "base" {
		t.Errorf("a job moved the agent to %s/%s", agent.env, agent.pillarEnv)
	}
	if worker == agent {
		t.Error("forJob returned the agent itself")
	}
}
