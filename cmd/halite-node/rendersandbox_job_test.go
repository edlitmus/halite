package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/rendersandbox"
)

// The agent and its jobs share one render sandbox child, and the agent's
// close ends it.
//
// A job runs on a copy of the node (forJob), and renderEngine kept the
// sandbox in a field of whatever node it was called on. A copy that found
// the field nil built a sandbox of its own, which nothing closed: the
// agent's deferred closeRenderSandbox closed the agent's. Each such job
// left a child running until its pipes were finalised. Whether it happened
// depended on whether the agent's own node had built one before the first
// job. DIVERGENCE 5.592.
//
// The node is built by setup, as the agent's is, so the test holds the
// wiring the agent gets rather than a hand-made node's.
func TestJobsShareOneRenderSandboxChildAndTheAgentEndsIt(t *testing.T) {
	dir := t.TempDir()
	states, pillars := filepath.Join(dir, "states"), filepath.Join(dir, "pillar")
	for path, body := range map[string]string{
		filepath.Join(states, "top.sls"):  "base:\n  '*':\n    - web\n",
		filepath.Join(states, "web.sls"):  "{% set greeting = 'hello' %}\nsay:\n  test.nop:\n    - name: {{ greeting }}\n",
		filepath.Join(pillars, "top.sls"): "base: {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := writeConfig(t, dir, "render_sandbox: true\nnode_id: web1.example\nevidence: false\n"+
		"state_dir: "+filepath.Join(dir, "state")+"\ncache_dir: "+filepath.Join(dir, "cache")+"\n")
	args, err := cli.Parse([]string{"--config", path, "--root", dir})
	if err != nil {
		t.Fatal(err)
	}
	n := setup(args)
	n.pillars = fileserver.NewRoots(map[string][]string{"base": {pillars}})
	n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {states}}))
	t.Cleanup(n.closeRenderSandbox)

	before := rendersandbox.Live()
	for i := 0; i < 3; i++ {
		ret := n.executeJob(&job.Job{JID: job.ID("20261105T2"), Fun: "state.show_lowstate", Arg: []string{"web"}})
		if !ret.Success {
			t.Fatalf("the job failed: %s", ret.Return)
		}
		if !strings.Contains(string(ret.Return), "hello") {
			t.Fatalf("the template was not rendered: %s", ret.Return)
		}
	}
	if got := rendersandbox.Live() - before; got != 1 {
		t.Errorf("three jobs left %d render sandbox children running; they should share one", got)
	}
	n.closeRenderSandbox()
	if got := rendersandbox.Live() - before; got != 0 {
		t.Errorf("the agent's close left %d render sandbox children running", got)
	}
}
