package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every spelling of a layout a tree might carry reads to the same thing.
// Salt writes the positional index into the vdev name and the devices as
// a mapping to nothing; a person writes a list. Both arrive here.
func TestALayoutIsReadInEverySpellingATreeWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  any
		want string
	}{
		{"a bare device", "/dev/sda", "/dev/sda"},
		{"a list of devices is a stripe",
			[]any{"/dev/sda", "/dev/sdb"}, "/dev/sda /dev/sdb"},
		{"one mirror",
			value.MapOf("mirror", []any{"/dev/sda", "/dev/sdb"}),
			"mirror /dev/sda /dev/sdb"},
		{"several vdevs in order",
			[]any{
				value.MapOf("mirror", []any{"/dev/sda", "/dev/sdb"}),
				value.MapOf("mirror", []any{"/dev/sdc", "/dev/sdd"}),
				value.MapOf("log", []any{"/dev/nvme0n1"}),
			},
			"mirror /dev/sda /dev/sdb mirror /dev/sdc /dev/sdd log /dev/nvme0n1"},
		{"Salt's positional index is not part of the type",
			value.MapOf("raidz2-0", []any{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"}),
			"raidz2 /dev/sda /dev/sdb /dev/sdc /dev/sdd"},
		{"Salt's devices as a mapping to nothing",
			value.MapOf("mirror-0", value.MapOf("/dev/sda", nil, "/dev/sdb", nil)),
			"mirror /dev/sda /dev/sdb"},
	} {
		got, err := parseLayout(tc.raw)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if argv := strings.Join(vdevArgv(got), " "); argv != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, argv, tc.want)
		}
	}
}

// A vdev type ZFS does not have is refused, and the refusal says what
// the types are. Passing it through would have zpool read it as a
// device path and fail with something less useful.
func TestALayoutThatIsNotAVdevIsRefusedByName(t *testing.T) {
	_, err := parseLayout(value.MapOf("mirrored", []any{"/dev/sda", "/dev/sdb"}))
	if err == nil {
		t.Fatal("a vdev type that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "mirrored") || !strings.Contains(err.Error(), "raidz2") {
		t.Errorf("the refusal does not name the mistake and the alternatives: %v", err)
	}

	if _, err := parseLayout(value.MapOf("mirror", []any{})); err == nil {
		t.Error("a mirror with no devices under it was accepted")
	}
}

// The state creates a pool that is not there, from the declared layout,
// and reports no change on the run after that.
func TestZpoolPresentCreatesAndThenConverges(t *testing.T) {
	r := New()
	c := zpoolFixture(t, nil)

	args := value.MapOf(
		"name", "tank",
		"layout", value.MapOf("mirror", []any{"/dev/sda", "/dev/sdb"}),
		"properties", value.MapOf("ashift", "12"),
		"import", false,
	)
	res, err := r.States.Call(c, "zpool.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("creating the pool failed: %s", res.Comment)
	}
	assertRan(t, c, "zpool create -o ashift=12 tank mirror /dev/sda /dev/sdb")

	// Now it exists, with that property and that layout.
	c = zpoolFixture(t, map[string]string{
		"zpool list -H -o name tank":                    "tank",
		"zpool get -H -p -o property,value ashift tank": "ashift\t12",
		"zpool list -v -H -p tank":                      oneMirrorListing,
	})
	res, err = r.States.Call(c, "zpool.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %v", res.Changes)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("the second run warned: %v", res.Warnings)
	}
}

// A pool that already exists is never reshaped. A top-level vdev cannot
// be removed from most pools, and `zpool add` aimed at a mirror turns it
// into a stripe with no undo, so a layout that does not match is a
// warning and not an action.
func TestZpoolPresentReportsALayoutItWillNotReshape(t *testing.T) {
	r := New()
	c := zpoolFixture(t, map[string]string{
		"zpool list -H -o name tank": "tank",
		// One mirror, where the declaration asks for two.
		"zpool list -v -H -p tank": oneMirrorListing,
	})

	res, err := r.States.Call(c, "zpool.present", value.MapOf(
		"name", "tank",
		"layout", []any{
			value.MapOf("mirror", []any{"/dev/sda", "/dev/sdb"}),
			value.MapOf("mirror", []any{"/dev/sdc", "/dev/sdd"}),
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	if res.HasChanges() {
		t.Errorf("a layout difference was acted on: %v", res.Changes)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("a layout difference produced %d warning(s): %v", len(res.Warnings), res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "does not reshape") {
		t.Errorf("the warning does not say what it will not do: %s", res.Warnings[0])
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.Contains(ran, "add") || strings.Contains(ran, "create") {
			t.Errorf("a pool that exists was reshaped: %q", ran)
		}
	}
}

// A pool that is on this node's devices but not attached is imported,
// not created over. Creating a pool on the devices of one that already
// holds data destroys the data, and a node that rebooted into a state
// run must not be able to do that by itself.
func TestZpoolPresentImportsBeforeItCreates(t *testing.T) {
	r := New()
	c := zpoolFixture(t, map[string]string{
		"zpool import -d /vdevs tank": "",
	})

	res, err := r.States.Call(c, "zpool.present", value.MapOf(
		"name", "tank",
		"layout", value.MapOf("mirror", []any{"/vdevs/a", "/vdevs/b"}),
		"device_dir", "/vdevs",
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "imported") {
		t.Errorf("the pool was not reported as imported: %s", res.Comment)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "zpool create") {
			t.Errorf("a pool that could be imported was created over: %q", ran)
		}
	}
}

// And with the import turned off, or when there is nothing to import,
// it creates.
func TestZpoolPresentCreatesWhenThereIsNothingToImport(t *testing.T) {
	r := New()
	c := zpoolFixture(t, nil)
	// The import fails, as it does when no such pool is on the devices.
	c.Runner.(*exec.RecordingRunner).Responses["zpool import tank"] =
		exec.Result{Code: 1, Stderr: "cannot import 'tank': no such pool available"}

	res, err := r.States.Call(c, "zpool.present", value.MapOf(
		"name", "tank",
		"layout", []any{"/dev/sda"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	assertRan(t, c, "zpool create tank /dev/sda")
}

// A pool that exists and is missing no layout is left alone entirely
// when the declaration names none: a state that says only "this pool
// should exist" is satisfied.
func TestZpoolPresentWithNoLayoutIsSatisfiedByAPoolThatExists(t *testing.T) {
	r := New()
	c := zpoolFixture(t, map[string]string{"zpool list -H -o name tank": "tank"})

	res, err := r.States.Call(c, "zpool.present", value.MapOf("name", "tank"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	if res.HasChanges() || len(res.Warnings) != 0 {
		t.Errorf("an existing pool with nothing declared reported %v / %v", res.Changes, res.Warnings)
	}
}

// absent exports by default and destroys only when told, because the two
// differ by whether the data survives.
func TestZpoolAbsentExportsByDefaultAndDestroysWhenTold(t *testing.T) {
	r := New()

	c := zpoolFixture(t, map[string]string{"zpool list -H -o name tank": "tank"})
	res, err := r.States.Call(c, "zpool.absent", value.MapOf("name", "tank"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	assertRan(t, c, "zpool export tank")
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "zpool destroy") {
			t.Errorf("the default destroyed the pool: %q", ran)
		}
	}

	c = zpoolFixture(t, map[string]string{"zpool list -H -o name tank": "tank"})
	res, err = r.States.Call(c, "zpool.absent", value.MapOf("name", "tank", "export", false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	assertRan(t, c, "zpool destroy tank")

	// And a pool that is not attached is nothing to do.
	c = zpoolFixture(t, nil)
	res, err = r.States.Call(c, "zpool.absent", value.MapOf("name", "tank"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a pool that is not there reported a change: %v", res.Changes)
	}
}

// Test mode changes nothing at all.
func TestZpoolStatesInTestModeRunNothingThatChanges(t *testing.T) {
	r := New()
	for _, tc := range []struct {
		state string
		args  *value.Map
		setup map[string]string
	}{
		{"zpool.present", value.MapOf("name", "tank",
			"layout", value.MapOf("mirror", []any{"/dev/sda", "/dev/sdb"})), nil},
		{"zpool.absent", value.MapOf("name", "tank"),
			map[string]string{"zpool list -H -o name tank": "tank"}},
	} {
		c := zpoolFixture(t, tc.setup)
		c.Test = true
		res, err := r.States.Call(c, tc.state, tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if res.Result != nil {
			t.Errorf("%s did not report a would-change: %s", tc.state, res.Comment)
		}
		for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
			for _, verb := range []string{"create", "destroy", "export", "import", "set"} {
				if strings.HasPrefix(ran, "zpool "+verb) {
					t.Errorf("%s in test mode ran %q", tc.state, ran)
				}
			}
		}
	}
}

// Importing with recovery discards the pool's last transactions, which
// loses whatever was in them. It is refused unless the caller also says
// force, so that the loss is something they asked for twice.
func TestImportWithRecoveryIsRefusedUnlessForcedToo(t *testing.T) {
	r := New()
	c := zpoolFixture(t, nil)

	_, err := r.Exec.Call(c, "zpool.import", value.MapOf("zpool", "tank", "recovery", true))
	if err == nil {
		t.Fatal("recovery was accepted on its own")
	}
	if !strings.Contains(err.Error(), "loses") {
		t.Errorf("the refusal does not say what is lost: %v", err)
	}

	if _, err := r.Exec.Call(c, "zpool.import",
		value.MapOf("zpool", "tank", "recovery", true, "force", true)); err != nil {
		t.Fatalf("recovery with force was refused: %v", err)
	}
	assertRan(t, c, "zpool import -f -F tank")
}

// These are the bytes ZFS 2.2.2 really emits, copied out of a run
// against a pool on a Linux node rather than written from memory. The
// shape is the point: every row under the pool is indented by exactly
// one tab, whether it is a top-level vdev or a device inside one, so a
// reader that takes the indentation for depth finds nothing. The first
// version of this module did, and reported every pool as empty.
const (
	oneMirrorListing = "tank\t251658240\t339968\t251318272\t-\t-\t3\t0\t1.00\tONLINE\t-\n" +
		"\tmirror-0\t251658240\t339968\t251318272\t-\t-\t3\t0\t-\tONLINE\n" +
		"\t/dev/sda\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/dev/sdb\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n"

	twoMirrorListing = "tank\t503316480\t638976\t502677504\t-\t-\t1\t0\t1.00\tONLINE\t-\n" +
		"\tmirror-0\t251658240\t638976\t251019264\t-\t-\t3\t0\t-\tONLINE\n" +
		"\t/vdevs/a\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/vdevs/b\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\tmirror-1\t251658240\t0\t251658240\t-\t-\t0\t0\t-\tONLINE\n" +
		"\t/vdevs/c\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/vdevs/d\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n"

	stripeListing = "tank\t754974720\t159744\t754814976\t-\t-\t0\t0\t1.00\tONLINE\t-\n" +
		"\t/vdevs/a\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/vdevs/b\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/vdevs/c\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n"

	// Two mirrors, a log, a cache and a spare. The three section headers
	// are copied byte for byte: they are printed unindented and padded
	// with spaces, ignoring -H, in the middle of an otherwise
	// tab-separated listing.
	everythingListing = "tank\t503316480\t126976\t503189504\t-\t-\t2\t0\t1.00\tONLINE\t-\n" +
		"\tmirror-0\t251658240\t67584\t251590656\t-\t-\t2\t0\t-\tONLINE\n" +
		"\t/vdevs/a\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/vdevs/b\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\tmirror-1\t251658240\t59392\t251598848\t-\t-\t2\t0\t-\tONLINE\n" +
		"\t/vdevs/c\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"\t/vdevs/d\t268435456\t-\t-\t-\t-\t-\t-\t-\tONLINE\n" +
		"logs              -      -      -        -         -      -      -      -         -\n" +
		"\t/vdevs/e\t268435456\t0\t251658240\t-\t-\t0\t0\t-\tONLINE\n" +
		"cache             -      -      -        -         -      -      -      -         -\n" +
		"\t/vdevs/f\t268435456\t512\t263715840\t-\t-\t0\t0\t-\tONLINE\n" +
		"spare             -      -      -        -         -      -      -      -         -\n" +
		"\t/vdevs/g\t268435456\t-\t-\t-\t-\t-\t-\t-\tAVAIL\n"
)

// The listing is read by name, because scripted mode drops the depth
// that would otherwise say which rows are inside which.
func TestAPoolsLayoutIsReadOutOfTheListingByName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing string
		want    string
	}{
		{"one mirror of two", oneMirrorListing, "mirror of 2"},
		{"two mirrors", twoMirrorListing, "mirror of 2, mirror of 2"},
		{"a stripe is three vdevs of one, not one of three",
			stripeListing, "device of 1, device of 1, device of 1"},
		{"a log, a cache and a spare are their own vdevs and not extra mirror legs",
			everythingListing,
			"mirror of 2, mirror of 2, log of 1, cache of 1, spare of 1"},
	} {
		c := zpoolFixture(t, map[string]string{"zpool list -v -H -p tank": tc.listing})
		got, err := zpoolVdevs(c, "tank")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if describeLayout(got) != tc.want {
			t.Errorf("%s: read %q, want %q", tc.name, describeLayout(got), tc.want)
		}
	}
}

// zpoolFixture returns a node with a scripted zpool. Anything not named
// in the responses answers as a pool that is not there, which is the
// state of a machine with ZFS installed and no pools on it.
func zpoolFixture(t *testing.T, responses map[string]string) *exec.Context {
	t.Helper()
	// The states are declared for the platforms with ZFS and the
	// registry refuses them elsewhere. They are exercised on a Linux
	// node — under `make zpoolcheck`, against a real pool — rather than
	// on a Windows one pretending to have one.
	if runtime.GOOS == "windows" {
		t.Skip("zpool does not run on Windows, and this build says so")
	}
	scripted := map[string]exec.Result{
		// A pool that is not there: zpool list exits non-zero.
		"zpool list -H -o name tank": {Code: 1, Stderr: "cannot open 'tank': no such pool"},
	}
	for cmd, out := range responses {
		scripted[cmd] = exec.Result{Stdout: out}
	}
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{Responses: scripted}
	c.Lookup = func(name string) string { return "/sbin/" + name }
	return c
}
