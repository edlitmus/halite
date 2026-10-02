package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// pillarTargetTree is a pillar tree whose `role` depends on both the
// node's ID and a trusted grain, so a target that matches proves the hub
// compiled each candidate's pillar against that candidate's own cached
// grains rather than against nothing.
//
// `broken*` gets a file that will not parse, which is how a pillar that
// fails to compile for one node looks.
var pillarTargetTree = map[string]string{
	"top.sls": `base:
  'web*':
    - web
  'os:FreeBSD':
    - match: grain
    - bsd
  'broken*':
    - broken
`,
	"web.sls":    "role: web\n",
	"bsd.sls":    "platform: bsd\n",
	"broken.sls": "role: [this does not close\n",
}

// `-I` on the command line is SPEC 8.1's "compiled pillar, hub side".
// It used to be accepted and match nothing at all, because the value the
// matcher read was an empty map that nothing ever filled in.
func TestAPillarTargetSelectsTheNodesWhosePillarHasTheValue(t *testing.T) {
	l := newLab(t).withJobs(t).withPillar(t, pillarTargetTree)
	web1 := l.enrolled(t, "web1.example")
	db1 := l.enrolled(t, "db1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"Linux"}`)()
	defer l.connect(t, db1, "db1.example", `{"os":"FreeBSD"}`)()
	op := l.operator(t, "ed")

	for _, tc := range []struct {
		kind, target string
		want         string
	}{
		{"I", "role:web", "web1.example"},
		// From a grain the node reported, through the top file: this is
		// the case that needs the cached grains and not just the ID.
		{"I", "platform:bsd", "db1.example"},
		{"J", "role:^we", "web1.example"},
		{"C", "I@platform:bsd and not I@role:web", "db1.example"},
	} {
		res, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: tc.target, TargetKind: tc.kind, Fun: "test.ping",
		})
		if err != nil {
			t.Errorf("-%s %q: %v", tc.kind, tc.target, err)
			continue
		}
		if len(res.Nodes) != 1 || res.Nodes[0] != tc.want {
			t.Errorf("-%s %q matched %v, want [%s]", tc.kind, tc.target, res.Nodes, tc.want)
		}
	}
}

// A node whose pillar will not compile has no answer to "is role web?",
// and both guesses are wrong somewhere: treating it as empty pillar puts
// it inside every `not I@...`, and dropping it leaves a node out of a
// job with only the hub's log to say so. The dispatch is refused, naming
// the node.
func TestAPillarTargetRefusesWhenACandidatesPillarWillNotCompile(t *testing.T) {
	l := newLab(t).withJobs(t).withPillar(t, pillarTargetTree)
	web1 := l.enrolled(t, "web1.example")
	broken := l.enrolled(t, "broken1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"Linux"}`)()
	defer l.connect(t, broken, "broken1.example", `{"os":"Linux"}`)()
	op := l.operator(t, "ed")

	for _, tgt := range []struct{ kind, target string }{
		{"I", "role:web"},
		// The case that makes "treat it as empty" unsafe: an empty
		// pillar is not role:db, so the negation would have sent the
		// job to the node nobody could decide about.
		{"C", "not I@role:db"},
	} {
		res, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: tgt.target, TargetKind: tgt.kind, Fun: "test.ping",
		})
		if err == nil {
			t.Errorf("-%s %q was dispatched to %v with a candidate's pillar uncompilable",
				tgt.kind, tgt.target, res.Nodes)
			continue
		}
		if !strings.Contains(err.Error(), "broken1.example") {
			t.Errorf("-%s %q: the refusal does not name the node: %v", tgt.kind, tgt.target, err)
		}
	}

	// A target that decides the broken node without reading its pillar
	// is not held up by it: pillar is compiled only for a node whose
	// answer depends on it, which is also what keeps the cost down.
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "L@web1.example and I@role:web", TargetKind: "C", Fun: "test.ping",
	})
	if err != nil {
		t.Fatalf("a target that never reads the broken node's pillar was refused: %v", err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0] != "web1.example" {
		t.Errorf("matched %v", res.Nodes)
	}
}

// A node accepted but never connected has no grains on the hub, and a
// pillar compiled from none is not the pillar it would receive -- a top
// entry on `os` would be missing from it. It is undecided, as a broken
// pillar is, and for the same reason.
func TestAPillarTargetRefusesForANodeThatHasNeverConnected(t *testing.T) {
	l := newLab(t).withJobs(t).withPillar(t, pillarTargetTree)
	web1 := l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"Linux"}`)()
	op := l.operator(t, "ed")

	_, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "not I@platform:bsd", TargetKind: "C", Fun: "test.ping",
	})
	if err == nil {
		t.Fatal("a pillar target was decided for a node the hub holds no grains for")
	}
	if !strings.Contains(err.Error(), "db1.example") || !strings.Contains(err.Error(), "grains") {
		t.Errorf("the refusal does not name the node and what is missing: %v", err)
	}
}

// A hub with no pillar_roots has nothing to target pillar against, and
// saying so is better than the empty match it used to give.
func TestAPillarTargetOnAHubWithNoPillarIsRefused(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"Linux"}`)()
	op := l.operator(t, "ed")

	_, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "role:web", TargetKind: "I", Fun: "test.ping",
	})
	if err == nil {
		t.Fatal("a pillar target was accepted by a hub that compiles no pillar")
	}
	if !strings.Contains(err.Error(), "pillar_roots") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}
