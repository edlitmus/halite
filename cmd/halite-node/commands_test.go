package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
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

// A scheduled job's return goes to the node's own log, which is the
// `local` returner SPEC 20.3 makes the default.
//
// Not to the hub: the hub refuses a return for a job it never
// dispatched, which is the right refusal — and the node retried it five
// times with backoff for every scheduled run until this existed.
func TestAScheduledReturnGoesToTheNodesOwnLog(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(config.Node, config.LoadOptions{
		Path:         writeConfig(t, dir, "state_dir: "+dir+"\n"),
		AllowMissing: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	n := &node{cfg: cfg}

	ret := &job.Return{
		JID: job.ID("20260823T101500.000000"), NodeID: "web1.example",
		Fun: "test.ping", Success: true, Schema: job.ReturnSchema,
		Return: json.RawMessage(`true`),
	}
	for i := 0; i < 2; i++ {
		if err := n.writeLocalReturn(ret); err != nil {
			t.Fatal(err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "returns.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("two returns wrote %d line(s)", len(lines))
	}
	// One JSON object per line, so a truncated write is a line that
	// will not parse rather than a file that will not.
	for i, line := range lines {
		var back job.Return
		if err := json.Unmarshal([]byte(line), &back); err != nil {
			t.Errorf("line %d does not parse: %v", i+1, err)
			continue
		}
		if back.JID != ret.JID || back.Fun != "test.ping" {
			t.Errorf("line %d came back as %+v", i+1, back)
		}
	}
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
