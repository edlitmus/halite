package hub

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The second step fails the first time round, so there is something to
// resume; the first carries the version the run was started with.
const resumeWithVersion = `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

deploy_web:
  salt.function:
    - name: pkg.install
    - tgt: '*'
    - arg:
      - web-{{ pillar.get('version', 'none') }}
    - require:
      - salt: drain
`

// A resume compiled without the override its run was started with ran
// the remaining steps against a different pillar from the one the
// carried steps saw. `orch resume` took no override and the record kept
// none, so it always did, silently (DIVERGENCE 5.201).
func TestAResumeIsHeldToThePillarOverrideItsRunHad(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": resumeWithVersion})
	node := l.enrolled(t, "web1.example")
	var mu sync.Mutex
	var dispatched []string
	failInstall := true
	defer l.answeringAs(t, node, "web1.example", func(fun string) bool {
		mu.Lock()
		defer mu.Unlock()
		dispatched = append(dispatched, fun)
		return !(fun == "pkg.install" && failInstall)
	})()

	override := value.MapOf("version", "1.4.2", "channel", "stable")
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: override,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != OrchFailed {
		t.Fatalf("the first run is %s; it should have failed at deploy_web", first.State)
	}
	if first.PillarDigest == "" || first.PillarDigest == orchNoPillar {
		t.Fatalf("the run did not record its override: %q", first.PillarDigest)
	}
	// `orch show` reads this record, and the override is pillar; the
	// digest stays in the record and out of what an operator is shown.
	if shown := orchJSON(first, true); shown.Has("pillar_digest") || shown.Has("pillar") {
		t.Errorf("orch show prints the override or its digest: %v", shown.Keys())
	}
	mu.Lock()
	failInstall = false
	before := len(dispatched)
	mu.Unlock()

	resume := func(p *value.Map) (*OrchRun, error) {
		return l.server.Orchestrate(context.Background(), OrchRequest{
			Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: p,
			ResumeOf: first.JID, ResumeFrom: "deploy_web",
		})
	}

	for _, tc := range []struct {
		name   string
		pillar *value.Map
		want   string
	}{
		{"no override", nil, "passes none"},
		{"a different value", value.MapOf("version", "1.4.3", "channel", "stable"), "different pillar override"},
	} {
		if _, err := resume(tc.pillar); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a refusal saying %q, got %v", tc.name, tc.want, err)
		}
	}
	mu.Lock()
	if after := dispatched[before:]; len(after) != 0 {
		t.Errorf("a refused resume dispatched %v", after)
	}
	mu.Unlock()

	// The same override, its keys in another order, is the same one.
	second, err := resume(value.MapOf("channel", "stable", "version", "1.4.2"))
	if err != nil {
		t.Fatalf("the same override was refused: %v", err)
	}
	if second.State != OrchComplete {
		t.Fatalf("the resumed run is %s: %+v", second.State, second.Steps)
	}
	if second.PillarDigest != first.PillarDigest {
		t.Errorf("the resumed run recorded %q, the run it resumed %q", second.PillarDigest, first.PillarDigest)
	}
}

// A run with no override cannot be resumed with one: that is a different
// run from the one whose steps are being carried forward.
func TestAResumeCannotAddAPillarOverride(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": resumeWithVersion})
	defer l.answering(t, "web1.example", func(fun string) bool { return fun != "pkg.install" })()
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.PillarDigest != orchNoPillar {
		t.Fatalf("a run with no override recorded %q", first.PillarDigest)
	}
	_, err = l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: value.MapOf("version", "1.4.2"),
		ResumeOf: first.JID, ResumeFrom: "deploy_web",
	})
	if err == nil || !strings.Contains(err.Error(), "no pillar override") {
		t.Fatalf("want a refusal saying the run had no override, got %v", err)
	}
}

// A record written before runs kept the digest cannot say what the run
// had, and resumes as it always did rather than refusing every old run.
func TestAnOldRecordWithNoDigestStillResumes(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": resumeWithVersion})
	defer l.answering(t, "web1.example", nil)()
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: value.MapOf("version", "1.4.2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := l.server.orchStore().Get(first.JID)
	if err != nil {
		t.Fatal(err)
	}
	stored.PillarDigest = ""
	if err := l.server.orchStore().Put(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: first.JID, ResumeFrom: "deploy_web",
	}); err != nil {
		t.Fatalf("an old record was refused: %v", err)
	}
}
