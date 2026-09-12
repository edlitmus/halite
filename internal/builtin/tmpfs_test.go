package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// tmpfsMountCapture is `mount`'s real answer on a FreeBSD 15.1-RELEASE-p3
// host, unedited.
const tmpfsMountCapture = `zroot/ROOT/default on / (zfs, local, noatime, nfsv4acls)
devfs on /dev (devfs)
linprocfs on /compat/linux/proc (linprocfs, local)
linsysfs on /compat/linux/sys (linsysfs, local)
tmpfs on /compat/linux/dev/shm (tmpfs, local)
`

// tmpfsDfCapture is `df -Pk`'s real answer on the same host, at the same
// moment, unedited.
//
// The mount point spelling for the very tmpfs above disagrees between
// the two: `mount` says /compat/linux/dev/shm, `df` says /dev/shm. Both
// tools were asked about the same filesystem and gave a different name
// for where it lives — a real consequence of this host's Linux
// compatibility layer giving `df` its own, translated view of the mount
// table. tmpfsEntries joins the two tables by exact mount-point string,
// so this is also the fixture for the case where that join finds
// nothing to attach: see
// TestListStillReportsAMountWhoseDfSpellingDisagrees below.
const tmpfsDfCapture = `Filesystem                           1024-blocks       Used   Available Capacity Mounted on
zroot/ROOT/default                    1696826240   38989756  1657836484       3% /
tmpfs                                          1          0           1       0% /dev/shm
`

func tmpfsContext(mountOut, dfOut string) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: map[string]exec.Result{
			"mount":  {Code: 0, Stdout: mountOut},
			"df -Pk": {Code: 0, Stdout: dfOut},
		}},
	}
}

// activeMounts prefers /proc/self/mounts, which this FreeBSD host does
// not have; forcing it to a path that does not exist is what sends
// every test in this file down the `mount`-parsing path the fixture
// above was captured for.
func tmpfsForceMountCommandFallback(t *testing.T) {
	t.Helper()
	old := ProcMountsPath
	ProcMountsPath = t.TempDir() + "/no-such-file"
	t.Cleanup(func() { ProcMountsPath = old })
}

func TestListFindsTheOneTmpfsAmongOtherFilesystems(t *testing.T) {
	tmpfsForceMountCommandFallback(t)
	c := tmpfsContext(tmpfsMountCapture, tmpfsDfCapture)
	out, err := tmpfsEntries(c)
	if err != nil {
		t.Fatalf("tmpfsEntries: %v", err)
	}
	if out.Len() != 1 {
		t.Fatalf("found %d tmpfs mounts, want 1 (zfs, devfs, linprocfs and linsysfs are not tmpfs): %v", out.Len(), out)
	}
	entry, ok := out.GetString("/compat/linux/dev/shm")
	if !ok {
		t.Fatalf("the tmpfs mount is not keyed by the mount point `mount` reported: %v", out)
	}
	m := entry.(*value.Map)
	if device, _ := m.GetString("device"); device != "tmpfs" {
		t.Errorf("device = %v, want tmpfs", device)
	}
	// mount.go's fstabEntry.asMap renders opts as a []string, and this
	// module passes that value through unchanged rather than re-flatten
	// it to a string mount.go has already decided not to use.
	opts, _ := m.GetString("opts")
	if list, ok := opts.([]string); !ok || len(list) != 1 || list[0] != "local" {
		t.Errorf("opts = %#v, want []string{\"local\"}", opts)
	}
}

// This is the real, observed consequence of the two captures above
// disagreeing on where the tmpfs lives: the mount is still reported,
// because `mount` is what says it exists, but with no usage figures,
// because nothing in `df`'s answer is keyed the way `mount`'s is. A
// silently wrong number would be worse than a missing one.
func TestListStillReportsAMountWhoseDfSpellingDisagrees(t *testing.T) {
	tmpfsForceMountCommandFallback(t)
	c := tmpfsContext(tmpfsMountCapture, tmpfsDfCapture)
	out, err := tmpfsEntries(c)
	if err != nil {
		t.Fatalf("tmpfsEntries: %v", err)
	}
	entry, _ := out.GetString("/compat/linux/dev/shm")
	m := entry.(*value.Map)
	for _, field := range []string{"1K-blocks", "used", "available", "capacity"} {
		if _, ok := m.GetString(field); ok {
			t.Errorf("field %q was set from a df row this mount point does not match: %v", field, m)
		}
	}
}

// Paths aligned by hand between the two real formats above, to exercise
// the case those formats agree — which is the ordinary one, on a host
// without this one's Linux-compatibility quirk.
func TestUsageJoinsFiguresWhenTheTwoToolsAgreeOnThePath(t *testing.T) {
	tmpfsForceMountCommandFallback(t)
	mountOut := "tmpfs on /dev/shm (tmpfs, local)\n"
	dfOut := "Filesystem     1024-blocks Used Available Capacity Mounted on\n" +
		"tmpfs                    1    0         1       0% /dev/shm\n"
	c := tmpfsContext(mountOut, dfOut)

	usage, err := tmpfsUsageFn(c, value.MapOf("name", "/dev/shm"))
	if err != nil {
		t.Fatalf("tmpfs.usage: %v", err)
	}
	m := usage.(*value.Map)
	if v, _ := m.GetString("1K-blocks"); v != int64(1) {
		t.Errorf("1K-blocks = %v, want 1", v)
	}
	if v, _ := m.GetString("capacity"); v != "0%" {
		t.Errorf("capacity = %v, want 0%%", v)
	}
}

func TestIsMountedIsTrueOnlyForTheTmpfsOne(t *testing.T) {
	tmpfsForceMountCommandFallback(t)
	c := tmpfsContext(tmpfsMountCapture, tmpfsDfCapture)

	tmpfsMounted, err := tmpfsIsMountedFn(c, value.MapOf("name", "/compat/linux/dev/shm"))
	if err != nil || tmpfsMounted != true {
		t.Errorf("tmpfs.is_mounted(/compat/linux/dev/shm) = %v, %v, want true", tmpfsMounted, err)
	}

	zfsMounted, err := tmpfsIsMountedFn(c, value.MapOf("name", "/"))
	if err != nil || zfsMounted != false {
		t.Errorf("tmpfs.is_mounted(/) = %v, %v, want false; / is zfs, not tmpfs", zfsMounted, err)
	}

	absent, err := tmpfsIsMountedFn(c, value.MapOf("name", "/nowhere"))
	if err != nil || absent != false {
		t.Errorf("tmpfs.is_mounted(/nowhere) = %v, %v, want false", absent, err)
	}
}

func TestUsageNamesTheRealFilesystemWhenAskedAboutANonTmpfsMount(t *testing.T) {
	tmpfsForceMountCommandFallback(t)
	c := tmpfsContext(tmpfsMountCapture, tmpfsDfCapture)

	_, err := tmpfsUsageFn(c, value.MapOf("name", "/"))
	if err == nil {
		t.Fatal("tmpfs.usage(/) should refuse a zfs mount rather than answer for it")
	}
	if !strings.Contains(err.Error(), "zfs") {
		t.Errorf("the refusal does not name the real filesystem: %v", err)
	}
}

func TestUsageOnAnUnmountedPathSaysSoRatherThanNamingAFilesystem(t *testing.T) {
	tmpfsForceMountCommandFallback(t)
	c := tmpfsContext(tmpfsMountCapture, tmpfsDfCapture)

	_, err := tmpfsUsageFn(c, value.MapOf("name", "/nowhere"))
	if err == nil {
		t.Fatal("tmpfs.usage(/nowhere) should have failed")
	}
	if strings.Contains(err.Error(), "mounted, but as") {
		t.Errorf("a path with nothing mounted there was reported as mounted: %v", err)
	}
}

func TestSignaturesDeclareUnixPlatformsAndSection15Point2(t *testing.T) {
	r := &Registries{Exec: exec.NewRegistry()}
	registerTmpfs(r)
	for _, name := range []string{"tmpfs.list", "tmpfs.is_mounted", "tmpfs.usage"} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Fatalf("%s was not registered", name)
		}
		if sig.Section != "15.2" {
			t.Errorf("%s.Section = %q, want 15.2", name, sig.Section)
		}
		if sig.Mutates {
			t.Errorf("%s.Mutates = true; this module only reads", name)
		}
		if len(sig.Platforms) == 0 {
			t.Errorf("%s declares no Platforms; tmpfs is not a Windows concept", name)
		}
	}
}
