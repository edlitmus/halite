package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/value"
)

// A node whose pillar does not compile can still be asked whether it is
// alive.
//
// It could not: every exec function compiled pillar first and failed the
// job when that failed, so one bad file in the pillar tree took the node
// silent — exactly when somebody was trying to find out what was wrong
// with it. Found by running a node against a real tree whose pillar this
// process had no GPG key for.
func TestABrokenPillarDoesNotStopEveryFunction(t *testing.T) {
	n := nodeWithBrokenPillar(t)

	// The question you ask when you think a node is broken.
	ret := n.executeJob(&job.Job{JID: job.ID("20260824T1"), Fun: "test.ping"})
	if !ret.Success {
		t.Errorf("test.ping failed on a node with a broken pillar: %s", ret.Return)
	}

	// Grains too: they come from the machine and owe pillar nothing.
	ret = n.executeJob(&job.Job{JID: job.ID("20260824T2"), Fun: "grains.items"})
	if !ret.Success {
		t.Errorf("grains.items failed on a node with a broken pillar: %s", ret.Return)
	}
}

// And a function that reads pillar says why, rather than answering with
// nothing where the values should be — which would be worse than the
// failure it replaced.
func TestAFunctionThatReadsPillarStillFails(t *testing.T) {
	n := nodeWithBrokenPillar(t)

	for _, fun := range []string{"pillar.items", "pillar.keys", "pillar.raw"} {
		ret := n.executeJob(&job.Job{JID: job.ID("20260824T3"), Fun: fun})
		if ret.Success {
			t.Errorf("%s succeeded with no pillar: %s", fun, ret.Return)
		}
		if !strings.Contains(string(ret.Return), "pillar") {
			t.Errorf("%s does not say what went wrong: %s", fun, ret.Return)
		}
	}
}

// nodeWithBrokenPillar is a node whose pillar top names a file that is
// not there, which is a compilation failure rather than an empty pillar.
func nodeWithBrokenPillar(t *testing.T) *node {
	t.Helper()
	dir := t.TempDir()
	pillarDir := filepath.Join(dir, "pillar")
	if err := os.MkdirAll(pillarDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pillarDir, "top.sls"),
		[]byte("base:\n  '*':\n    - missing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Node, config.LoadOptions{
		Path:         writeConfig(t, dir, "state_dir: "+dir+"\n"),
		AllowMissing: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger, err := log.New(log.Options{Level: log.Error, Format: log.JSON, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	return &node{
		cfg:       cfg,
		registry:  builtin.New(),
		grains:    value.NewMap(0),
		nodeID:    "web1.example",
		env:       "base",
		pillarEnv: "base",
		format:    cli.Nested,
		log:       logger,
		pillars:   fileserver.NewRoots(map[string][]string{"base": {pillarDir}}),
	}
}

// nodeWithTwoSLS is a node whose top file assigns two SLS files, each
// with one state that changes nothing, so a test can see which of them
// a job ran from the IDs in its return.
func nodeWithTwoSLS(t *testing.T) *node {
	t.Helper()
	n := nodeWithBrokenPillar(t)
	dir := t.TempDir()
	pillarDir := filepath.Join(dir, "pillar")
	stateDir := filepath.Join(dir, "states")
	for path, body := range map[string]string{
		filepath.Join(pillarDir, "top.sls"):                 "base: {}\n",
		filepath.Join(stateDir, "top.sls"):                  "base:\n  '*':\n    - webserver.nginx\n    - database.postgres\n",
		filepath.Join(stateDir, "webserver", "nginx.sls"):   "from_webserver:\n  test.succeed_without_changes: []\n",
		filepath.Join(stateDir, "database", "postgres.sls"): "from_database:\n  test.succeed_without_changes: []\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n.pillars = fileserver.NewRoots(map[string][]string{"base": {pillarDir}})
	n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {stateDir}}))
	// newNode sets this; a state job counts itself in it for SPEC 16.3.
	n.statesRunning = new(atomic.Int64)
	return n
}

// The hub's allow_sls (internal/policy checkArgs) refuses the requests
// below because of what the node does with them, and that is a claim
// about this file, not about the policy. So it is held here: if the
// node ever starts honouring state.highstate's arguments or a keyword
// `mods`, these fail, and the policy's refusal can be reconsidered
// rather than left standing for a reason that stopped being true.
// DIVERGENCE 5.319.
func TestTheNodeRunsTheHighstateForTheJobsAllowSLSRefuses(t *testing.T) {
	cases := []struct {
		name  string
		fun   string
		arg   []string
		kwarg map[string]any
	}{
		{"state.apply with no names", "state.apply", nil, nil},
		{"state.sls with no names", "state.sls", nil, nil},
		{"state.apply with mods only as a keyword", "state.apply", nil,
			map[string]any{"mods": "webserver.nginx"}},
		{"state.highstate naming one sls", "state.highstate", []string{"webserver.nginx"}, nil},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kwarg := map[string]any{"test": true}
			for k, v := range c.kwarg {
				kwarg[k] = v
			}
			ret := nodeWithTwoSLS(t).executeJob(&job.Job{
				JID: job.ID(fmt.Sprintf("20261016T%d", i)), Fun: c.fun, Arg: c.arg, Kwarg: kwarg,
			})
			if !ret.Success {
				t.Fatalf("the job failed: %s", ret.Return)
			}
			for _, id := range []string{"from_webserver", "from_database"} {
				if !strings.Contains(string(ret.Return), id) {
					t.Errorf("%s did not run %s, so it was not the highstate: %s", c.fun, id, ret.Return)
				}
			}
		})
	}

	// And the control: naming an SLS to state.apply runs that SLS and
	// nothing else, which is the request allow_sls still permits.
	ret := nodeWithTwoSLS(t).executeJob(&job.Job{
		JID: job.ID("20261016T9"), Fun: "state.apply", Arg: []string{"webserver.nginx"},
		Kwarg: map[string]any{"test": true},
	})
	if !ret.Success {
		t.Fatalf("the job failed: %s", ret.Return)
	}
	if !strings.Contains(string(ret.Return), "from_webserver") || strings.Contains(string(ret.Return), "from_database") {
		t.Errorf("state.apply webserver.nginx did not run exactly that SLS: %s", ret.Return)
	}
}
