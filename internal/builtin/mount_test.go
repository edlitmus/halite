package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// A table entry round-trips: written from a declaration, read back as
// the same declaration, and writing it again is not a change. That last
// part is what a state converging depends on.
func TestAnFstabEntryRoundTripsAndIsWrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	writeFile(t, path, "# a comment an operator wrote\n"+
		"UUID=1111 / ext4 defaults 0 1\n")

	want := fstabEntry{
		Device: "UUID=2222", Point: "/srv/data", Type: "xfs",
		Opts: "defaults,noatime", Dump: "0", Pass: "2",
	}
	c := newCtx(false)

	if got, err := setFstab(c, path, want); err != nil || got != "new" {
		t.Fatalf("first write: got %q, %v; want \"new\"", got, err)
	}
	if got, err := setFstab(c, path, want); err != nil || got != "present" {
		t.Errorf("second write: got %q, %v; want \"present\"", got, err)
	}

	entries, err := readFstab(path)
	if err != nil {
		t.Fatal(err)
	}
	var found *fstabEntry
	for i := range entries {
		if entries[i].Point == "/srv/data" {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatal("the entry was not read back")
	}
	if !found.sameAs(want) {
		t.Errorf("read back %+v, want %+v", *found, want)
	}

	// The lines that were already there are still there, unchanged.
	raw := readFile(t, path)
	for _, line := range []string{"# a comment an operator wrote", "UUID=1111 / ext4 defaults 0 1"} {
		if !strings.Contains(raw, line) {
			t.Errorf("the table lost %q:\n%s", line, raw)
		}
	}
}

// The option list is a set, and the dump and pass fields are numbers.
// Neither is a change, and a state that thought otherwise would report
// one on every run for ever.
func TestAnEntryThatIsSpelledDifferentlyIsNotAChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	writeFile(t, path, "UUID=2222\t/srv\txfs\tnoatime,defaults\t00 02\n")

	c := newCtx(false)
	same := fstabEntry{
		Device: "UUID=2222", Point: "/srv", Type: "xfs",
		Opts: "defaults,noatime", Dump: "0", Pass: "2",
	}
	if got, err := setFstab(c, path, same); err != nil || got != "present" {
		t.Errorf("reordered options read as %q, want \"present\"", got)
	}

	// A real difference is still a change.
	changed := same
	changed.Opts = "defaults,noatime,nosuid"
	if got, err := setFstab(c, path, changed); err != nil || got != "change" {
		t.Errorf("an added option read as %q, want \"change\"", got)
	}
}

// A mount point with a space in it is written the way getmntent reads it
// and comes back whole. It is the one part of the format that cannot be
// guessed from the fields.
func TestAMountPointWithASpaceSurvivesTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fstab")
	want := fstabEntry{
		Device: "/dev/sdb1", Point: "/mnt/my backup", Type: "ext4",
		Opts: "defaults", Dump: "0", Pass: "2",
	}
	if _, err := setFstab(newCtx(false), path, want); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, path)
	if !strings.Contains(raw, `/mnt/my\040backup`) {
		t.Errorf("the space was not escaped:\n%s", raw)
	}
	entries, err := readFstab(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Point != "/mnt/my backup" {
		t.Errorf("read back %+v", entries)
	}
}

// The kernel reports the flags that are in effect, not the options it
// was given, so only the flags it does report can be compared against
// it — and how much an absent flag proves depends on which table the
// answer came from.
func TestOptionsAreComparedAgainstTheRunningTableOnItsOwnTerms(t *testing.T) {
	// The kernel's table: a closed vocabulary, so a flag that is not
	// printed is a flag that is not set.
	kernel := []string{"rw", "relatime", "nosuid"}
	for _, tc := range []struct {
		name     string
		declared []string
		want     string
	}{
		{"an option in effect is not a difference", []string{"rw", "nosuid"}, ""},
		{"the opposite flag is a difference", []string{"ro"}, "ro"},
		{"a flag that is not set is a difference", []string{"noexec"}, "noexec"},
		{"so is the wrong atime", []string{"noatime"}, "noatime"},
		{"the same atime is not", []string{"relatime"}, ""},
		{"the default half of a pair is not, unless contradicted",
			[]string{"exec", "dev"}, ""},
		{"and it is when it is contradicted", []string{"suid"}, "suid"},
		{"an option the table never carries is not evidence",
			[]string{"defaults", "_netdev", "nofail", "x-systemd.automount"}, ""},
	} {
		got := strings.Join(contradictedOptions(tc.declared, kernel, true), ",")
		if got != tc.want {
			t.Errorf("kernel table, %s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	// `mount` output, whose vocabulary differs between platforms: an
	// absent flag concludes nothing, and only the opposite counts.
	// Remounting the root filesystem on every run because a BSD spells a
	// flag differently is the failure this avoids.
	parsed := []string{"local", "read-only", "nosuid"}
	for _, tc := range []struct {
		name     string
		declared []string
		want     string
	}{
		{"an alias for a flag is read as the flag", []string{"ro"}, ""},
		{"and contradicts its opposite", []string{"rw"}, "rw"},
		{"read-only is conclusive by its absence on every platform",
			[]string{"noexec"}, ""},
		{"an absent flag concludes nothing", []string{"noexec", "noatime"}, ""},
	} {
		got := strings.Join(contradictedOptions(tc.declared, parsed, false), ",")
		if got != tc.want {
			t.Errorf("mount output, %s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The state converges: it mounts what is not mounted, writes the table,
// and the run after that reports no change.
func TestMountedConvergesInOneChange(t *testing.T) {
	r := New()
	c, path := mountFixture(t, nil)

	args := value.MapOf(
		"name", "/srv/data", "device", "UUID=2222", "fstype", "xfs",
		"opts", "defaults,noatime", "pass_num", "2", "config", path)

	res, err := r.States.Call(c, "mount.mounted", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("mounting failed: %s", res.Comment)
	}
	if _, ok := res.Changes.Get("mount"); !ok {
		t.Errorf("mounting an unmounted filesystem reported %v", res.Changes)
	}
	if _, ok := res.Changes.Get("fstab"); !ok {
		t.Errorf("the table was not written: %v", res.Changes)
	}
	assertRan(t, c, "mount -t xfs -o defaults,noatime UUID=2222 /srv/data")

	// Now it is mounted, with those options, and the table agrees.
	c, _ = mountFixture(t, map[string]any{
		"/srv/data": value.MapOf("device", "UUID=2222", "fstype", "xfs",
			"opts", []any{"rw", "noatime"}),
	})
	c.Runner.(*exec.RecordingRunner).Ran = nil
	res, err = r.States.Call(c, "mount.mounted", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %v", res.Changes)
	}
	assertChangedNothing(t, c)
}

// An option the running mount contradicts is a remount, and it is a
// remount rather than an unmount and a mount: a filesystem the node is
// running out of cannot be taken away and put back.
func TestMountedRemountsForAnOptionTheKernelContradicts(t *testing.T) {
	r := New()
	c, path := mountFixture(t, map[string]any{
		"/srv/data": value.MapOf("device", "UUID=2222", "fstype", "xfs",
			"opts", []any{"rw", "relatime"}),
	})
	writeFile(t, path, "UUID=2222\t/srv/data\txfs\tro\t0 2\n")

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID=2222", "fstype", "xfs",
		"opts", "ro", "pass_num", "2", "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the remount failed: %s", res.Comment)
	}
	if _, ok := res.Changes.Get("opts"); !ok {
		t.Errorf("a contradicted option did not report an option change: %v", res.Changes)
	}
	assertRan(t, c, "mount -t xfs -o remount,ro UUID=2222 /srv/data")
	if !strings.Contains(res.Comment, "ro") {
		t.Errorf("the comment does not name the option: %s", res.Comment)
	}
}

// Test mode changes nothing: it neither runs mount nor writes the table.
func TestMountedInTestModeTouchesNothing(t *testing.T) {
	r := New()
	c, path := mountFixture(t, nil)
	c.Test = true

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID=2222", "fstype", "xfs", "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Fatalf("test mode did not report a would-change: %s", res.Comment)
	}
	assertChangedNothing(t, c)
	if _, err := os.Stat(path); err == nil {
		t.Error("a test run wrote the table")
	}
}

// persist: False manages the running mount and leaves the table alone,
// which is what a temporary mount wants and the only way to declare one.
func TestMountedWithoutPersistLeavesTheTableAlone(t *testing.T) {
	r := New()
	c, path := mountFixture(t, nil)

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID=2222", "fstype", "xfs",
		"persist", false, "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("mounting failed: %s", res.Comment)
	}
	if _, ok := res.Changes.Get("fstab"); ok {
		t.Error("persist: False wrote the table anyway")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("persist: False created the table")
	}
}

// The unmount state unmounts, optionally takes the entry out, and
// converges to nothing to do.
func TestUnmountedRemovesTheMountAndOptionallyTheEntry(t *testing.T) {
	r := New()
	c, path := mountFixture(t, map[string]any{
		"/srv/data": value.MapOf("device", "UUID=2222", "fstype", "xfs",
			"opts", []any{"rw"}),
	})
	writeFile(t, path, "UUID=2222\t/srv/data\txfs\tdefaults\t0 2\n")

	res, err := r.States.Call(c, "mount.unmounted", value.MapOf(
		"name", "/srv/data", "persist", true, "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("unmounting failed: %s", res.Comment)
	}
	assertRan(t, c, "umount /srv/data")
	if strings.Contains(readFile(t, path), "/srv/data") {
		t.Errorf("the entry is still in the table:\n%s", readFile(t, path))
	}

	// Nothing mounted and nothing in the table is nothing to do.
	c, path = mountFixture(t, nil)
	res, err = r.States.Call(c, "mount.unmounted", value.MapOf(
		"name", "/srv/data", "persist", true, "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("unmounting what is not mounted reported a change: %v", res.Changes)
	}
}

// The same filesystem read out of the kernel's own table converges too.
//
// It has its own test because the two readings take different code and
// only one of them was covered: the fixture above answers a `mount`
// command, and a state that converged against that still remounted on
// every run against /proc/self/mounts, where the options arrive as a
// list this build made rather than one YAML handed it.
func TestMountedConvergesAgainstTheKernelsOwnTable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mount.mounted does not run on Windows, and this build says so")
	}
	r := New()
	dir := t.TempDir()

	old := ProcMountsPath
	ProcMountsPath = filepath.Join(dir, "mounts")
	t.Cleanup(func() { ProcMountsPath = old })
	writeFile(t, ProcMountsPath,
		"/dev/loop2 /mnt/probe ext4 rw,nosuid,relatime 0 0\n")

	path := filepath.Join(dir, "fstab")
	writeFile(t, path, "/dev/loop2\t/mnt/probe\text4\tdefaults,nosuid\t0 0\n")

	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{}

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/mnt/probe", "device", "/dev/loop2", "fstype", "ext4",
		"opts", "defaults,nosuid", "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	if res.HasChanges() {
		t.Errorf("a filesystem already mounted as declared reported %v: %s",
			res.Changes, res.Comment)
	}
	if ran := c.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 0 {
		t.Errorf("nothing needed doing, but %v was run", ran)
	}
}

// mountFixture returns a node whose running mount table is the one given
// and whose fstab is a file of its own, so that no test can mount
// anything on the machine it runs on.
func mountFixture(t *testing.T, active map[string]any) (*exec.Context, string) {
	t.Helper()
	// The states are declared for the platforms with a mount table, and
	// the registry refuses them elsewhere. Their behaviour is checked on
	// a Linux node, not on a Windows one pretending to be one.
	if runtime.GOOS == "windows" {
		t.Skip("mount.mounted does not run on Windows, and this build says so")
	}
	// Pointed at a file that is not there, so that activeMounts falls
	// through to the `mount` command, which the runner answers.
	oldProc := ProcMountsPath
	ProcMountsPath = filepath.Join(t.TempDir(), "no-proc-mounts")
	t.Cleanup(func() { ProcMountsPath = oldProc })

	var lines []string
	for point, m := range active {
		e := m.(*value.Map)
		opts := states.Strings(e, "opts")
		lines = append(lines, strings.Join([]string{
			states.Str(e, "device", ""), "on", point, "type",
			states.Str(e, "fstype", ""), "(" + strings.Join(opts, ",") + ")",
		}, " "))
	}

	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"mount": {Stdout: strings.Join(lines, "\n")},
		},
	}
	return c, filepath.Join(t.TempDir(), "fstab")
}

// assertChangedNothing fails if anything but a read of the mount table
// was run. Reading it is how the state decides there is nothing to do,
// so a run that reads and stops is the converged run, not an idle one.
func assertChangedNothing(t *testing.T, c *exec.Context) {
	t.Helper()
	for _, got := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if got != "mount" {
			t.Errorf("something was changed: %q", got)
		}
	}
}

func assertRan(t *testing.T, c *exec.Context, want string) {
	t.Helper()
	for _, got := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if got == want {
			return
		}
	}
	t.Errorf("%q was not run; ran %v", want, c.Runner.(*exec.RecordingRunner).RanCommands())
}
