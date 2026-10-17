package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// These tests are about the device field of mount.mounted and
// mount.unmounted: a declaration names a device by UUID=, LABEL= or a
// /dev/disk/by-* link, and the running table names the node mount(8)
// resolved that to. Compared as strings the two never agreed, and every
// run remounted (DIVERGENCE 5.313).
//
// NOTHING HERE IS CAPTURED. Every running-table line below is synthetic:
// written to the shape of /proc/self/mounts, not taken from a machine,
// and the device names, UUIDs and labels are made up. The /dev trees are
// built in a temporary directory with relative symlinks, which is how
// udev lays out /dev/disk/by-* -- but that layout, and the escaping of a
// label in a link name, are read from udev and libblkid's source rather
// than captured. What they prove is the comparison, given that layout;
// they do not prove the layout.

const testUUID = "0b6f9f5e-1d2c-4c8e-9a51-3f6c2d7e8a90"

// identityFixture lays out a /dev under a temporary DevRoot, points the
// kernel's table at a file holding procMounts, and returns a context
// whose runner records commands and an fstab path in the same tree.
//
// links maps a path under /dev to the relative target udev would give it;
// nodes are the plain files that stand in for device nodes.
func identityFixture(t *testing.T, nodes []string, links map[string]string, procMounts string) (*exec.Context, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mount.mounted does not run on Windows, and this build says so")
	}
	linuxMount(t)
	root := t.TempDir()
	for _, n := range nodes {
		p := filepath.Join(root, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "")
	}
	for l, target := range links {
		p := filepath.Join(root, l)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot, oldProc := DevRoot, ProcMountsPath
	DevRoot = root
	ProcMountsPath = filepath.Join(root, "proc-self-mounts")
	t.Cleanup(func() { DevRoot, ProcMountsPath = oldRoot, oldProc })
	writeFile(t, ProcMountsPath, procMounts)

	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{}
	return c, filepath.Join(root, "fstab")
}

func ranCommands(c *exec.Context) []string {
	return c.Runner.(*exec.RecordingRunner).RanCommands()
}

// A filesystem declared by UUID=, mounted, and in the table as declared,
// is converged: the running table's /dev/nvme0n1p2 is the node the UUID
// names, so there is no change and nothing is run.
func TestMountedByUUIDIsTheNodeTheKernelNames(t *testing.T) {
	r := New()
	c, path := identityFixture(t,
		[]string{"/dev/nvme0n1p2"},
		map[string]string{"/dev/disk/by-uuid/" + testUUID: "../../nvme0n1p2"},
		// synthetic
		"/dev/nvme0n1p2 /srv/data ext4 rw,relatime 0 0\n")
	writeFile(t, path, "UUID="+testUUID+"\t/srv/data\text4\tdefaults\t0 2\n")

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID="+testUUID, "fstype", "ext4",
		"opts", "defaults", "pass_num", "2", "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the state failed: %s", res.Comment)
	}
	if res.HasChanges() {
		t.Errorf("a filesystem mounted from the node its UUID names reported %v: %s",
			res.Changes, res.Comment)
	}
	if ran := ranCommands(c); len(ran) != 0 {
		t.Errorf("nothing needed doing, but %v was run", ran)
	}
}

// LVM: the kernel's table names /dev/mapper/<vg>-<lv>, itself a symlink
// to ../dm-0, and the UUID's link goes to ../../dm-0. Two chains of
// links that end at the same node are the same device.
func TestMountedByUUIDMatchesADeviceMapperName(t *testing.T) {
	r := New()
	c, path := identityFixture(t,
		[]string{"/dev/dm-0"},
		map[string]string{
			"/dev/disk/by-uuid/" + testUUID: "../../dm-0",
			"/dev/mapper/vg0-data":          "../dm-0",
		},
		// synthetic
		"/dev/mapper/vg0-data /srv/data xfs rw,relatime 0 0\n")

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID="+testUUID, "fstype", "xfs",
		"persist", false, "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() || res.Result == nil || !*res.Result {
		t.Errorf("a mapper name for the UUID's node reported %v: %s", res.Changes, res.Comment)
	}
	if ran := ranCommands(c); len(ran) != 0 {
		t.Errorf("nothing needed doing, but %v was run", ran)
	}
}

// A label is found by its udev link name, which escapes what a file name
// cannot hold. "my data/2" is linked as my\x20data\x2f2, and a label
// declared quoted is the same label.
func TestMountedByLabelFindsTheEscapedLink(t *testing.T) {
	for _, declared := range []string{`LABEL=my data/2`, `LABEL="my data/2"`} {
		t.Run(declared, func(t *testing.T) {
			r := New()
			c, path := identityFixture(t,
				[]string{"/dev/sdb1"},
				map[string]string{`/dev/disk/by-label/my\x20data\x2f2`: "../../sdb1"},
				// synthetic
				"/dev/sdb1 /mnt/data ext4 rw,relatime 0 0\n")

			res, err := r.States.Call(c, "mount.mounted", value.MapOf(
				"name", "/mnt/data", "device", declared, "fstype", "ext4",
				"persist", false, "config", path))
			if err != nil {
				t.Fatal(err)
			}
			if res.HasChanges() || res.Result == nil || !*res.Result {
				t.Errorf("%s reported %v: %s", declared, res.Changes, res.Comment)
			}
		})
	}
}

// The encoding itself, case by case. The expected spellings follow
// libblkid's blkid_encode_string; they were not captured from a machine.
func TestDevnodeNamesAreEncodedTheWayUdevLinksThem(t *testing.T) {
	for in, want := range map[string]string{
		"root":          "root",
		"EFI-SYSTEM_1":  "EFI-SYSTEM_1",
		"a#b+c.d:e=f@g": "a#b+c.d:e=f@g",
		"my data":       `my\x20data`,
		"a/b":           `a\x2fb`,
		`back\slash`:    `back\x5cslash`,
		"tab\there":     `tab\x09here`,
		"daten-ä":       "daten-ä",
		"bad\xffbyte":   `bad\xffbyte`,
	} {
		if got := encodeDevnodeName(in); got != want {
			t.Errorf("encodeDevnodeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A UUID whose link goes to a node other than the one mounted is a
// different device, and is still treated as one.
func TestMountedByUUIDOfAnotherDeviceIsStillADifference(t *testing.T) {
	r := New()
	c, path := identityFixture(t,
		[]string{"/dev/nvme0n1p2", "/dev/nvme1n1p1"},
		map[string]string{"/dev/disk/by-uuid/" + testUUID: "../../nvme1n1p1"},
		// synthetic
		"/dev/nvme0n1p2 /srv/data ext4 rw,relatime 0 0\n")

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID="+testUUID, "fstype", "ext4",
		"persist", false, "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Changes.Get("device"); !ok {
		t.Errorf("a different device reported no device change: %v: %s", res.Changes, res.Comment)
	}
	if !strings.Contains(res.Comment, "different device") {
		t.Errorf("the comment does not say why: %s", res.Comment)
	}
}

// A UUID with no link on this host cannot be compared with the node the
// kernel names. The state fails and says so, and does nothing: no
// remount, no fstab write. Remounting on that difference is the loop
// this replaced.
func TestMountedByAnUnresolvableUUIDFailsAndChangesNothing(t *testing.T) {
	r := New()
	c, path := identityFixture(t,
		[]string{"/dev/nvme0n1p2"},
		nil,
		// synthetic
		"/dev/nvme0n1p2 /srv/data ext4 rw,relatime 0 0\n")

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/data", "device", "UUID="+testUUID, "fstype", "ext4",
		"config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Fatalf("an unresolvable device did not fail: result %v: %s", res.Result, res.Comment)
	}
	if res.HasChanges() {
		t.Errorf("a failed comparison reported changes: %v", res.Changes)
	}
	for _, want := range []string{"/dev/nvme0n1p2", "UUID=" + testUUID, "Nothing was remounted"} {
		if !strings.Contains(res.Comment, want) {
			t.Errorf("the comment does not name %q: %s", want, res.Comment)
		}
	}
	if ran := ranCommands(c); len(ran) != 0 {
		t.Errorf("an undecided comparison ran %v", ran)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an undecided comparison wrote the table")
	}
}

// Names that are not nodes are compared as names, as before: tmpfs is
// tmpfs, and is not a block device.
func TestFilesystemNamesAreComparedAsNames(t *testing.T) {
	_, _ = identityFixture(t, []string{"/dev/sda1"}, nil, "")
	for _, tc := range []struct {
		have, want string
		match      deviceMatch
	}{
		{"tmpfs", "tmpfs", deviceSame},
		{"tmpfs", "none", deviceDiffers},
		{"zroot/ROOT/default", "zroot/ROOT/other", deviceDiffers},
		{"server:/export", "/dev/sda1", deviceDiffers},
		{"/dev/sda1", "/dev/sda1", deviceSame},
		{"/dev/root", "/dev/sda1", deviceUndecided},
		{"/dev/sda1", "UUID=", deviceDiffers},
	} {
		if got := sameDevice(tc.have, tc.want); got != tc.match {
			t.Errorf("sameDevice(%q, %q) = %d, want %d", tc.have, tc.want, got, tc.match)
		}
	}
}

// mount.unmounted with a device filter matches by identity too. Before,
// a filter of UUID=... never equalled the node in the running table, so
// the state answered "Nothing is mounted" with the filesystem mounted.
func TestUnmountedByUUIDFindsTheNodeTheKernelNames(t *testing.T) {
	r := New()
	c, path := identityFixture(t,
		[]string{"/dev/nvme0n1p2"},
		map[string]string{"/dev/disk/by-uuid/" + testUUID: "../../nvme0n1p2"},
		// synthetic
		"/dev/nvme0n1p2 /srv/data ext4 rw,relatime 0 0\n")

	res, err := r.States.Call(c, "mount.unmounted", value.MapOf(
		"name", "/srv/data", "device", "UUID="+testUUID, "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("unmounting failed: %s", res.Comment)
	}
	assertRan(t, c, "umount /srv/data")
}
