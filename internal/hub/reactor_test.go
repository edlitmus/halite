package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

func parseReactorConfig(t *testing.T, src string) []ReactorEntry {
	t.Helper()
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "hub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	root, ok := doc.(*value.Map)
	if !ok {
		t.Fatalf("the fixture is not a mapping")
	}
	raw, _ := root.Get("reactor")
	entries, err := ParseReactors(raw)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// Salt's form is a list of single-key mappings, and an existing
// configuration is written that way. The extended form is what carries
// the SPEC 18.2 controls, which the first has nowhere to put.
func TestBothReactorConfigurationFormsParse(t *testing.T) {
	entries := parseReactorConfig(t, `
reactor:
  - 'halite/node/*/start':
      - /srv/reactor/start.sls
  - tag: 'halite/beacon/**'
    sls: /srv/reactor/beacon.sls
    principal: 'reactor:beacons'
    debounce: 5s
    dedupe_window: 30s
    dedupe_key: 'path'
    rate_limit: 600/m
`)
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries", len(entries))
	}
	if entries[0].Tag != "halite/node/*/start" || len(entries[0].SLS) != 1 {
		t.Errorf("the Salt form parsed as %+v", entries[0])
	}
	// An entry that names no principal gets the restricted default,
	// which the policy grants nothing until someone binds it.
	if entries[0].Principal != DefaultReactorPrincipal {
		t.Errorf("the default principal is %q", entries[0].Principal)
	}

	e := entries[1]
	if e.Principal != "reactor:beacons" || e.Debounce != 5*time.Second ||
		e.DedupeWindow != 30*time.Second || e.DedupeKey != "path" {
		t.Errorf("the extended form parsed as %+v", e)
	}
	if e.RateLimit != 10 || e.RateBurst != 600 {
		t.Errorf("600/m came to %v per second, burst %d", e.RateLimit, e.RateBurst)
	}
	if len(e.SLS) != 1 || e.SLS[0] != "/srv/reactor/beacon.sls" {
		t.Errorf("one file written as a bare string parsed as %v", e.SLS)
	}
}

// A configuration that will not parse must stop the hub, not start one
// with a reactor that reacts to nothing.
func TestABadReactorConfigurationIsRefused(t *testing.T) {
	cases := map[string]string{
		"two keys in the Salt form": "reactor:\n  - 'a/b': [x.sls]\n    'c/d': [y.sls]\n",
		"no files":                  "reactor:\n  - tag: 'a/b'\n",
		"not a list":                "reactor:\n  tag: 'a/b'\n",
		"empty tag":                 "reactor:\n  - tag: ''\n    sls: [x.sls]\n",
		"a rate that is not one":    "reactor:\n  - tag: 'a/b'\n    sls: [x.sls]\n    rate_limit: 'lots'\n",
	}
	for name, src := range cases {
		doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "hub.yaml"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := doc.(*value.Map).Get("reactor")
		if _, err := ParseReactors(raw); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// reactorLab is a hub with a bus, a job cache, and a reaction on disk.
func reactorLab(t *testing.T, reactions map[string]string, config string) (*lab, *Reactor, string) {
	t.Helper()
	l := newLab(t).withJobs(t).withEvents(t)
	dir := t.TempDir()
	for name, body := range reactions {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := strings.ReplaceAll(config, "$DIR", dir)
	l.server.Reactors = parseReactorConfig(t, src)

	r := &Reactor{
		Server:  l.server,
		Entries: l.server.Reactors,
		Workers: 2,
		Now:     l.server.now,
	}
	return l, r, dir
}

// A reaction fires the job its SLS declares, targeted where the SLS
// says, with the event's payload available to the template.
func TestAnEventFiresTheReactionItsTagNames(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"nginx.sls": `
{% set node = data['id'] %}

restart_nginx:
  local.service.restart:
    - tgt: {{ node }}
    - arg:
      - nginx
`,
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/nginx.sls
    principal: 'cert:CN=ed'
`)
	l.enrolled(t, "web1.example")

	done := make(chan []ReactionResult, 1)
	r.Handled = func(tag string, res []ReactionResult) { done <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	r.Offer(&eventbus.Event{
		Tag:  "halite/beacon/web1.example/inotify/etc/nginx/nginx.conf",
		Node: "web1.example",
		Data: map[string]any{"id": "web1.example", "path": "/etc/nginx/nginx.conf"},
	})

	var results []ReactionResult
	select {
	case results = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reaction never ran")
	}
	if len(results) != 1 {
		t.Fatalf("the reaction produced %d results", len(results))
	}
	res := results[0]
	if !res.Succeeded() {
		t.Fatalf("the reaction failed: %s", res.Error)
	}
	if res.Reaction.Kind != ReactLocal || res.Reaction.Fun != "service.restart" {
		t.Errorf("the reaction parsed as %+v", res.Reaction)
	}
	if res.JID == "" || len(res.Nodes) != 1 || res.Nodes[0] != "web1.example" {
		t.Errorf("the job reached %v under %q", res.Nodes, res.JID)
	}

	// The job carries the chain the event belonged to, so what a
	// reaction caused can be traced back to what caused the reaction.
	j, err := l.server.Jobs.Get(jobIDOf(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if j.Correlation == "" {
		t.Error("the dispatched job records no causality chain")
	}
	if j.Submitter != "cert:CN=ed" {
		t.Errorf("the job was submitted as %q", j.Submitter)
	}
}

// SPEC 18.3's departure from Salt: a reactor is subject to the policy
// exactly like a human caller. In Salt it runs with the control plane's
// full privilege, so a node that can fire the right event can cause
// arbitrary fleet-wide execution.
func TestAReactionIsRefusedByThePolicyLikeAnyCaller(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"anything.sls": `
run_it:
  local.cmd.run:
    - tgt: '*'
    - arg:
      - /usr/bin/whatever
`,
	}, `
reactor:
  - tag: 'halite/node/**'
    sls:
      - $DIR/anything.sls
`)
	l.enrolled(t, "web1.example")

	done := make(chan []ReactionResult, 1)
	r.Handled = func(tag string, res []ReactionResult) { done <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	r.Offer(&eventbus.Event{Tag: "halite/node/web1.example/start", Node: "web1.example"})

	select {
	case results := <-done:
		if len(results) != 1 || results[0].Succeeded() {
			t.Fatalf("an unbound reactor principal dispatched a job: %+v", results)
		}
		if !strings.Contains(results[0].Error, DefaultReactorPrincipal) {
			t.Errorf("the refusal should name the principal: %s", results[0].Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reaction never ran")
	}

	// And the refusal is on the bus, because a reaction that did not
	// fire is invisible otherwise and the event does not come again.
	if !busHas(t, l, "halite/reactor/error") {
		t.Error("the refusal was not recorded on the bus")
	}
}

// A reaction that will not render must say so on the bus. Salt fails
// this silently, and the event that would have triggered it is gone.
func TestAReactionThatWillNotRenderIsReported(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"broken.sls": "{% set x = %}\n",
	}, `
reactor:
  - tag: 'halite/node/**'
    sls:
      - $DIR/broken.sls
`)
	done := make(chan []ReactionResult, 1)
	r.Handled = func(tag string, res []ReactionResult) { done <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	r.Offer(&eventbus.Event{Tag: "halite/node/web1.example/start", Node: "web1.example"})
	select {
	case results := <-done:
		if len(results) != 1 || results[0].Succeeded() {
			t.Fatalf("a reaction that will not render reported success: %+v", results)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reaction never ran")
	}
	if !busHas(t, l, "halite/reactor/error") {
		t.Error("the failure was not recorded on the bus")
	}
}

// A file that changes in a loop fires a beacon that fires a reactor
// that changes the file. SPEC 16.3 breaks the chain rather than letting
// the estate do it.
func TestALongCausalityChainIsBroken(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"noop.sls": `
touch_it:
  local.test.ping:
    - tgt: '*'
`,
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/noop.sls
    principal: 'cert:CN=ed'
`)
	l.enrolled(t, "web1.example")
	r.MaxDepth = 3

	handled := make(chan struct{}, 32)
	r.Handled = func(string, []ReactionResult) { handled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	// Five events in one chain; only the first three may react.
	for i := 0; i < 5; i++ {
		r.Offer(&eventbus.Event{
			Tag:         "halite/beacon/web1.example/inotify/etc/thing",
			Node:        "web1.example",
			Correlation: "chain-1",
		})
	}
	ran := 0
	deadline := time.After(3 * time.Second)
	for ran < 3 {
		select {
		case <-handled:
			ran++
		case <-deadline:
			t.Fatalf("only %d of 3 reactions ran", ran)
		}
	}
	select {
	case <-handled:
		t.Error("a chain longer than max_causality_depth kept reacting")
	case <-time.After(300 * time.Millisecond):
	}
	if !busHas(t, l, "halite/reactor/loop") {
		t.Error("breaking the chain was not recorded on the bus")
	}
}

// Deduplication drops a repeat inside the window, which is what stops
// one flapping source from causing the same reaction fifty times.
func TestDeduplicationDropsARepeatInsideTheWindow(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"noop.sls": "ping:\n  local.test.ping:\n    - tgt: '*'\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/noop.sls
    principal: 'cert:CN=ed'
    dedupe_window: 1h
    dedupe_key: 'path'
`)
	l.enrolled(t, "web1.example")

	handled := make(chan struct{}, 16)
	r.Handled = func(string, []ReactionResult) { handled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	for i := 0; i < 4; i++ {
		r.Offer(&eventbus.Event{
			Tag:  "halite/beacon/web1.example/inotify/etc/thing",
			Node: "web1.example",
			Data: map[string]any{"path": "/etc/thing"},
		})
	}
	// A different payload is a different event and is not a duplicate.
	r.Offer(&eventbus.Event{
		Tag:  "halite/beacon/web1.example/inotify/etc/other",
		Node: "web1.example",
		Data: map[string]any{"path": "/etc/other"},
	})

	ran := 0
	deadline := time.After(3 * time.Second)
	for ran < 2 {
		select {
		case <-handled:
			ran++
		case <-deadline:
			t.Fatalf("%d reactions ran; two distinct payloads should give two", ran)
		}
	}
	select {
	case <-handled:
		t.Error("a duplicate inside the window reacted")
	case <-time.After(300 * time.Millisecond):
	}
}

// The queue is bounded and drops the oldest, reporting the count. A
// channel would block the bus reader instead, which turns a burst into
// a backlog -- the Salt failure SPEC 18.2 names.
func TestTheQueueDropsTheOldestAndSaysHowMany(t *testing.T) {
	q := newBoundedQueue(2)
	if dropped := q.push(reactorJob{entry: ReactorEntry{Tag: "a"}}); dropped != 0 {
		t.Errorf("the first push dropped %d", dropped)
	}
	q.push(reactorJob{entry: ReactorEntry{Tag: "b"}})
	if dropped := q.push(reactorJob{entry: ReactorEntry{Tag: "c"}}); dropped != 1 {
		t.Errorf("a full queue dropped %d", dropped)
	}
	if q.Len() != 2 {
		t.Errorf("the queue holds %d", q.Len())
	}
	// The oldest went, not the newest: the newest is the current state
	// of whatever fired, and dropping it would leave the estate acting
	// on a stale one.
	got, ok := q.pop(context.Background())
	if !ok || got.entry.Tag != "b" {
		t.Errorf("popped %+v", got.entry.Tag)
	}
}

// `reactor.test` answers what an event would cause, without waiting for
// one and without dispatching anything.
func TestReactorTestPlansWithoutDispatching(t *testing.T) {
	l, _, _ := reactorLab(t, map[string]string{
		"nginx.sls": `
restart_nginx:
  local.service.restart:
    - tgt: {{ data['id'] }}
    - arg:
      - nginx
`,
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/nginx.sls
`)
	l.server.Policy = labPolicy(t)
	op := l.operator(t, "ed")

	res, err := op.Runner(context.Background(), runnerRequest("reactor.test", map[string]any{
		"tag":  "halite/beacon/web1.example/inotify/etc/nginx",
		"node": "web1.example",
		"data": map[string]any{"id": "web1.example"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	out := mapOf(t, returned(t, res))
	raw, _ := out.Get("reactions")
	list, ok := raw.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("the plan is %v", raw)
	}
	plan := mapOf(t, list[0])
	if fun, _ := plan.Get("fun"); fun != "service.restart" {
		t.Errorf("the plan names %v", fun)
	}
	if tgt, _ := plan.Get("tgt"); tgt != "web1.example" {
		t.Errorf("the target rendered as %v", tgt)
	}
	// The policy decision is part of the answer: "it renders" and "it
	// is permitted" are different questions.
	if auth, _ := plan.Get("authorized"); !strings.Contains(value.KeyString(auth), "denied") {
		t.Errorf("the default reactor principal should be denied: %v", auth)
	}

	// Nothing was dispatched.
	jobs, err := l.server.Jobs.List(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.Fun == "service.restart" {
			t.Error("reactor.test dispatched the reaction it was asked to describe")
		}
	}
}

// A reaction SLS that is not one has to say what it should have been.
func TestAMalformedReactionSaysWhatIsWrong(t *testing.T) {
	cases := map[string]string{
		"not a reaction type": "a:\n  nonsense.thing:\n    - tgt: '*'\n",
		"no type at all":      "a:\n  restart:\n    - tgt: '*'\n",
		"two reactions in one": "a:\n  local.test.ping:\n    - tgt: '*'\n" +
			"  runner.test.ping: []\n",
		"a local with no target": "a:\n  local.test.ping: []\n",
		"an unknown argument":    "a:\n  local.test.ping:\n    - nonsense: 1\n",
	}
	for name, src := range cases {
		doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "r.sls"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		m, ok := doc.(*value.Map)
		if !ok {
			t.Fatalf("%s: the fixture is not a mapping", name)
		}
		if _, err := parseReactions(m, "r.sls"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A runner reaction reaches the runner registry as its principal.
func TestARunnerReactionCallsARunner(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"audit.sls": `
record_it:
  runner.event.send:
    - args:
        tag: halite/audit/node_started
        data:
          node: {{ data['id'] }}
`,
	}, `
reactor:
  - tag: 'halite/node/*/start'
    sls:
      - $DIR/audit.sls
    principal: 'cert:CN=ed'
`)
	l.server.Policy = labPolicy(t)

	done := make(chan []ReactionResult, 1)
	r.Handled = func(tag string, res []ReactionResult) { done <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	r.Offer(&eventbus.Event{
		Tag: "halite/node/web1.example/start", Node: "web1.example",
		Data: map[string]any{"id": "web1.example"},
	})
	select {
	case results := <-done:
		if len(results) != 1 || !results[0].Succeeded() {
			t.Fatalf("the runner reaction did not run: %+v", results)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reaction never ran")
	}
	if !busHas(t, l, "halite/audit/node_started") {
		t.Error("the runner reaction did not put its event on the bus")
	}
}

// busHas reports whether a tag is on the bus.
func busHas(t *testing.T, l *lab, tag string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, _, err := l.server.Events.Read(eventbus.Earliest, []string{tag + "**"}, 500)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// A reactor restart is lossless, which is the claim SPEC 17.2 makes for
// a durable bus. It is lossless because the reactor writes down where it
// had read to and resumes there, rather than starting at the end and
// leaving a gap nobody notices.
func TestARestartedReactorResumesFromWhereItStopped(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"audit.sls": `
record_it:
  runner.event.send:
    - args:
        tag: halite/audit/saw_it
`,
	}, `
reactor:
  - tag: 'halite/node/*/start'
    sls:
      - $DIR/audit.sls
    principal: 'cert:CN=ed'
`)
	l.server.Policy = labPolicy(t)
	r.OffsetFile = filepath.Join(t.TempDir(), "reactor.offset")

	// Three events land before the reactor ever runs. A reactor that
	// started at the end would miss all three.
	for i := 0; i < 3; i++ {
		l.server.emit("halite/node/web1.example/start", "web1.example", map[string]any{"n": int64(i)})
	}

	handled := make(chan struct{}, 8)
	r.Handled = func(string, []ReactionResult) { handled <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The first start has no offset file, so it begins at the end and
	// sees nothing -- which is right: reacting to a month of history on
	// first boot is worse than missing what happened before there was a
	// reactor.
	if got := r.readOffset(); got != eventbus.Latest {
		t.Errorf("a reactor with no record starts at %q", got)
	}

	// Now it has one, pointing at the beginning.
	r.writeOffset(eventbus.Earliest)
	go func() {
		if err := r.Run(ctx); err != nil {
			t.Errorf("the reactor stopped: %v", err)
		}
	}()

	seen := 0
	deadline := time.After(5 * time.Second)
	for seen < 3 {
		select {
		case <-handled:
			seen++
		case <-deadline:
			t.Fatalf("the reactor caught up with %d of 3 events", seen)
		}
	}

	// And it has written down where it got to, so the next start does
	// not do this again.
	if got := r.readOffset(); got == eventbus.Earliest || got == eventbus.Latest {
		t.Errorf("the reactor recorded its position as %q", got)
	}
}
