package main

import (
	"strings"
	"testing"
	"time"
)

// The other place the node told the hub's tree from its own by the type
// of a job's n.files: extensionSource, which saltutil.sync_* reaches for.
// A sync the tracer samples and one it does not must give the same
// answer; the sampled one was refused with "this node is working from
// its own tree" on a node attached to its hub. Issue 803.
func TestATracedExtensionSyncReachesTheHubsTree(t *testing.T) {
	lab := startTracedEnvLab(t)
	answers := map[string]string{}
	for _, node := range []string{"plain1", "traced1"} {
		out, code := lab.hub("run", node, "saltutil.sync_all")
		t.Logf("%s: saltutil.sync_all exited %d:\n%s", node, code, out)
		if code != 0 {
			t.Errorf("%s: saltutil.sync_all failed", node)
		}
		answers[node] = out
	}
	if strings.Contains(answers["traced1"], "working from its own tree") {
		t.Fatalf("a traced sync was told the node has no hub tree:\n%s", answers["traced1"])
	}
	// The sync on traced1 was sampled, so the wrapper was in place;
	// without this the test would pass on a tracer that sampled nothing.
	deadline := time.Now().Add(20 * time.Second)
	for !lab.collector.saw(`"job saltutil.sync_all"`, "traced1") {
		if time.Now().After(deadline) {
			t.Fatalf("the collector never received traced1's sync span, so the traced path did not run:\n%s",
				tail(lab.collector.String(), 5))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
