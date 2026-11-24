package builtin

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The archive check's decision, on the numbers DIVERGENCE 5.729 read from
// the fleet's linux leg. The measurement itself needs root and a Debian
// archive, and runs in the leg; this is the half that can fail anywhere.
func TestTheKernelArchiveCheckRefusesTheMirrorOfIssue852(t *testing.T) {
	const pkg = "linux-image-7.0.0-1014-aws"
	for _, c := range []struct {
		what   string
		size   int64
		took   time.Duration
		refuse bool
	}{
		// A normal night: the removal test's 184 MB fetched and installed
		// in 35 s, so the fetch alone was at least this fast.
		{"a normal AWS runner", 184e6, 35 * time.Second, false},
		// The image this check fetches, at that rate.
		{"the probe on a normal runner", 16.7e6, 3 * time.Second, false},
		// 2026-11-24: the one removal test that finished, 1,208 s.
		{"the night's one slow pass", 184e6, 1100 * time.Second, true},
		// The probe at that night's rate, 170 kB/s.
		{"the probe on that night", 16.7e6, 98 * time.Second, true},
		// The threshold itself passes; a byte a second less does not.
		{"exactly the minimum", kernelArchiveMinRate * 10, 10 * time.Second, false},
		{"just under it", (kernelArchiveMinRate - 1) * 10, 10 * time.Second, true},
	} {
		note, err := judgeKernelArchive(pkg, c.size, c.took)
		if (err != nil) != c.refuse {
			t.Errorf("%s (%d bytes in %s): refused=%v, want %v: %q %v", c.what, c.size, c.took, err != nil, c.refuse, note, err)
			continue
		}
		if err != nil {
			for _, want := range []string{pkg, "kB/s", "370 MB", "5.729"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s: the refusal does not say %q: %v", c.what, want, err)
				}
			}
		} else if !strings.Contains(note, pkg) || !strings.Contains(note, "kB/s") {
			t.Errorf("%s: the note does not say what was measured: %q", c.what, note)
		}
	}

	// Nothing fetched is no measurement, and no reason to refuse.
	if note, err := judgeKernelArchive(pkg, 0, time.Second); err != nil || !strings.Contains(note, "not measured") {
		t.Errorf("nothing fetched: %q %v", note, err)
	}
}

// The measurement against a real apt, which is the half the test above
// cannot reach: that `apt-get download` fetches the running kernel's image
// into the directory it is run in, and that the check reads its size from
// there. It only downloads, into a temporary directory, so it needs
// HALITE_SYSTEM_LIVE and not HALITE_KERNELPKG_LIVE. It fails as the
// kernel tests would on an archive too slow for them; one that cannot be
// measured here is logged, not failed, because the check does not refuse
// on that either.
func TestLiveKernelpkgArchiveIsMeasuredWithoutInstallingAnything(t *testing.T) {
	_, f := kernelpkgLiveGate(t)
	if _, ok := f.(aptKernels); !ok {
		t.Skipf("the archive check measures apt's archive; this host's kernels are %s's", f.name())
	}
	before, _ := filepath.Glob("/boot/*")
	note, err := measureKernelArchive()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(note)
	if strings.Contains(note, "not measured") {
		t.Log("so the kernel tests on this host start unmeasured, behind the ten-minute limit alone")
	}
	if after, _ := filepath.Glob("/boot/*"); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Errorf("/boot changed while measuring: %v, then %v", before, after)
	}
}
