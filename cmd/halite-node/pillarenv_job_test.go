package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/job"
)

// A state job naming a pillarenv this node will not read from is refused
// at admission, before the acceptance record, as an unusable pillar
// override is. The real-binary test
// (TestAStateJobsPillarenvCompilesThatRunsPillarFromIt) holds what an
// accepted one does.
func TestAStateJobWithAPillarenvThisNodeWillNotUseIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		config string
		kwarg  map[string]any
		want   string
	}{
		{"a number", "", map[string]any{"pillarenv": 3}, "a pillar environment is named by a string"},
		{"a list", "", map[string]any{"pillarenv": []any{"dev"}}, "a pillar environment is named by a string"},
		{"denied", "env_denylist: [secret]\n", map[string]any{"pillarenv": "secret"}, "excluded by env_denylist"},
		{"not allowed", "env_allowlist: [base]\n", map[string]any{"pillarenv": "dev"}, "not in env_allowlist"},
	}
	for _, c := range cases {
		n := nodeForEvidence(t, c.config)
		n.refusals = make(chan *job.Return, 4)
		n.executor = newExecutor(n, 4, func(*job.Return) {})
		msg := jobMessage(t, "20261123T090000000301")
		msg.Fun = "state.apply"
		msg.Kwarg = c.kwarg
		n.acceptJob(msg)
		if n.executor.Depth() != 0 {
			t.Errorf("%s: a state job with that pillarenv was accepted", c.name)
			continue
		}
		if refusal := lastRefusal(t, n); !strings.Contains(refusal, c.want) {
			t.Errorf("%s: the refusal does not say why: %s", c.name, refusal)
		}
	}
}

// An allowed pillarenv, and none at all, are admitted.
func TestAStateJobWithAPermittedPillarenvIsAdmitted(t *testing.T) {
	for _, kwarg := range []map[string]any{nil, {"pillarenv": nil}, {"pillarenv": ""}, {"pillarenv": "dev"}} {
		n := nodeForEvidence(t, "env_allowlist: [base, dev]\n")
		n.refusals = make(chan *job.Return, 4)
		n.executor = newExecutor(n, 4, func(*job.Return) {})
		msg := jobMessage(t, "20261123T090000000302")
		msg.Fun = "state.apply"
		msg.Kwarg = kwarg
		n.acceptJob(msg)
		if n.executor.Depth() != 1 {
			t.Errorf("%v: refused: %s", kwarg, lastRefusal(t, n))
		}
	}
}

// To an execution function `pillarenv` is an ordinary keyword argument
// (pillar.items takes one), so the state functions' rule is not applied.
func TestAPillarenvKeywordOnAnExecutionFunctionIsNotJudged(t *testing.T) {
	n := nodeForEvidence(t, "env_denylist: [secret]\n")
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	msg := jobMessage(t, "20261123T090000000303")
	msg.Fun = "test.arg"
	msg.Kwarg = map[string]any{"pillarenv": "secret"}
	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("test.arg with a pillarenv= keyword was refused: %s", lastRefusal(t, n))
	}
}

// Agentless mode has the one pillar the hub sent with the job, so a job
// naming another pillarenv is refused rather than given that one.
func TestAnAgentlessRunRefusesAPillarenv(t *testing.T) {
	n := nodeForEvidence(t, "")
	n.pillarPushed = true
	_, err := n.jobPillarEnv(&job.Job{Fun: "state.apply", Kwarg: map[string]any{"pillarenv": "dev"}})
	if err == nil || !strings.Contains(err.Error(), "agentless") {
		t.Fatalf("got %v, want a refusal naming agentless mode", err)
	}
	if env, err := n.jobPillarEnv(&job.Job{Fun: "state.apply"}); env != "" || err != nil {
		t.Fatalf("a job naming no pillarenv: got %q, %v", env, err)
	}
}

// A node working from its own roots -- with no hub, or on a hub that
// compiles no pillar -- compiles the named environment's pillar, alone,
// and its own pillar environment is not changed by being asked.
func TestARunsPillarenvCompilesFromTheNodesOwnRootsWithoutAHub(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("base/top.sls", "base:\n  '*':\n    - app\n")
	write("base/app.sls", "where: base\n")
	write("other/top.sls", "other:\n  '*':\n    - app\n")
	write("other/app.sls", "where: other\nonly_other: true\n")
	n := nodeForEvidence(t, "pillar_roots: {base: ["+dir+"/base], other: ["+dir+"/other]}\n")
	n.args = &cli.Args{}

	got, err := n.runPillarIn("other")
	if err != nil {
		t.Fatal(err)
	}
	if where, _ := got.Get("where"); where != "other" {
		t.Errorf("pillarenv=other compiled where=%v", where)
	}
	if _, ok := got.Get("only_other"); !ok {
		t.Errorf("pillarenv=other is missing a key only it has: %v", got)
	}
	if n.pillarEnv != "base" {
		t.Errorf("asking for another environment changed the node's to %q", n.pillarEnv)
	}
}
