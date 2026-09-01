package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/hub"
)

// writeTree lays out a tree of SLS files and returns its root.
func writeSLSTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// auditTree runs the audit the way the command wires it.
func auditSLSTree(t *testing.T, root string) *Report {
	t.Helper()
	registries := builtin.New()
	rep, err := Run(Options{
		Root:           root,
		Registry:       registries.Exec.Signatures(),
		StateRegistry:  registries.States.Signatures(),
		OrchRegistry:   hub.OrchSignatures(),
		RunnerRegistry: hub.NewRunners().Signatures(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// findingFor returns the finding whose subject matches, and whether one
// was found at all.
func findingFor(rep *Report, subject string) (Finding, bool) {
	for _, f := range rep.Findings {
		if f.Subject == subject {
			return f, true
		}
	}
	return Finding{}, false
}

// An orchestration step is not a missing state.
//
// The audit judged every declaration against the node-side state
// registry, which does not hold the `salt.*` steps and never will: they
// run on the hub. A real tree's orchestration and reactor files produced
// twenty-three blocking findings against functions this build ships,
// which is worse than a missed finding — it sends an operator to rewrite
// something that already works, and inflates the effort estimate that
// decides whether the migration is worth starting.
func TestAnOrchestrationStepIsNotAMissingState(t *testing.T) {
	root := writeSLSTree(t, map[string]string{
		"orch/cluster.sls": `deploy:
  salt.state:
    - tgt: 'web*'
    - sls:
      - app

ping_them:
  salt.function:
    - tgt: 'web*'
    - name: test.ping
`,
	})
	rep := auditSLSTree(t, root)

	for _, name := range []string{"salt.state", "salt.function"} {
		f, ok := findingFor(rep, name)
		if !ok {
			t.Fatalf("%s produced no finding at all", name)
		}
		if f.Severity == Blocking {
			t.Errorf("%s is reported blocking; this build ships it as an "+
				"orchestration step", name)
		}
		if !strings.Contains(f.Msg, "orchestration step") {
			t.Errorf("%s: the message does not say it is an orchestration step: %s",
				name, f.Msg)
		}
	}
	if n := rep.Count().Blocking; n != 0 {
		t.Errorf("an orchestration of shipped steps has %d blocking findings", n)
	}
}

// A reaction is not a missing state either, and the audit says which of
// the four forms it is and whether the thing it calls exists.
func TestAReactionIsReportedAsAReaction(t *testing.T) {
	root := writeSLSTree(t, map[string]string{
		"reactors/up.sls": `orchestrate:
  runner.state.orchestrate:
    - args:
      - mods: orch.cluster

sync:
  local.saltutil.sync_grains:
    - tgt: '*'

notify:
  local.slack.call_hook:
    - tgt: '*'
`,
	})
	rep := auditSLSTree(t, root)

	// Shipped: reported for review, naming what it found.
	for _, tc := range []struct{ subject, ships string }{
		{"runner.state.orchestrate", "state.orchestrate"},
		{"local.saltutil.sync_grains", "saltutil.sync_grains"},
	} {
		f, ok := findingFor(rep, tc.subject)
		if !ok {
			t.Fatalf("%s produced no finding", tc.subject)
		}
		if f.Severity == Blocking {
			t.Errorf("%s is reported blocking; this build ships %s", tc.subject, tc.ships)
		}
		if !strings.Contains(f.Msg, tc.ships) {
			t.Errorf("%s: the message does not name %s: %s", tc.subject, tc.ships, f.Msg)
		}
	}

	// Not shipped: still blocking, and it says what is missing rather
	// than calling the reaction itself wrong.
	f, ok := findingFor(rep, "local.slack.call_hook")
	if !ok {
		t.Fatal("local.slack.call_hook produced no finding")
	}
	if f.Severity != Blocking {
		t.Errorf("a reaction calling a function this build lacks should block, got %q",
			f.Severity)
	}
	if !strings.Contains(f.Msg, "slack.call_hook is not an execution function") {
		t.Errorf("the message does not name the missing function: %s", f.Msg)
	}
}

// And the change does not soften a real gap: a node state file naming a
// function this build lacks still blocks.
func TestARealStateGapStillBlocks(t *testing.T) {
	root := writeSLSTree(t, map[string]string{
		"base/app.sls": `set_it:
  grains.present:
    - name: role
    - value: web

copy_it:
  file.recurse:
    - name: /srv/app
    - source: salt://app/files
`,
	})
	rep := auditSLSTree(t, root)
	for _, name := range []string{"grains.present", "file.recurse"} {
		f, ok := findingFor(rep, name)
		if !ok {
			t.Fatalf("%s produced no finding", name)
		}
		if f.Severity != Blocking {
			t.Errorf("%s is a real gap and should block, got %q", name, f.Severity)
		}
	}
}

// A report says which build produced it.
//
// Two copies of a report sat side by side, one from before a fix to the
// audit and one from after, and nothing in either said which was which.
// The findings looked identical because the stale one was stale, and
// establishing that took longer than the fix had.
//
// The version is not asserted, only its presence: a test binary carries
// no commit stamp, so `version.Full` here is not what a released build
// prints, and pinning the string would test the harness rather than the
// report.
func TestAReportSaysWhichBuildProducedIt(t *testing.T) {
	root := writeSLSTree(t, map[string]string{
		"base/app.sls": "noop:\n  test.nop: []\n",
	})
	summary := auditSLSTree(t, root).Summary()

	lines := strings.Split(summary, "\n")
	if len(lines) < 2 {
		t.Fatal("the report has no header")
	}
	if !strings.HasPrefix(lines[1], "  by halite-hub ") {
		t.Errorf("the second line does not name the build that produced the "+
			"report, so two copies cannot be told apart: %q", lines[1])
	}
	if strings.TrimSpace(strings.TrimPrefix(lines[1], "  by halite-hub")) == "" {
		t.Error("the build stamp is empty")
	}
}
