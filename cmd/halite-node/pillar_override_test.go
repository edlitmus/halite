package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/value"
)

// A state job whose pillar override this node cannot use is refused at
// admission, before the acceptance record, so the evidence chain says
// refused rather than accepted and then failed. The real-binary test
// (TestAStateJobsPillarOverrideAppliesToThatRunOnly) holds what an
// accepted override does; this holds the ones that are never accepted.
func TestAStateJobWithAnUnusablePillarOverrideIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		kwarg map[string]any
		want  string
	}{
		{"a string", map[string]any{"pillar": "role: db"}, "a pillar override must be a mapping"},
		{"a list", map[string]any{"pillar": []any{"role"}}, "a pillar override must be a mapping"},
		{"a number", map[string]any{"pillar": 3}, "a pillar override must be a mapping"},
		{"an encrypted override", map[string]any{"pillar": map[string]any{"pw": "-----BEGIN PGP MESSAGE-----"},
			"pillar_enc": "gpg"}, "pillar_enc=gpg, which this node does not do"},
	}
	for _, c := range cases {
		n := nodeForEvidence(t, "")
		n.refusals = make(chan *job.Return, 4)
		n.executor = newExecutor(n, 4, func(*job.Return) {})
		msg := jobMessage(t, "20261123T080000000201")
		msg.Fun = "state.apply"
		msg.Kwarg = c.kwarg
		n.acceptJob(msg)
		if n.executor.Depth() != 0 {
			t.Errorf("%s: a state job with that override was accepted", c.name)
			continue
		}
		if refusal := lastRefusal(t, n); !strings.Contains(refusal, c.want) {
			t.Errorf("%s: the refusal does not say why: %s", c.name, refusal)
		}
	}
}

// To an execution function `pillar` is an ordinary keyword argument, so
// the state functions' rule is not applied to it.
func TestAPillarKeywordOnAnExecutionFunctionIsNotJudgedAsAnOverride(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	msg := jobMessage(t, "20261123T080000000202")
	msg.Fun = "test.arg"
	msg.Kwarg = map[string]any{"pillar": "not a mapping, and not an override either"}
	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("test.arg with a pillar= keyword was refused: %s", lastRefusal(t, n))
	}
}

// No override, and an empty one, leave the run's pillar as the node's:
// nothing is merged and nothing is logged as merged.
func TestAnAbsentOrEmptyPillarOverrideIsNoOverride(t *testing.T) {
	for _, kwarg := range []map[string]any{nil, {"pillar": nil}, {"pillar": map[string]any{}}} {
		override, err := jobPillarOverride(&job.Job{Fun: "state.apply", Kwarg: kwarg})
		if err != nil || override != nil {
			t.Errorf("%v: got %v, %v; want no override and no error", kwarg, override, err)
		}
	}
}

// The merge is Salt's for this node's settings, and changes neither side:
// the compiled pillar is still the node's after a run that overrode it.
func TestAPillarOverrideMergesLikeAPillarSourceAndChangesNeitherSide(t *testing.T) {
	n := nodeForEvidence(t, "")
	compiled := value.FromJSON(map[string]any{
		"nested": map[string]any{"keep": "kept", "over": "base"},
		"list":   []any{"a", "b"},
	}).(*value.Map)
	override := value.FromJSON(map[string]any{
		"nested": map[string]any{"over": "job"},
		"list":   []any{"c"},
	}).(*value.Map)
	merged := n.mergePillarOverride(compiled, override)
	got, err := value.EncodeJSON(merged, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Under the default `smart` with pillar_merge_lists off, a mapping is
	// merged key by key and a list is replaced, as in Salt.
	if want := `{"list":["c"],"nested":{"keep":"kept","over":"job"}}`; string(got) != want {
		t.Errorf("merged to %s, want %s", got, want)
	}
	before, _ := value.EncodeJSON(compiled, 0)
	if want := `{"list":["a","b"],"nested":{"keep":"kept","over":"base"}}`; string(before) != want {
		t.Errorf("the compiled pillar was changed by the merge: %s", before)
	}
}
