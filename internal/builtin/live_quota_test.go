package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `quota`, against the real tools and the real mount table of whatever
// machine this runs on.
//
// # What can honestly be checked here, and what cannot
//
// Nothing here sets a quota. Setting one needs root, a filesystem with
// quotas enabled, and an account to attach it to, and the machine that
// has all three on this fleet is a production host. So the mutating half
// stays where pam's does: against the argument table, from any platform,
// with no tool involved.
//
// What a real machine *can* settle is the two things a fixture cannot.
// The first is whether `quotaon -p` says what this module reads it as
// saying — a state word this build matches on, on a tool nobody here has
// run. The second is what happens on a filesystem that has quotas under
// a mechanism these tools cannot see, which on this project's own fleet
// is every filesystem: it is entirely ZFS.
//
// # Why the ZFS check is the one that matters
//
// `repquota` on a ZFS dataset reports nothing at all. A module that
// passed that on would tell an operator "this filesystem has no quotas"
// about a dataset that may have a quota on every account, and that is
// the wrong answer rather than a missing one. The check below is run
// against the machine's own mount table rather than a fixture, because
// the fixture is where a wrong idea of what ZFS looks like would live.

// liveQuotaSkip reports why this machine cannot be asked, or an empty
// string.
func liveQuotaSkip() string {
	if _, why := quotaReadPlan(runtime.GOOS); why == "" {
		return runtime.GOOS + " has no filesystem quotas of the kind this module drives"
	}
	return ""
}

// This machine's own ZFS filesystems are reported as having their quotas
// elsewhere.
//
// The mount table is the real one. On a host with no ZFS the test says
// so and skips rather than passing on an empty set, because "no ZFS
// filesystem was misreported" is not a fact about this module when there
// was no ZFS filesystem to misreport.
func TestZFSFilesystemsOnThisMachineAreDivertedToTheZFSModule(t *testing.T) {
	if why := liveQuotaSkip(); why != "" {
		t.Skip(why)
	}
	c := &exec.Context{}
	mounts, _, err := activeMounts(c)
	if err != nil {
		t.Skipf("this host's mount table could not be read: %v", err)
	}

	var zfsPoints, otherPoints []string
	for _, point := range mounts.SortedKeys() {
		entry, _ := mounts.GetString(point)
		m, ok := entry.(*value.Map)
		if !ok {
			continue
		}
		if states.Str(m, "fstype", "") == "zfs" {
			zfsPoints = append(zfsPoints, point)
			continue
		}
		otherPoints = append(otherPoints, point)
	}
	if len(zfsPoints) == 0 {
		t.Skip("this host has no ZFS filesystem to be misreported")
	}

	for _, point := range zfsPoints {
		why := quotaWrongMechanism(c, point)
		if why == "" {
			t.Errorf("%s is ZFS on this host and the quota tools were let loose on it", point)
			continue
		}
		if !strings.Contains(why, "zfs.get") {
			t.Errorf("%s: the answer does not say where to look instead: %s", point, why)
		}
	}
	t.Logf("%d of this host's %d mounted filesystems are ZFS and are diverted by name",
		len(zfsPoints), len(zfsPoints)+len(otherPoints))

	// And a filesystem that is not ZFS is left to the quota tools, so the
	// diversion is about the filesystem rather than about the platform.
	for _, point := range otherPoints {
		if why := quotaWrongMechanism(c, point); why != "" {
			t.Errorf("%s is not ZFS on this host and was diverted anyway: %s", point, why)
		}
	}
}

// `quotaon -p` on this machine says one of the two things this module
// reads it as saying.
//
// The state word is what `quota.get_mode` matches on, and it has been
// matched against documentation rather than against a running tool. This
// asks the real binary about a real filesystem and requires the answer
// to be one of the two, or a refusal — never a third thing quietly read
// as "off", which is the answer that would tell an operator their quotas
// are not running when they are.
func TestQuotaonReportsAStateThisModuleCanRead(t *testing.T) {
	if why := liveQuotaSkip(); why != "" {
		t.Skip(why)
	}
	c := &exec.Context{}
	if c.Which("quotaon") == "" {
		t.Skip("this host has no `quotaon`")
	}

	// It has to be a filesystem the quota tools can actually see. On this
	// project's own fleet the root filesystem is ZFS, so asking about `/`
	// would be answered by the diversion above and `quotaon` would never
	// run at all -- a test that passed without reaching the thing it is
	// named after. The first non-ZFS mount point is used instead, and
	// where there is none the test says so.
	point := liveQuotaNonZFSMount(t, c)
	if point == "" {
		t.Skip("every filesystem on this host is ZFS, so `quotaon -p` has nothing to be asked about")
	}

	// It almost certainly has no quotas, which is the point: the answer
	// being "off" is a real answer, and a tool that cannot answer at all
	// must say so rather than be read as "off".
	out, err := quotaMode(c, point)
	if err != nil {
		// A refusal is an acceptable outcome and is what a non-root
		// account or a ZFS root should produce. What it must not be is
		// silence.
		if strings.TrimSpace(err.Error()) == "" {
			t.Fatal("quotaon failed and said nothing about why")
		}
		t.Skipf("this host cannot be asked about %s: %v", point, err)
	}

	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("get_mode returned %T", out)
	}
	for _, kind := range []string{"user", "group"} {
		v, ok := m.GetString(kind)
		if !ok {
			t.Errorf("get_mode did not report the %s quota state at all", kind)
			continue
		}
		if _, ok := v.(bool); !ok {
			t.Errorf("the %s quota state came back as %T, want a bool", kind, v)
		}
	}
	t.Logf("quotaon on this %s host answered for %s: %v", runtime.GOOS, point, m.Entries())
}

// `repquota` on this machine produces something this build's reader
// accepts, or a refusal that names the problem.
//
// This is the assertion that the format read out of repquota.c is the
// format the installed binary prints. It runs only where quotas are
// actually switched on, because repquota prints nothing at all where
// they are not, and asserting about an empty report would be asserting
// about nothing.
func TestRepquotaOnThisMachineIsReadableOrRefusedByName(t *testing.T) {
	if why := liveQuotaSkip(); why != "" {
		t.Skip(why)
	}
	c := &exec.Context{}
	if c.Which("repquota") == "" {
		t.Skip("this host has no `repquota`")
	}

	rows, format, err := quotaRun(c, "user", "-a")
	if err != nil {
		if strings.TrimSpace(err.Error()) == "" {
			t.Fatal("repquota failed and said nothing about why")
		}
		t.Skipf("this host's repquota could not be read: %v", err)
	}
	if len(rows) == 0 {
		t.Skipf("no filesystem on this host has %s quotas switched on, so there is no report to read", runtime.GOOS)
	}
	for _, row := range rows {
		if row.Name == "" {
			t.Errorf("a row came back with no account name: %+v", row)
		}
		if row.Filesystem == "" {
			t.Errorf("%s's row does not say which filesystem it is about", row.Name)
		}
		if row.BlocksUsed < 0 || row.FilesUsed < 0 {
			t.Errorf("%s's row reads as negative usage: %+v", row.Name, row)
		}
	}
	t.Logf("read %d real quota rows from this host in the %s format", len(rows), format)
}

// liveQuotaNonZFSMount is the first mounted filesystem the quota tools
// could see, or an empty string.
//
// Pseudo-filesystems are skipped. devfs and its like have no quotas and
// no device to hold a quota file, so `quotaon` on one answers a question
// nobody asked.
func liveQuotaNonZFSMount(t *testing.T, c *exec.Context) string {
	t.Helper()
	pseudo := map[string]bool{
		"devfs": true, "procfs": true, "linprocfs": true, "linsysfs": true,
		"tmpfs": true, "fdescfs": true, "nullfs": true, "proc": true,
		"sysfs": true, "devtmpfs": true, "cgroup": true, "cgroup2": true,
		"overlay": true, "squashfs": true, "autofs": true,
	}
	mounts, _, err := activeMounts(c)
	if err != nil {
		return ""
	}
	for _, point := range mounts.SortedKeys() {
		entry, _ := mounts.GetString(point)
		m, ok := entry.(*value.Map)
		if !ok {
			continue
		}
		fstype := states.Str(m, "fstype", "")
		if fstype == "zfs" || fstype == "" || pseudo[fstype] {
			continue
		}
		return point
	}
	return ""
}
