package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The twelve storage states, through SPEC 11.6's harness, on storage this
// suite makes for itself.
//
// # Why none of it can be faked
//
// `pvs` reads a label off a disk, `vgcreate` writes group metadata,
// `lvcreate` asks device-mapper for a node under /dev, `zpool create` writes
// a pool label, and `mount` changes what the kernel has mounted. A container
// shares the host's device-mapper and its mount namespace is not the
// machine's, so a test there reaches the developer's own system.
//
// What it uses instead:
//
//   - **LVM** on two loop devices over backing files in a temporary
//     directory, the rig `live_lvm_loopback_test.go` established.
//   - **ZFS** on a single file vdev. ZFS takes a file as a vdev natively, so
//     no loop device is needed at all; the pool's mountpoint is redirected
//     into the temporary directory so nothing appears at `/<pool>`.
//   - **mount** on `tmpfs`, which is a real mount needing no block device
//     and no filesystem to be written. Its table is a `config` file of this
//     suite's own, so `/etc/fstab` is never touched.
//
// # Why each case builds and destroys its own rig
//
// The harness runs a case's `Setup` before test mode and again before the
// apply, so a rig built once and shared would be in whatever state the
// previous phase left it. Each `Setup` therefore brings the machine to "not
// yet applied" from wherever it is, and each `Cleanup` removes what the case
// made. Attaching a loop device costs milliseconds; a wrong assumption about
// shared state costs a defect that only appears on the second phase.
//
// The rig object itself is created at case-construction time and does
// nothing then. It holds the names, and every command it runs happens inside
// a Setup, a Probe or a Cleanup -- the rule the whole live case list follows,
// because the unit suite builds this list to count what is covered.
//
// # How a case learns an argument it cannot know yet
//
// A loop device's name and a temporary directory's path are not known until
// Setup has run, and `Conformance.Args` is a field read when the state is
// called -- which is after Setup. So a case whose arguments depend on its rig
// builds an empty `*value.Map` at construction and its Setup fills the
// discovered values in. The harness sees the same pointer, so it sees them.
// No change to the harness, and nothing is computed at construction.
//
// DIVERGENCE 5.157.

// storageRig is the names and devices one case works with.
type storageRig struct {
	c *hexec.Context
	r *Registries
	// dir holds the backing files. Made on first use rather than at
	// construction, because construction must touch nothing.
	dir string
	// loops are the loop devices currently attached for this rig.
	loops []string
	vg    string
	pool  string
}

func newStorageRig() *storageRig {
	return &storageRig{
		c:    liveRoot(),
		r:    New(),
		vg:   liveConformancePrefix + "vg",
		pool: liveConformancePrefix + "pool",
	}
}

// run is a command whose failure is the caller's to interpret.
func (s *storageRig) run(argv ...string) (string, error) {
	res, err := s.c.Run(hexec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("%s exited %d: %s", argv[0], res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

// quiet runs a command whose failure is ordinary -- removing something that
// is not there.
func (s *storageRig) quiet(argv ...string) {
	_, _ = s.c.Run(hexec.Command{Argv: argv, IgnoreExitCode: true})
}

// workDir makes the directory the backing files live in, once.
func (s *storageRig) workDir() (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	dir, err := os.MkdirTemp("", liveConformancePrefix+"-storage-")
	if err != nil {
		return "", err
	}
	s.dir = dir
	return dir, nil
}

// sparseFile makes a backing file of the given size, replacing any previous
// one so that a second Setup starts from the same place.
func (s *storageRig) sparseFile(name string, bytes int64) (string, error) {
	dir, err := s.workDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	_ = os.Remove(path)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Truncate(bytes); err != nil {
		return "", err
	}
	return path, nil
}

// ---- LVM ----

// loopDevice attaches a loop device for this rig, reusing the one it already
// has. Reuse is what makes a second Setup safe: attaching again each time
// would leak a device per phase, and the loop driver is shared with the rest
// of the machine.
func (s *storageRig) loopDevice() (string, error) {
	if len(s.loops) > 0 {
		return s.loops[0], nil
	}
	img, err := s.sparseFile("pv0.img", 96<<20)
	if err != nil {
		return "", err
	}
	out, err := s.run("losetup", "--find", "--show", img)
	if err != nil {
		return "", err
	}
	dev := strings.TrimSpace(out)
	if dev == "" {
		return "", fmt.Errorf("losetup attached a device and did not name it")
	}
	s.loops = append(s.loops, dev)
	return dev, nil
}

// clearLVM removes anything this rig made, in reverse order. Every step is
// quiet: "not there" is the ordinary case, and a teardown that stopped on
// the first absence would leave a loop device attached.
func (s *storageRig) clearLVM() {
	s.quiet("lvremove", "-f", s.vg)
	s.quiet("vgremove", "-f", s.vg)
	for _, dev := range s.loops {
		s.quiet("pvremove", "-ff", "-y", dev)
	}
}

func (s *storageRig) teardownLVM() {
	s.clearLVM()
	for _, dev := range s.loops {
		s.quiet("losetup", "-d", dev)
	}
	s.loops = nil
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
		s.dir = ""
	}
}

// pvProbe reports whether a device carries an LVM label, read from `pvs`
// rather than from the module's own answer.
func (s *storageRig) pvProbe(dev *string) func() (string, error) {
	return func() (string, error) {
		if *dev == "" {
			return "no device", nil
		}
		out, err := s.c.Run(hexec.Command{
			Argv:           []string{"pvs", "--noheadings", "-o", "pv_name,vg_name", *dev},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if out.Code != 0 {
			return "no pv", nil
		}
		return strings.Join(strings.Fields(out.Stdout), " "), nil
	}
}

func (s *storageRig) vgProbe() func() (string, error) {
	return func() (string, error) {
		out, err := s.c.Run(hexec.Command{
			Argv:           []string{"vgs", "--noheadings", "-o", "vg_name,pv_count", s.vg},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if out.Code != 0 {
			return "no vg", nil
		}
		return strings.Join(strings.Fields(out.Stdout), " "), nil
	}
}

func (s *storageRig) lvProbe(lv string) func() (string, error) {
	return func() (string, error) {
		out, err := s.c.Run(hexec.Command{
			Argv:           []string{"lvs", "--noheadings", "-o", "lv_name,lv_size", s.vg + "/" + lv},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if out.Code != 0 {
			return "no lv", nil
		}
		return strings.Join(strings.Fields(out.Stdout), " "), nil
	}
}

func lvmCases() []liveCase {
	// One rig per case. They are independent, and a shared one would carry
	// the previous case's group into the next.
	lvmTools := []string{"losetup", "pvs", "vgs", "lvs", "pvcreate", "vgcreate",
		"lvcreate", "lvremove", "vgremove", "pvremove"}
	linux := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = lvmTools
		return lc
	}

	const lv = "data"
	var cases []liveCase

	// pv_present: a device with no label, which the state must label.
	pvOn := newStorageRig()
	pvOnDev := ""
	pvOnArgs := value.NewMap(2)
	cases = append(cases, linux(liveCase{Conformance: states.Conformance{
		Name: "lvm.pv_present",
		Args: pvOnArgs,
		Setup: func() error {
			dev, err := pvOn.loopDevice()
			if err != nil {
				return err
			}
			pvOnDev = dev
			pvOnArgs.Set("name", dev)
			pvOn.clearLVM()
			return nil
		},
		Probe:   pvOn.pvProbe(&pvOnDev),
		Cleanup: pvOn.teardownLVM,
	}}))

	// pv_absent: a labelled device, which the state must wipe.
	pvOff := newStorageRig()
	pvOffDev := ""
	pvOffArgs := value.MapOf("force", true)
	cases = append(cases, linux(liveCase{Conformance: states.Conformance{
		Name: "lvm.pv_absent",
		Args: pvOffArgs,
		Setup: func() error {
			dev, err := pvOff.loopDevice()
			if err != nil {
				return err
			}
			pvOffDev = dev
			pvOffArgs.Set("name", dev)
			pvOff.clearLVM()
			_, err = pvOff.run("pvcreate", "-ff", "-y", dev)
			return err
		},
		Probe:   pvOff.pvProbe(&pvOffDev),
		Cleanup: pvOff.teardownLVM,
	}}))

	vgOn := newStorageRig()
	vgOnArgs := value.MapOf("name", vgOn.vg, "force", true)
	cases = append(cases, linux(liveCase{Conformance: states.Conformance{
		Name: "lvm.vg_present",
		Args: vgOnArgs,
		Setup: func() error {
			dev, err := vgOn.loopDevice()
			if err != nil {
				return err
			}
			vgOnArgs.Set("devices", []any{dev})
			vgOn.clearLVM()
			return nil
		},
		Probe:   vgOn.vgProbe(),
		Cleanup: vgOn.teardownLVM,
	}}))

	vgOff := newStorageRig()
	cases = append(cases, linux(liveCase{Conformance: states.Conformance{
		Name: "lvm.vg_absent",
		Args: value.MapOf("name", vgOff.vg, "force", true),
		Setup: func() error {
			dev, err := vgOff.loopDevice()
			if err != nil {
				return err
			}
			vgOff.clearLVM()
			if _, err := vgOff.run("pvcreate", "-ff", "-y", dev); err != nil {
				return err
			}
			_, err = vgOff.run("vgcreate", vgOff.vg, dev)
			return err
		},
		Probe:   vgOff.vgProbe(),
		Cleanup: vgOff.teardownLVM,
	}}))

	lvOn := newStorageRig()
	cases = append(cases, linux(liveCase{Conformance: states.Conformance{
		Name: "lvm.lv_present",
		Args: value.MapOf("name", lv, "vgname", lvOn.vg, "size", "16M"),
		Setup: func() error {
			return lvOn.freshGroup()
		},
		Probe:   lvOn.lvProbe(lv),
		Cleanup: lvOn.teardownLVM,
	}}))

	lvOff := newStorageRig()
	cases = append(cases, linux(liveCase{Conformance: states.Conformance{
		Name: "lvm.lv_absent",
		Args: value.MapOf("name", lv, "vgname", lvOff.vg),
		Setup: func() error {
			if err := lvOff.freshGroup(); err != nil {
				return err
			}
			_, err := lvOff.run("lvcreate", "-y", "-L", "16M", "-n", lv, lvOff.vg)
			return err
		},
		Probe:   lvOff.lvProbe(lv),
		Cleanup: lvOff.teardownLVM,
	}}))

	return cases
}

// freshGroup leaves a volume group with no logical volumes in it.
func (s *storageRig) freshGroup() error {
	dev, err := s.loopDevice()
	if err != nil {
		return err
	}
	s.clearLVM()
	if _, err := s.run("pvcreate", "-ff", "-y", dev); err != nil {
		return err
	}
	_, err = s.run("vgcreate", s.vg, dev)
	return err
}

// ---- ZFS ----
//
// Gated on the tools rather than on the platform: ZFS is in FreeBSD's base
// system and is a separate package on Linux, so a row that has it runs these
// and a row that does not says so. FreeBSD is where they are expected to
// run, which is the point -- it is tier 1 and carries most of the estate.

func (s *storageRig) destroyPool() {
	s.quiet("zpool", "destroy", "-f", s.pool)
}

func (s *storageRig) teardownZFS() {
	s.destroyPool()
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
		s.dir = ""
	}
}

// vdev is the file the pool is built on. 128M because ZFS refuses a vdev
// under 64M and leaves little usable at exactly that.
func (s *storageRig) vdev() (string, error) {
	return s.sparseFile("vdev.img", 128<<20)
}

// createPool builds the pool on a fresh file, with its mountpoint inside the
// working directory so that nothing appears at /<pool>.
func (s *storageRig) createPool() error {
	s.destroyPool()
	img, err := s.vdev()
	if err != nil {
		return err
	}
	mnt := filepath.Join(s.dir, "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	_, err = s.run("zpool", "create", "-f", "-m", mnt, s.pool, img)
	return err
}

func (s *storageRig) poolProbe() func() (string, error) {
	return func() (string, error) {
		out, err := s.c.Run(hexec.Command{
			Argv:           []string{"zpool", "list", "-H", "-o", "name,health", s.pool},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if out.Code != 0 {
			return "no pool", nil
		}
		return strings.Join(strings.Fields(out.Stdout), " "), nil
	}
}

func (s *storageRig) datasetProbe(dataset string) func() (string, error) {
	return func() (string, error) {
		out, err := s.c.Run(hexec.Command{
			Argv:           []string{"zfs", "list", "-H", "-o", "name,mounted", dataset},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if out.Code != 0 {
			return "no dataset", nil
		}
		return strings.Join(strings.Fields(out.Stdout), " "), nil
	}
}

func zfsCases() []liveCase {
	zfsTools := []string{"zpool", "zfs"}
	withZFS := func(lc liveCase) liveCase {
		lc.needs = zfsTools
		return lc
	}
	var cases []liveCase

	poolOn := newStorageRig()
	poolOnArgs := value.MapOf("name", poolOn.pool, "force", true)
	cases = append(cases, withZFS(liveCase{Conformance: states.Conformance{
		Name: "zpool.present",
		Args: poolOnArgs,
		Setup: func() error {
			poolOn.destroyPool()
			// The vdev is replaced so that a second Setup does not find a
			// file already carrying a pool label.
			img, err := poolOn.vdev()
			if err != nil {
				return err
			}
			mnt := filepath.Join(poolOn.dir, "mnt")
			if err := os.MkdirAll(mnt, 0o755); err != nil {
				return err
			}
			poolOnArgs.Set("layout", []any{img})
			poolOnArgs.Set("mountpoint", mnt)
			return nil
		},
		Probe:   poolOn.poolProbe(),
		Cleanup: poolOn.teardownZFS,
	}}))

	poolOff := newStorageRig()
	cases = append(cases, withZFS(liveCase{Conformance: states.Conformance{
		Name:    "zpool.absent",
		Args:    value.MapOf("name", poolOff.pool, "force", true),
		Setup:   poolOff.createPool,
		Probe:   poolOff.poolProbe(),
		Cleanup: poolOff.teardownZFS,
	}}))

	fsOn := newStorageRig()
	cases = append(cases, withZFS(liveCase{Conformance: states.Conformance{
		Name: "zfs.filesystem_present",
		Args: value.MapOf("name", liveConformancePrefix+"pool/data"),
		Setup: func() error {
			if err := fsOn.createPool(); err != nil {
				return err
			}
			fsOn.quiet("zfs", "destroy", "-r", fsOn.pool+"/data")
			return nil
		},
		Probe:   fsOn.datasetProbe(liveConformancePrefix + "pool/data"),
		Cleanup: fsOn.teardownZFS,
	}}))

	fsOff := newStorageRig()
	cases = append(cases, withZFS(liveCase{Conformance: states.Conformance{
		Name: "zfs.absent",
		Args: value.MapOf("name", liveConformancePrefix+"pool/data", "recursive", true),
		Setup: func() error {
			if err := fsOff.createPool(); err != nil {
				return err
			}
			_, err := fsOff.run("zfs", "create", fsOff.pool+"/data")
			return err
		},
		Probe:   fsOff.datasetProbe(liveConformancePrefix + "pool/data"),
		Cleanup: fsOff.teardownZFS,
	}}))

	return cases
}

// ---- mount ----
//
// `tmpfs`, which is a real mount the kernel performs and needs no block
// device and no filesystem to be written. The table is a `config` file of
// this suite's own, so `/etc/fstab` is never read or written -- which is what
// makes these safe on a machine somebody else owns, and is also the argument
// for the parameter existing.

func (s *storageRig) mountPoint() (string, error) {
	dir, err := s.workDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mnt"), nil
}

func (s *storageRig) mountTable() (string, error) {
	dir, err := s.workDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fstab"), nil
}

// mountProbe reports whether the kernel has this path mounted, asked of the
// system rather than of the table: an entry in a file is not a mount.
func (s *storageRig) mountProbe(point *string) func() (string, error) {
	return func() (string, error) {
		if *point == "" {
			return "no mount point", nil
		}
		out, err := s.c.Run(hexec.Command{
			Argv:           []string{"mount"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(out.Stdout, "\n") {
			if strings.Contains(line, *point) {
				return "mounted: " + strings.TrimSpace(line), nil
			}
		}
		return "not mounted", nil
	}
}

func (s *storageRig) unmountAll(point string) {
	if point == "" {
		return
	}
	s.quiet("umount", point)
	s.quiet("umount", "-f", point)
}

func mountCases() []liveCase {
	// tmpfs is Linux's and FreeBSD's; both spell the type the same way.
	tmpfsPlatforms := []string{"linux", "freebsd"}
	var cases []liveCase

	up := newStorageRig()
	upPoint := ""
	upArgs := value.MapOf("device", "tmpfs", "fstype", "tmpfs",
		"opts", "size=1m", "mkmnt", true)
	cases = append(cases, liveCase{
		platforms: tmpfsPlatforms,
		needs:     []string{"mount", "umount"},
		Conformance: states.Conformance{
			Name: "mount.mounted",
			Args: upArgs,
			Setup: func() error {
				point, err := up.mountPoint()
				if err != nil {
					return err
				}
				upPoint = point
				up.unmountAll(point)
				if err := os.MkdirAll(point, 0o755); err != nil {
					return err
				}
				table, err := up.mountTable()
				if err != nil {
					return err
				}
				if err := os.WriteFile(table, []byte("# halite conformance\n"), 0o644); err != nil {
					return err
				}
				upArgs.Set("name", point)
				upArgs.Set("config", table)
				return nil
			},
			Probe: up.mountProbe(&upPoint),
			Cleanup: func() {
				up.unmountAll(upPoint)
				if up.dir != "" {
					_ = os.RemoveAll(up.dir)
					up.dir = ""
				}
			},
		},
	})

	down := newStorageRig()
	downPoint := ""
	downArgs := value.MapOf("device", "tmpfs", "force", true)
	cases = append(cases, liveCase{
		platforms: tmpfsPlatforms,
		needs:     []string{"mount", "umount"},
		Conformance: states.Conformance{
			Name: "mount.unmounted",
			Args: downArgs,
			Setup: func() error {
				point, err := down.mountPoint()
				if err != nil {
					return err
				}
				downPoint = point
				down.unmountAll(point)
				if err := os.MkdirAll(point, 0o755); err != nil {
					return err
				}
				table, err := down.mountTable()
				if err != nil {
					return err
				}
				if err := os.WriteFile(table, []byte("# halite conformance\n"), 0o644); err != nil {
					return err
				}
				if _, err := down.run("mount", "-t", "tmpfs", "-o", "size=1m", "tmpfs", point); err != nil {
					return err
				}
				downArgs.Set("name", point)
				downArgs.Set("config", table)
				return nil
			},
			Probe: down.mountProbe(&downPoint),
			Cleanup: func() {
				down.unmountAll(downPoint)
				if down.dir != "" {
					_ = os.RemoveAll(down.dir)
					down.dir = ""
				}
			},
		},
	})

	return cases
}

func storageCases() []liveCase {
	var cases []liveCase
	cases = append(cases, lvmCases()...)
	cases = append(cases, zfsCases()...)
	cases = append(cases, mountCases()...)
	return cases
}
