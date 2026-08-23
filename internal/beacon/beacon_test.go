package beacon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

func parseConfig(t *testing.T, src string) []*Instance {
	t.Helper()
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "node.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	root, ok := doc.(*value.Map)
	if !ok {
		t.Fatal("the fixture is not a mapping")
	}
	raw, _ := root.Get("beacons")
	instances, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return instances
}

// The schema is Salt's, including the list-of-single-key-mappings form
// that an existing beacon configuration is written in: the items merge
// into one configuration with the control settings taken out.
func TestSaltsBeaconSchemaParses(t *testing.T) {
	instances := parseConfig(t, `
beacons:
  diskusage:
    - /: 85%
    - /var: 90%
    - interval: 60
  service:
    - services:
        nginx:
          onchangeonly: True
          delay: 10
      disable_during_state_run: True
    - interval: 5
`)
	if len(instances) != 2 {
		t.Fatalf("parsed %d beacons", len(instances))
	}

	disk := instances[0]
	if disk.Name != "diskusage" || disk.Interval != time.Minute {
		t.Errorf("diskusage parsed as %+v", disk)
	}
	if got := disk.StringArg("/", ""); got != "85%" {
		t.Errorf("the / threshold is %q", got)
	}
	if got := disk.StringArg("/var", ""); got != "90%" {
		t.Errorf("the /var threshold is %q", got)
	}
	// A control is not part of the beacon's own configuration.
	if _, ok := disk.Arg("interval"); ok {
		t.Error("`interval` reached the beacon's configuration")
	}

	svc := instances[1]
	if !svc.DisableDuringStateRun || svc.Interval != 5*time.Second {
		t.Errorf("service parsed as %+v", svc)
	}
	if _, ok := svc.Arg("services"); !ok {
		t.Error("the services mapping did not survive the merge")
	}
}

// A configuration that will not parse must stop the node rather than
// leaving a watcher silently absent.
func TestABadBeaconConfigurationIsRefused(t *testing.T) {
	cases := map[string]string{
		"not a mapping":       "beacons:\n  - diskusage\n",
		"an item that is not": "beacons:\n  diskusage:\n    - 85\n",
		"a bad interval":      "beacons:\n  diskusage:\n    - interval: 'soon'\n",
	}
	for name, src := range cases {
		doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "node.yaml"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := doc.(*value.Map).Get("beacons")
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// recorder collects what an engine sent.
type recorder struct {
	mu   sync.Mutex
	sent []recorded
	fire chan struct{}
}

type recorded struct {
	tag  string
	data map[string]any
}

func newRecorder() *recorder { return &recorder{fire: make(chan struct{}, 64)} }

func (r *recorder) Send(tag string, data map[string]any) error {
	r.mu.Lock()
	r.sent = append(r.sent, recorded{tag: tag, data: data})
	r.mu.Unlock()
	select {
	case r.fire <- struct{}{}:
	default:
	}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *recorder) tags() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.sent))
	for i, s := range r.sent {
		out[i] = s.tag
	}
	return out
}

// testEngine builds an engine over one beacon written for the test.
func testEngine(t *testing.T, in *Instance, fn Func) (*Engine, *recorder) {
	t.Helper()
	reg := NewRegistry()
	reg.Add(Module{Name: in.Name, Doc: "a beacon for a test", Fn: fn})
	rec := newRecorder()
	return &Engine{
		Registry:  reg,
		Instances: []*Instance{in},
		Context:   func() *exec.Context { return &exec.Context{Ctx: context.Background()} },
		Send:      rec.Send,
	}, rec
}

func instance(name string) *Instance {
	return &Instance{
		Name:    name,
		Args:    value.NewMap(0),
		last:    map[string]string{},
		holding: map[string]time.Time{},
	}
}

func waitFor(t *testing.T, rec *recorder, n int, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for rec.count() < n {
		select {
		case <-rec.fire:
		case <-deadline:
			t.Fatalf("%d of %d events arrived: %v", rec.count(), n, rec.tags())
		}
	}
}

// The tag a beacon fires under is its own name plus whatever it said,
// so a reactor can match one filesystem rather than all of them.
func TestABeaconFiresUnderItsOwnName(t *testing.T) {
	in := instance("diskusage")
	in.Interval = 10 * time.Millisecond
	in.CoalesceWindow = time.Millisecond
	e, rec := testEngine(t, in, func(*exec.Context, *Instance) ([]Event, error) {
		return []Event{{Suffix: "var", Data: map[string]any{"percent_used": 91.0}}}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	waitFor(t, rec, 1, 3*time.Second)
	if got := rec.tags()[0]; got != "diskusage/var" {
		t.Errorf("the beacon fired under %q", got)
	}
}

// `onchangeonly` is what stops a beacon that reports a fact from
// reporting it every interval for ever.
func TestOnChangeOnlySuppressesTheSameEvent(t *testing.T) {
	in := instance("service")
	in.Interval = 5 * time.Millisecond
	in.CoalesceWindow = time.Millisecond
	in.OnChangeOnly = true

	var polls int
	var mu sync.Mutex
	e, rec := testEngine(t, in, func(*exec.Context, *Instance) ([]Event, error) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		running := polls < 5
		return []Event{{Suffix: "nginx", Data: map[string]any{"running": running}}}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// The state changes once, so exactly two events: the first reading
	// and the change.
	waitFor(t, rec, 2, 3*time.Second)
	time.Sleep(100 * time.Millisecond)
	if n := rec.count(); n != 2 {
		t.Errorf("%d events for one change: %v", n, rec.tags())
	}
}

// Identical events inside the coalescing window become one carrying a
// count. Loss is never silent, and neither is collapsing.
func TestIdenticalEventsCoalesceWithACount(t *testing.T) {
	q := newCoalescingQueue(10)
	now := time.Now()
	for i := 0; i < 4; i++ {
		q.push(queued{tag: "t", key: "k", at: now, window: time.Second, data: map[string]any{"a": 1}})
	}
	if q.Len() != 1 {
		t.Fatalf("four identical events made %d entries", q.Len())
	}
	// A different payload is a different event.
	q.push(queued{tag: "t", key: "other", at: now, window: time.Second})
	if q.Len() != 2 {
		t.Fatalf("a different payload made %d entries", q.Len())
	}

	item, ok := q.pop(context.Background(), func() time.Time { return now.Add(2 * time.Second) })
	if !ok {
		t.Fatal("nothing came off the queue")
	}
	if item.count != 4 {
		t.Errorf("the collapsed event stands for %d", item.count)
	}
}

// The queue is bounded and drops the oldest, reporting how many.
func TestTheBeaconQueueDropsTheOldest(t *testing.T) {
	q := newCoalescingQueue(2)
	now := time.Now()
	q.push(queued{tag: "a", key: "a", at: now})
	q.push(queued{tag: "b", key: "b", at: now})
	if dropped := q.push(queued{tag: "c", key: "c", at: now}); dropped != 1 {
		t.Errorf("a full queue dropped %d", dropped)
	}
	item, _ := q.pop(context.Background(), func() time.Time { return now.Add(time.Second) })
	if item.tag != "b" {
		t.Errorf("the oldest kept was %q", item.tag)
	}
}

// `disable_during_state_run` is how a state run is stopped from firing
// the beacon that triggers the state run. SPEC 16.3.
func TestABeaconIsSuppressedDuringAStateRun(t *testing.T) {
	in := instance("filechanges")
	in.Interval = 5 * time.Millisecond
	in.CoalesceWindow = time.Millisecond
	in.DisableDuringStateRun = true

	e, rec := testEngine(t, in, func(*exec.Context, *Instance) ([]Event, error) {
		return []Event{{Suffix: "etc/thing", Data: map[string]any{"n": time.Now().UnixNano()}}}, nil
	})
	running := true
	var mu sync.Mutex
	e.StateRunning = func() bool { mu.Lock(); defer mu.Unlock(); return running }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	time.Sleep(100 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Fatalf("%d events fired during a state run", n)
	}
	mu.Lock()
	running = false
	mu.Unlock()
	waitFor(t, rec, 1, 3*time.Second)
}

// A beacon that cannot read what it watches says so. Silence is what a
// healthy system looks like, and a watcher that has stopped watching
// must not look like one.
func TestABeaconThatFailsFiresAnError(t *testing.T) {
	in := instance("diskusage")
	in.Interval = 5 * time.Millisecond
	in.CoalesceWindow = time.Millisecond
	e, rec := testEngine(t, in, func(*exec.Context, *Instance) ([]Event, error) {
		return nil, errRead
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	waitFor(t, rec, 1, 3*time.Second)
	if got := rec.tags()[0]; got != "diskusage/error" {
		t.Errorf("a failing beacon fired %q", got)
	}
}

// A configuration naming a beacon this build does not have, or one that
// is declared and not built, stops the node rather than starting a
// watcher that is silently absent.
func TestAnUnknownOrUnbuiltBeaconIsRefused(t *testing.T) {
	reg := New()
	e := &Engine{
		Registry:  reg,
		Instances: []*Instance{instance("nosuchbeacon")},
		Context:   func() *exec.Context { return &exec.Context{} },
		Send:      func(string, map[string]any) error { return nil },
	}
	if err := e.Run(context.Background()); err == nil {
		t.Error("an unknown beacon was accepted")
	}

	e.Instances = []*Instance{instance("inotify")}
	err := e.Run(context.Background())
	if err == nil {
		t.Fatal("a declared-and-unbuilt beacon was accepted")
	}
	if !contains(err.Error(), "not built") {
		t.Errorf("the refusal says %q", err)
	}
}

// The whole SPEC 16.2 inventory is declared, built or pending, so that
// a name missing from the registry cannot be mistaken for a typo.
func TestTheBeaconRegistryIsWellFormed(t *testing.T) {
	reg := New()
	names := reg.Names()
	if len(names) < 20 {
		t.Fatalf("the registry holds %d beacons; SPEC 16.2 names more", len(names))
	}
	for _, name := range names {
		mod, _ := reg.Lookup(name)
		if mod.Doc == "" {
			t.Errorf("%s has no documentation", name)
		}
		if mod.Fn == nil && mod.Pending == "" {
			t.Errorf("%s is neither built nor pending", name)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var errRead = errRepr("the filesystem could not be read")

type errRepr string

func (e errRepr) Error() string { return string(e) }

// A tag has to be matchable. The root filesystem trims to nothing, and
// a beacon whose tag ends at its own name cannot be reached by
// `diskusage/**` -- which is the glob a reactor watching every
// filesystem writes.
func TestTheRootFilesystemStillHasATagSuffix(t *testing.T) {
	cases := map[string]string{
		"/":            "root",
		"/var":         "var",
		"/var/log":     "var/log",
		"relative/one": "relative/one",
	}
	for in, want := range cases {
		if got := pathSuffix(in); got != want {
			t.Errorf("pathSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
