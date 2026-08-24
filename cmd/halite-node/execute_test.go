package main

import (
	"os"
	"path/filepath"
	"strings"
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
