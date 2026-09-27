package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// The fifteen states that `unconformed` described as applying "to this
// node's own configuration, so a case needs its roots redirected first".
//
// Redirecting them turned out to be less than it sounded: the node's
// configuration reaches a module through hooks on `exec.Context` —
// `SaveConfig`, `LoadConfig`, `ReloadConfig`, `Beacons`, `Schedule`,
// `Events`, `SyncExtensions` — so a test supplies them and points them at a
// temporary directory. DIVERGENCE 5.157.
//
// # Why the real collector, and not a fake one
//
// `nodeRoots` below is a miniature of `cmd/halite-node`, and a miniature
// that disagrees with the original is a defect generator rather than a test.
// So the part where a disagreement would matter most — *which file wins when
// two of them name the same grain* — is not reimplemented: `reload` calls
// the real `grains.Collect` with the options the real node passes it. What
// is reimplemented is only the writing, and that is one filename in one
// directory.
//
// The remaining gap is stated rather than papered over: this is
// `cmd/halite-node/runtimeconfig.go`'s behaviour written a second time, and
// the two could drift. The filename is the thing to watch —
// `99-runtime.yaml`, whose number is load-bearing.

// runtimeFragment is the file a node writes its own changes to. The real
// one is `runtimeFile` in cmd/halite-node, and the number matters: it
// orders this file last within its directory.
const runtimeFragment = "99-runtime.yaml"

// nodeRoots stands in for the node's own configuration tree.
type nodeRoots struct {
	t    *testing.T
	root string
	// staticGrains is the operator's `grains` file, SPEC 14.2's static
	// file. Empty means the node has none, which is the common case.
	staticGrains string
	// extra is the `grains:` block from the configuration.
	extra *value.Map
	// collected is what the node currently believes its grains are.
	collected *value.Map
	watcher   *fakeWatcher
	events    *recordingEventSender
	// sync models a file server that has bundles to fetch once.
	syncFetched bool
	saved       int
	reloaded    int
}

func newNodeRoots(t *testing.T) *nodeRoots {
	t.Helper()
	n := &nodeRoots{
		t:       t,
		root:    t.TempDir(),
		extra:   value.NewMap(0),
		watcher: newFakeWatcher(),
		events:  &recordingEventSender{},
	}
	n.reload()
	return n
}

// dir is where a kind's fragments live, matching the real node's
// runtimeDir: `beacons.d`, `schedule.d`, `grains.d`.
func (n *nodeRoots) dir(kind string) string {
	return filepath.Join(n.root, kind+".d")
}

func (n *nodeRoots) fragment(kind string) string {
	return filepath.Join(n.dir(kind), runtimeFragment)
}

// reload re-collects the grains through the real collector, with the
// options `cmd/halite-node/grainopts.go` builds.
func (n *nodeRoots) reload() {
	opts := grains.Options{
		NodeID:    "conformance.node",
		GrainsDir: n.dir("grains"),
		Extra:     n.extra,
	}
	if n.staticGrains != "" {
		opts.StaticFile = n.staticGrains
	}
	collected, _ := grains.Collect(opts)
	n.collected = collected
}

func (n *nodeRoots) save(kind string, running *value.Map) (string, error) {
	if err := os.MkdirAll(n.dir(kind), 0o755); err != nil {
		return "", err
	}
	path := n.fragment(kind)
	body := "# Written by a test standing in for halite-node.\n" +
		yaml.Encode(running, yaml.EncodeOptions{Indent: 2})
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	n.saved++
	return path, nil
}

func (n *nodeRoots) load(kind string) (*value.Map, error) {
	raw, err := os.ReadFile(n.fragment(kind))
	if os.IsNotExist(err) {
		return value.NewMap(0), nil
	}
	if err != nil {
		return nil, err
	}
	parsed, _, err := yaml.Parse(raw, yaml.DefaultOptions(n.fragment(kind)))
	if err != nil {
		return nil, err
	}
	if m, ok := parsed.(*value.Map); ok {
		return m, nil
	}
	return value.NewMap(0), nil
}

func (n *nodeRoots) context(test bool) *exec.Context {
	c := newCtx(test)
	c.Grains = n.collected
	c.Beacons = n.watcher
	c.Schedule = n.watcher
	c.Events = n.events
	c.SaveConfig = n.save
	c.LoadConfig = n.load
	c.ReloadConfig = func(kind string) error {
		n.reloaded++
		if kind == "grains" {
			n.reload()
		}
		return nil
	}
	c.SyncExtensions = func(kinds []string) (any, error) {
		// A file server with one bundle, already fetched after the first
		// call: the first sync brings it down and the second finds it
		// there, which is what a real one reports.
		changed := !n.syncFetched
		n.syncFetched = true
		return value.MapOf(
			"changed", changed,
			"extensions", []any{value.MapOf("name", "example", "kinds", kindList(kinds))},
		), nil
	}
	return c
}

// grainValue reads a collected grain, for a probe.
func (n *nodeRoots) grainValue(name string) string {
	v, ok := n.collected.Get(name)
	if !ok {
		return "absent"
	}
	if v == nil {
		return "null"
	}
	return value.KeyString(v)
}

// recordingEventSender counts what was sent, so a probe can show that test
// mode sent nothing.
type recordingEventSender struct{ sent []string }

func (r *recordingEventSender) Send(tag string, data map[string]any) error {
	r.sent = append(r.sent, tag)
	return nil
}

// A grain the static file names cannot be set by this state, and run two
// says so instead of reporting the change again.
//
// `grains_state.go` said, twice, that the file this state writes is "merged
// last precisely so that a runtime change beats the file it was made
// against". True within `grains.d`; false against the static grains file of
// SPEC 14.2, which `grains.Collect` merges after it:
//
//	core facts -> the `grains:` config block -> grains.d -> the static file
//
// Measured before the fix, with `role: db` in the static file and
// `grains.present: role: web` in the tree -- the same answer for ever, with
// the grain never changing:
//
//	first:  succeeded / role was set to web in .../grains.d/99-runtime.yaml.
//	second: succeeded / role was set to web in .../grains.d/99-runtime.yaml.
//
// A state that cannot converge is worse than one that fails. The first run
// still writes, because it has no way to know; the second reads back its own
// file, finds what it wrote, sees the node still reporting the old value, and
// refuses with both names in the message.
func TestGrainsPresentSaysSoWhenItsWriteCannotTakeEffect(t *testing.T) {
	r := New()
	n := newNodeRoots(t)
	n.staticGrains = filepath.Join(n.root, "grains")
	if err := os.WriteFile(n.staticGrains, []byte("role: db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n.reload()
	if got := n.grainValue("role"); got != "db" {
		t.Fatalf("the static file should decide before this state runs: role = %q", got)
	}

	args := func() *value.Map { return value.MapOf("name", "role", "value", "web") }
	first, err := r.States.Call(n.context(false), "grains.present", args())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Succeeded() {
		t.Fatalf("the first run has no way to know, so it writes and reports: %+v", first)
	}

	second, err := r.States.Call(n.context(false), "grains.present", args())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Failed() {
		t.Errorf("the second run reported %q with the grain still %q, which is a change "+
			"that did not happen and will be reported on every run: %v",
			second.ResultString(), n.grainValue("role"), second.Comment)
	}
	if second.HasChanges() {
		t.Errorf("a refusal should report no changes: %v", second.Changes)
	}
	// The message is the whole value of the refusal, so it is checked.
	for _, want := range []string{"role", "static grains file", "cannot take effect"} {
		if !strings.Contains(second.Comment, want) {
			t.Errorf("the refusal does not mention %q, and an operator has to be told "+
				"which file to edit:\n%s", want, second.Comment)
		}
	}

	// And a third run says the same thing rather than drifting into some
	// other answer, because an operator who ignores it once will see it
	// again.
	third, err := r.States.Call(n.context(false), "grains.present", args())
	if err != nil {
		t.Fatal(err)
	}
	if third.Comment != second.Comment {
		t.Errorf("the third run said something else:\n%s\n%s", second.Comment, third.Comment)
	}
}

// The same for `absent`, whose null is written to the same losing file.
func TestGrainsAbsentSaysSoWhenItsNullCannotTakeEffect(t *testing.T) {
	r := New()
	n := newNodeRoots(t)
	n.staticGrains = filepath.Join(n.root, "grains")
	if err := os.WriteFile(n.staticGrains, []byte("role: db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n.reload()

	args := func() *value.Map { return value.MapOf("name", "role") }
	if _, err := r.States.Call(n.context(false), "grains.absent", args()); err != nil {
		t.Fatal(err)
	}
	second, err := r.States.Call(n.context(false), "grains.absent", args())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Failed() {
		t.Errorf("the second run reported %q with the grain still %q: %v",
			second.ResultString(), n.grainValue("role"), second.Comment)
	}
}

// nodeConformanceCases builds the cases for the fifteen states that apply to
// the node's own configuration.
//
// Each gets a fresh `nodeRoots`, because the harness runs a case's Setup
// between its phases and two cases sharing a node would see each other's
// writes. The cost is a temporary directory per case, which is nothing beside
// a grain collection that reads this machine's hardware -- and that is the
// slow part, so it is worth knowing that is where the time goes.
func nodeConformanceCases(t *testing.T) []conformanceCase {
	t.Helper()
	var cases []conformanceCase

	// ---- grains ----
	//
	// No static grains file: this is the ordinary node, where the file this
	// state writes is the last word. The node that has one is the subject of
	// the two tests above, which the harness could not have expressed --
	// its answer would have been "does not converge", without the reason.
	present := newNodeRoots(t)
	cases = append(cases, conformanceCase{
		Context: present.context,
		Conformance: states.Conformance{
			Name:  "grains.present",
			Args:  value.MapOf("name", "role", "value", "web"),
			Probe: func() (string, error) { return present.grainValue("role"), nil },
			Setup: func() error {
				if err := os.RemoveAll(present.dir("grains")); err != nil {
					return err
				}
				present.reload()
				return nil
			},
		},
	})

	// `absent` needs a grain to clear, and it comes from the `grains:`
	// configuration block rather than from a file this state owns -- which
	// is the realistic shape, and the one where the null it writes has to
	// mask something.
	absent := newNodeRoots(t)
	absent.extra = value.MapOf("node_exporter", "9100")
	absent.reload()
	cases = append(cases, conformanceCase{
		Context: absent.context,
		Conformance: states.Conformance{
			Name:  "grains.absent",
			Args:  value.MapOf("name", "node_exporter"),
			Probe: func() (string, error) { return absent.grainValue("node_exporter"), nil },
			Setup: func() error {
				if err := os.RemoveAll(absent.dir("grains")); err != nil {
					return err
				}
				absent.reload()
				return nil
			},
		},
	})

	// ---- environ.setenv ----
	//
	// `permanent: false` is Salt's own default behaviour for the state and
	// is the whole of what is portable here: on a unix the permanent store
	// is `/etc/environment` and on Windows it is the registry, so a case
	// that managed it would be writing to the machine running the suite.
	// `environ_unix_test.go` and `environ_windows_test.go` cover that half
	// by redirecting `EtcEnvironmentPath` and by using a test key.
	const envVar = "HALITE_CONFORMANCE_PROBE"
	t.Cleanup(func() { _ = os.Unsetenv(envVar) })
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name: "environ.setenv",
			Args: value.MapOf("name", envVar, "value", "set-by-conformance", "permanent", false),
			Probe: func() (string, error) {
				if v, ok := os.LookupEnv(envVar); ok {
					return v, nil
				}
				return "absent", nil
			},
			Setup: func() error { return os.Unsetenv(envVar) },
		},
	})

	// ---- beacon and schedule ----
	//
	// The same state twice, so both are driven rather than one taken on
	// trust -- which is how `watcher_states_test.go` argues it too.
	for _, w := range []struct {
		module  string
		dataArg string
		config  *value.Map
	}{
		{"beacon", "beacon_data", value.MapOf(
			"files", value.MapOf("/etc/nginx/nginx.conf", value.MapOf("mask", []any{"modify"})),
			"interval", int64(5))},
		{"schedule", "job", value.MapOf("function", "state.apply", "seconds", int64(3600))},
	} {
		w := w
		const name = "conformance"

		addRoots := newNodeRoots(t)
		cases = append(cases, conformanceCase{
			Context: addRoots.context,
			Conformance: states.Conformance{
				Name:  w.module + ".present",
				Args:  value.MapOf("name", name, w.dataArg, w.config),
				Probe: watcherProbe(addRoots, name),
				Setup: func() error { return addRoots.watcher.Delete(name) },
			},
		})

		removeRoots := newNodeRoots(t)
		cases = append(cases, conformanceCase{
			Context: removeRoots.context,
			Conformance: states.Conformance{
				Name:  w.module + ".absent",
				Args:  value.MapOf("name", name),
				Probe: watcherProbe(removeRoots, name),
				Setup: func() error { return removeRoots.watcher.Add(name, w.config) },
			},
		})
	}

	// ---- event.send ----
	//
	// An event is a thing that happened, not a condition to hold, so there
	// is nothing for a second run to find already true. The state's own
	// comment says exactly that, and Salt's does the same.
	//
	// The probe is what makes the case worth having: it counts what reached
	// the bus, so a test-mode run that fired the event anyway is caught by
	// the count rather than by the state's word for it.
	events := newNodeRoots(t)
	cases = append(cases, conformanceCase{
		Context: events.context,
		Conformance: states.Conformance{
			Name: "event.send",
			Args: value.MapOf("name", "conformance/finished",
				"data", value.MapOf("states", int64(3))),
			Probe: func() (string, error) {
				return fmt.Sprintf("%d sent", len(events.events.sent)), nil
			},
			Setup:           func() error { events.events.sent = nil; return nil },
			SkipIdempotence: true,
			SkipIdempotenceReason: "an event is something that happened, not a condition to hold, " +
				"so every run sends one; the state's own comment says so and Salt's behaves the same",
		},
	})

	// ---- saltutil.sync_* ----
	//
	// Seven registered functions over one implementation, and seven cases,
	// because the accounting counts functions and a shared implementation is
	// not a shared registration -- DIVERGENCE 5.128's lesson, where a
	// finished module was in no build because nothing called its register.
	for _, function := range []string{
		"sync_all", "sync_beacons", "sync_grains", "sync_modules",
		"sync_renderers", "sync_returners", "sync_states",
	} {
		roots := newNodeRoots(t)
		cases = append(cases, conformanceCase{
			Context: roots.context,
			Conformance: states.Conformance{
				Name:           "saltutil." + function,
				Args:           value.MapOf("name", "extensions"),
				Setup:          func() error { roots.syncFetched = false; return nil },
				AlwaysPredicts: true,
				AlwaysPredictsReason: "whether any bundle on the file server differs from the one " +
					"held here cannot be known without fetching it, which is why the signature " +
					"declares test mode unreliable",
			},
		})
	}

	return cases
}

// watcherProbe reports whether a beacon or job is configured, and with what,
// so that a test-mode run which added it anyway is seen directly.
func watcherProbe(n *nodeRoots, name string) func() (string, error) {
	return func() (string, error) {
		cfg, ok := n.watcher.set.Get(name)
		if !ok {
			return "absent", nil
		}
		return "present " + yaml.Encode(value.MapOf("c", cfg), yaml.EncodeOptions{Indent: 2}), nil
	}
}

// The `environ` execution functions honour `--test`, which nothing checked.
//
// `applyEnviron` has a test guard of its own, separate from the one in the
// state: two independent guards, which is why breaking the state's alone
// changes nothing observable. Deleting the inner one passed the whole
// package, and passed `TestEveryMutatingFunctionHonoursItsTestModeClaim` as
// well -- that audit needs a function to fail a dynamic *and* a static check,
// and neither half sees this one. The dynamic half watches a directory and a
// recording runner, and `os.Setenv` is neither a file nor a command; the
// static half looks for a reference to `Test`, and the audit's own comment
// says it "cannot tell a `Test` reference that guards the mutation from one
// that only phrases a message".
//
// So the guard that stops `environ.setval --test` from changing this
// process's environment, and on a unix from writing `/etc/environment`, was
// held by nothing at all. DIVERGENCE 5.157.
func TestEnvironExecFunctionsChangeNothingInTestMode(t *testing.T) {
	r := New()
	const key = "HALITE_EXEC_TESTMODE_PROBE"
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}

	// The permanent store too, pointed somewhere harmless. Not on Windows,
	// where it is the registry rather than a file -- `environ_windows_test.go`
	// is where that half belongs.
	permanent := runtime.GOOS != "windows"
	oldPath := EtcEnvironmentPath
	envFile := filepath.Join(t.TempDir(), "environment")
	EtcEnvironmentPath = envFile
	t.Cleanup(func() { EtcEnvironmentPath = oldPath })
	if err := os.WriteFile(envFile, []byte("EXISTING=keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args *value.Map
	}{
		{"environ.setval", value.MapOf("key", key, "val", "set-by-a-dry-run", "permanent", permanent)},
		{"environ.setenv", value.MapOf("environ", value.MapOf(key, "set-by-a-dry-run"), "permanent", permanent)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCtx(true)
			if _, err := r.Exec.Call(c, tc.name, tc.args); err != nil {
				t.Fatalf("%s under --test: %v", tc.name, err)
			}
			if got, ok := os.LookupEnv(key); ok {
				t.Errorf("%s set %s=%q during a dry run", tc.name, key, got)
			}
			if !permanent {
				return
			}
			body, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "EXISTING=keep\n" {
				t.Errorf("%s wrote the permanent store during a dry run:\n%s", tc.name, body)
			}
		})
	}
}
