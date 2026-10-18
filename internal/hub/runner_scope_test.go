package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// runnerScopePolicy grants alice runners against the web nodes alone,
// bob the same runners with no target at all, and ed everything.
func runnerScopePolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, _, err := policy.Load([]byte(`
roles:
  webrunners:
    - target: 'web*.example'
      runners: ['pillar.show_pillar', 'key.finger', 'manage.*', 'cache.*', 'mine.get', 'jobs.*', 'event.replay', 'fileserver.envs']
  untargeted:
    - runners: ['pillar.show_pillar', 'manage.status', 'fileserver.envs']
  administrator:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*']
bindings:
  - principal: 'cert:CN=alice'
    roles: ['webrunners']
  - principal: 'cert:CN=bob'
    roles: ['untargeted']
  - principal: 'cert:CN=ed'
    roles: ['administrator']
`), "runner-scope.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func runnerRefused(t *testing.T, client *transport.Client, fun string, args ...string) error {
	t.Helper()
	_, err := client.Runner(context.Background(), transport.RunnerRequest{Fun: fun, Arg: args})
	return err
}

// **A runner reaches only the nodes its rule's target covers** (issue
// 321, M-56). Salt grants a runner with no target and runs it with the
// whole reach of Salt's server; alice, granted pillar.show_pillar against web*,
// read db1's pillar. Now a node named outside the target is refused, an
// expression that reaches outside it is refused, and an estate-wide
// runner needs target '*'.
func TestARunnerIsConfinedToItsRulesTarget(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = runnerScopePolicy(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")
	alice := l.operator(t, "alice")
	ed := l.operator(t, "ed")

	for _, c := range []struct {
		fun  string
		args []string
	}{
		{"pillar.show_pillar", []string{"db1.example"}},
		{"key.finger", []string{"db1.example"}},
		{"cache.clear_grains", []string{"db1.example"}},
		{"mine.get", []string{"*", "network.ip_addrs"}},
		{"mine.get", []string{"db1.example", "network.ip_addrs"}},
		{"cache.clear_all", nil},
		{"jobs.prune", nil},
	} {
		err := runnerRefused(t, alice, c.fun, c.args...)
		if err == nil {
			t.Errorf("alice, scoped to web*, ran %s %v", c.fun, c.args)
		}
	}
	for _, c := range []struct {
		fun  string
		args []string
	}{
		{"pillar.show_pillar", []string{"web1.example"}},
		{"key.finger", []string{"web1.example"}},
		{"mine.get", []string{"web*.example", "network.ip_addrs"}},
		{"fileserver.envs", nil},
	} {
		if err := runnerRefused(t, alice, c.fun, c.args...); err != nil {
			t.Errorf("alice could not run %s %v inside her scope: %v", c.fun, c.args, err)
		}
	}
	if err := runnerRefused(t, alice, "cache.clear_all"); err == nil || !strings.Contains(err.Error(), "target: '*'") {
		t.Errorf("an estate runner's refusal does not say what is needed: %v", err)
	}
	// ed's '*' reaches everything.
	for _, fun := range []string{"pillar.show_pillar", "key.finger"} {
		if err := runnerRefused(t, ed, fun, "db1.example"); err != nil {
			t.Errorf("ed, with target '*', could not run %s db1: %v", fun, err)
		}
	}
	if err := runnerRefused(t, ed, "cache.clear_all"); err != nil {
		t.Errorf("ed could not run cache.clear_all: %v", err)
	}
}

// What a runner lists is cut to the scope, and a rule with no target
// grants the runner and no nodes.
func TestARunnerListsOnlyTheNodesInScope(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = runnerScopePolicy(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")
	alice := l.operator(t, "alice")
	bob := l.operator(t, "bob")
	ed := l.operator(t, "ed")

	nodesIn := func(m *value.Map, key string) []string { return listOf(t, m, key) }
	down := nodesIn(mapOf(t, returned(t, call(t, alice, "manage.status"))), "down")
	if len(down) != 1 || down[0] != "web1.example" {
		t.Errorf("alice's manage.status lists %v; she may see web1 alone", down)
	}
	all := nodesIn(mapOf(t, returned(t, call(t, ed, "manage.status"))), "down")
	if len(all) != 2 {
		t.Errorf("ed's manage.status lists %v", all)
	}
	if got := nodesIn(mapOf(t, returned(t, call(t, bob, "manage.status"))), "down"); len(got) != 0 {
		t.Errorf("bob's rule names no target and manage.status listed %v", got)
	}
	if err := runnerRefused(t, bob, "pillar.show_pillar", "web1.example"); err == nil {
		t.Error("bob's rule names no target and pillar.show_pillar read web1")
	}
	if err := runnerRefused(t, bob, "fileserver.envs"); err != nil {
		t.Errorf("a runner that touches no node needs no target: %v", err)
	}
}

// A job is seen by a scoped caller only if it went to a node in scope,
// and then only that node's part of it.
func TestARunnerShowsOnlyTheJobsInScope(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = runnerScopePolicy(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")
	alice := l.operator(t, "alice")
	ed := l.operator(t, "ed")

	submit := func(tgt, kind string) string {
		t.Helper()
		res, err := ed.Submit(context.Background(), transport.SubmitRequest{Target: tgt, TargetKind: kind, Fun: "test.ping"})
		if err != nil {
			t.Fatal(err)
		}
		return res.JID
	}
	dbOnly := submit("db1.example", "")
	both := submit("web1.example,db1.example", "L")

	listed := mapOf(t, returned(t, call(t, alice, "jobs.list_jobs")))
	if _, ok := listed.Get(dbOnly); ok {
		t.Error("alice's jobs.list_jobs lists a job that went to db1 alone")
	}
	entry, ok := listed.Get(both)
	if !ok {
		t.Fatal("alice's jobs.list_jobs does not list a job that went to web1")
	}
	if nodes := listOf(t, entry.(*value.Map), "nodes"); len(nodes) != 1 || nodes[0] != "web1.example" {
		t.Errorf("the job's nodes, as alice sees them: %v", nodes)
	}
	if res := call(t, alice, "jobs.lookup_jid", dbOnly); res.Success {
		t.Errorf("alice looked up db1's job: %s", res.Return)
	}
	missing := returned(t, call(t, alice, "jobs.missing", both)).([]any)
	for _, n := range missing {
		if value.KeyString(n) == "db1.example" {
			t.Errorf("jobs.missing told alice about db1: %v", missing)
		}
	}
}

// A call that carries a narrowing -- the API's, on an operator's behalf --
// is held to it as well as to the caller's own scope, and can only be
// made smaller by it.
func TestARunnerCallIsHeldToItsNarrowing(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = runnerScopePolicy(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")
	ed := l.operator(t, "ed")
	narrow := []string{"web*.example"}
	run := func(fun string, args ...string) (*transport.RunnerResponse, error) {
		return ed.Runner(context.Background(), transport.RunnerRequest{Fun: fun, Arg: args, Narrow: &narrow})
	}
	if _, err := run("pillar.show_pillar", "db1.example"); err == nil {
		t.Error("ed, narrowed to web*, read db1's pillar")
	}
	if _, err := run("pillar.show_pillar", "web1.example"); err != nil {
		t.Errorf("ed, narrowed to web*, could not read web1's pillar: %v", err)
	}
	if _, err := run("cache.clear_all"); err == nil {
		t.Error("a narrowing short of '*' ran an estate runner")
	}
	res, err := run("manage.status")
	if err != nil {
		t.Fatal(err)
	}
	if down := listOf(t, mapOf(t, returned(t, res)), "down"); len(down) != 1 || down[0] != "web1.example" {
		t.Errorf("manage.status, narrowed to web*, lists %v", down)
	}
	empty := []string{}
	if _, err := ed.Runner(context.Background(), transport.RunnerRequest{Fun: "pillar.show_pillar", Arg: []string{"web1.example"}, Narrow: &empty}); err == nil {
		t.Error("an empty narrowing reached a node")
	}
}
