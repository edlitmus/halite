package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `kernelpkg.latest_installed` and `kernelpkg.latest_wait` through SPEC
// 11.6's harness.
//
// `latest_installed` needs the newest kernel the repositories offer to
// be absent, and on a host that already has it the only way there is to
// take it away. Setup does that directly with apt -- the image and its
// unsigned twin, which on Debian also removes the metapackages that
// depend on it -- and only when that kernel is not the running one; the
// case is unavailable otherwise, and on a host where the running kernel
// is the newest there is nothing it may take. Cleanup puts the whole
// linux-image and linux-headers set back at the versions and automatic
// marks it found, and prints what it did, because a Cleanup has no test
// to fail.
//
// `latest_active` has no case. Its change is a scheduled reboot, and the
// lab hosts and fleet runners this harness runs on are not to be
// rebooted; see `unconformed`.

func kernelpkgConformanceCases() []liveCase {
	var (
		once sync.Once
		snap debianKernelSnapshot
		err0 error
	)
	ctx := func() *hexec.Context {
		c := liveRoot()
		if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			c.Grains = value.MapOf("kernelrelease", strings.TrimSpace(string(b)))
		}
		return c
	}
	newest := func() (string, string) {
		c := ctx()
		f, err := pickKernelFamily(c)
		if err != nil {
			return "", err.Error()
		}
		if _, ok := f.(aptKernels); !ok {
			return "", "this case takes a Debian kernel away and puts it back; this host's kernels are " + f.name() + "'s"
		}
		s, err := readKernels(c)
		if err != nil {
			return "", err.Error()
		}
		if _, ok := s.find(s.active); !ok {
			return "", "the running kernel " + s.active + " is not a package here (a container runs its host's)"
		}
		avail, ok, err := s.family.available(c)
		if err != nil || !ok {
			return "", fmt.Sprintf("no newest kernel is known here: %v", err)
		}
		if avail.Release == s.active {
			return "", "the newest kernel offered, " + avail.Release + ", is the running one, which this case may not take away"
		}
		return avail.Release, ""
	}
	probe := func() (string, error) {
		files, _ := filepath.Glob("/boot/vmlinuz-*")
		sort.Strings(files)
		return strings.Join(files, " "), nil
	}
	return []liveCase{
		{
			platforms: kernelpkgPlatforms,
			needs:     []string{"apt-get", "dpkg-query"},
			unavailable: func(c *hexec.Context) string {
				if os.Getenv("HALITE_KERNELPKG_LIVE") != "1" {
					return "this case removes and reinstalls a kernel; set HALITE_KERNELPKG_LIVE=1 as well to allow that"
				}
				_, why := newest()
				return why
			},
			Conformance: states.Conformance{
				Name: "kernelpkg.latest_installed",
				Args: value.MapOf("name", "newest-kernel"),
				Setup: func() error {
					once.Do(func() { snap, err0 = readDebianKernelSnapshot() })
					if err0 != nil {
						return err0
					}
					release, why := newest()
					if why != "" {
						return fmt.Errorf("%s", why)
					}
					return takeAwayDebianKernel(release)
				},
				Probe: probe,
				Cleanup: func() {
					if snap.versions == nil {
						return
					}
					did, err := snap.putBack()
					if err != nil {
						fmt.Fprintf(os.Stderr, "kernelpkg.latest_installed conformance cleanup: %v\n", err)
						return
					}
					fmt.Fprintf(os.Stderr, "kernelpkg.latest_installed conformance cleanup put the kernel set back (%s)\n", did)
				},
			},
		},
		{
			platforms: kernelpkgPlatforms,
			Conformance: states.Conformance{
				Name:             "kernelpkg.latest_wait",
				Args:             value.MapOf("name", "boot-newest"),
				Probe:            probe,
				Unchanging:       true,
				UnchangingReason: "it acts only when a watch or listen requisite fires, and none does here",
			},
		},
	}
}
