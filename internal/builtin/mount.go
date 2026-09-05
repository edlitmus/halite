package builtin

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// unixOnly names the platforms that have a mount table and an fstab.
//
// Windows is left out rather than given a translation. Its mount points
// are drive letters and directory junctions, its filesystems are attached
// by the volume manager from a store that is not a text file, and there
// is no `mount` to run: a mount.mounted that pretended otherwise would be
// a state that reads plausibly and does nothing.
var unixOnly = []string{"linux", "freebsd", "openbsd", "netbsd", "dragonfly", "darwin"}

// FstabPath is the mount table, as a variable so a test can point it
// somewhere harmless.
var FstabPath = "/etc/fstab"

// ProcMountsPath is where Linux publishes the running mount table.
var ProcMountsPath = "/proc/self/mounts"

// registerMount installs the mount module of SPEC sections 15.2 and 15.5.
//
// A mount has the same two states a sysctl has: what is mounted now, and
// what will be mounted after a reboot. mount.mounted manages both, and
// they are separate reads — the running table comes from the kernel and
// the other from /etc/fstab — because a node where the two disagree is
// exactly the node an operator needs told about.
func registerMount(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "active",
				Doc:       "Return the mounted filesystems.",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				table, _, err := activeMounts(c)
				return table, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "is_mounted",
				Doc: "Report whether anything is mounted at a path.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				active, _, err := activeMountAt(c, states.Str(args, "name", ""))
				return active != nil, err
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "fstab",
				Doc: "Return the mount table this node comes up with.",
				Params: []signature.Param{
					opt("config", signature.Path, "", "The table; defaults to /etc/fstab."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				entries, err := readFstab(fstabPathOf(args))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(entries))
				for _, e := range entries {
					out.Set(e.Point, e.asMap())
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "set_fstab",
				Doc: "Write a mount into the table, and report whether it was new, changed or already there.",
				Params: append(fstabParams(),
					opt("config", signature.Path, "", "The table; defaults to /etc/fstab.")),
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				want, err := entryFromArgs(args)
				if err != nil {
					return nil, err
				}
				return setFstab(c, fstabPathOf(args), want)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "rm_fstab",
				Doc: "Take a mount out of the table.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
					opt("device", signature.String, "", "Only remove the entry if it names this device."),
					opt("config", signature.Path, "", "The table; defaults to /etc/fstab."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return rmFstab(c, fstabPathOf(args),
					states.Str(args, "name", ""), states.Str(args, "device", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "mount",
				Doc: "Mount a filesystem.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
					req("device", signature.String, "What to mount."),
					opt("mkmnt", signature.Bool, false, "Create the mount point if it is not there."),
					opt("fstype", signature.String, "", "The filesystem type."),
					opt("opts", signature.String, "defaults", "Mount options, comma separated."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, mountFilesystem(c, mountArgs(args), false)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "remount",
				Doc: "Mount a filesystem again, in place, to pick up changed options.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
					req("device", signature.String, "What is mounted there."),
					opt("mkmnt", signature.Bool, false, "Create the mount point if it is not there."),
					opt("fstype", signature.String, "", "The filesystem type."),
					opt("opts", signature.String, "defaults", "Mount options, comma separated."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, mountFilesystem(c, mountArgs(args), true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "umount",
				Doc: "Unmount a filesystem.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
					opt("force", signature.Bool, false, "Unmount even while it is in use."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, umountFilesystem(c,
					states.Str(args, "name", ""), states.Bool(args, "force", false))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "swaps",
				Doc:       "Return the active swap devices and files.",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return activeSwaps(c)
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "mounted",
				Doc: "Ensure a filesystem is mounted now and after a reboot.",
				Params: append(append([]signature.Param{
					nameParam("The mount point. Defaults to the state ID."),
				}, fstabParams()[1:]...),
					opt("mkmnt", signature.Bool, false, "Create the mount point if it is not there."),
					opt("config", signature.Path, "", "The table; defaults to /etc/fstab."),
					opt("persist", signature.Bool, true, "Keep the entry in the table, so the mount survives a reboot."),
					opt("mount", signature.Bool, true, "Mount it now. False manages only the table."),
				),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.5",
			},
			Fn: mountMounted,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "mount", Function: "unmounted",
				Doc: "Ensure nothing is mounted at a path.",
				Params: []signature.Param{
					nameParam("The mount point. Defaults to the state ID."),
					opt("device", signature.String, "", "Only act on the entry naming this device."),
					opt("config", signature.Path, "", "The table; defaults to /etc/fstab."),
					opt("persist", signature.Bool, false, "Also take the entry out of the table."),
					opt("force", signature.Bool, false, "Unmount even while it is in use."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.5",
			},
			Fn: mountUnmounted,
		},
	)
}

// fstabParams are the fields of a table entry, shared by the exec
// function that writes one and the state that manages one.
func fstabParams() []signature.Param {
	return []signature.Param{
		req("name", signature.Path, "The mount point."),
		req("device", signature.String, "What to mount: a path, a UUID= or a LABEL=."),
		opt("fstype", signature.String, "", "The filesystem type."),
		opt("opts", signature.String, "defaults", "Mount options, comma separated."),
		opt("dump", signature.String, "0", "The dump field."),
		opt("pass_num", signature.String, "0", "The fsck pass field."),
	}
}

func fstabPathOf(args *value.Map) string {
	if p := states.Str(args, "config", ""); p != "" {
		return p
	}
	return FstabPath
}

// ---- the table ----

// fstabEntry is one line of the mount table.
type fstabEntry struct {
	Device string
	Point  string
	Type   string
	Opts   string
	Dump   string
	Pass   string
}

func (e fstabEntry) asMap() *value.Map {
	return value.MapOf(
		"device", e.Device,
		"fstype", e.Type,
		"opts", splitOpts(e.Opts),
		"dump", e.Dump,
		"pass", e.Pass,
	)
}

// line renders the entry as fstab holds it, with the fields padded the
// way every shipped table is padded so that a hand-edited file and a
// written one look alike.
func (e fstabEntry) line() string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s %s",
		escapeFstab(e.Device), escapeFstab(e.Point), e.Type, e.Opts, e.Dump, e.Pass)
}

// sameAs compares two entries as fstab means them: the option list is a
// set, because `defaults,noatime` and `noatime,defaults` mount the same
// filesystem and a state that reported a change between them would report
// one for ever.
func (e fstabEntry) sameAs(o fstabEntry) bool {
	return e.Device == o.Device && e.Point == o.Point && e.Type == o.Type &&
		sameOptionSet(e.Opts, o.Opts) &&
		numField(e.Dump) == numField(o.Dump) && numField(e.Pass) == numField(o.Pass)
}

// numField compares a dump or pass field numerically, so that an entry
// written "0" and one written "00" are not a change.
func numField(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func entryFromArgs(args *value.Map) (fstabEntry, error) {
	e := fstabEntry{
		Device: states.Str(args, "device", ""),
		Point:  states.Str(args, "name", ""),
		Type:   states.Str(args, "fstype", ""),
		Opts:   states.Str(args, "opts", "defaults"),
		Dump:   states.Str(args, "dump", "0"),
		Pass:   states.Str(args, "pass_num", "0"),
	}
	if e.Point == "" || e.Device == "" {
		return e, fmt.Errorf("a table entry needs a mount point and a device")
	}
	if e.Opts == "" {
		e.Opts = "defaults"
	}
	return e, nil
}

// readFstab parses the table. A line it cannot read is skipped rather
// than guessed at: a table this build does not understand is one an
// operator wrote by hand, and rewriting it from a guess is worse than
// leaving it be.
func readFstab(path string) ([]fstabEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []fstabEntry
	for _, line := range strings.Split(string(b), "\n") {
		if e, ok := parseFstabLine(line); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func parseFstabLine(line string) (fstabEntry, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return fstabEntry{}, false
	}
	f := strings.Fields(trimmed)
	// OpenBSD and macOS write four fields where Linux writes six; the
	// first four are the same everywhere.
	if len(f) < 4 {
		return fstabEntry{}, false
	}
	e := fstabEntry{
		Device: unescapeFstab(f[0]),
		Point:  unescapeFstab(f[1]),
		Type:   f[2],
		Opts:   f[3],
		Dump:   "0",
		Pass:   "0",
	}
	if len(f) > 4 {
		e.Dump = f[4]
	}
	if len(f) > 5 {
		e.Pass = f[5]
	}
	return e, true
}

// setFstab writes the entry, and says which of the three things it did.
func setFstab(c *exec.Context, path string, want fstabEntry) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var lines []string
	if len(b) > 0 {
		lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}

	result := "new"
	replaced := false
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		have, ok := parseFstabLine(line)
		if !ok || have.Point != want.Point {
			out = append(out, line)
			continue
		}
		if replaced {
			// A second entry for the same point is one the kernel would
			// mount over; keeping it would leave the table saying two
			// things and this state reporting a change on every run.
			result = "change"
			continue
		}
		replaced = true
		if have.sameAs(want) {
			result = "present"
			out = append(out, line)
			continue
		}
		result = "change"
		out = append(out, want.line())
	}
	if !replaced {
		out = append(out, want.line())
	}
	if result == "present" || c.Test {
		return result, nil
	}
	if err := writeAtomic(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return result, nil
}

// rmFstab takes the entry out, and reports whether there was one.
func rmFstab(c *exec.Context, path, point, device string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := make([]string, 0, len(lines))
	removed := false
	for _, line := range lines {
		have, ok := parseFstabLine(line)
		if ok && have.Point == point && (device == "" || have.Device == device) {
			removed = true
			continue
		}
		out = append(out, line)
	}
	if !removed || c.Test {
		return removed, nil
	}
	body := ""
	if len(out) > 0 {
		body = strings.Join(out, "\n") + "\n"
	}
	return true, writeAtomic(path, []byte(body), 0o644)
}

// fstab escapes a space, a tab, a newline and a backslash as octal, and
// nothing else. Getmntent does the same, which is why a mount point with
// a space in it is readable at all.
var fstabEscapes = []struct{ ch, esc string }{
	{`\`, `\134`}, {" ", `\040`}, {"\t", `\011`}, {"\n", `\012`},
}

func escapeFstab(s string) string {
	for _, e := range fstabEscapes {
		s = strings.ReplaceAll(s, e.ch, e.esc)
	}
	return s
}

func unescapeFstab(s string) string {
	// Backwards, so that a literal backslash written \134 does not have
	// its own replacement re-read as the start of another escape.
	for i := len(fstabEscapes) - 1; i >= 0; i-- {
		s = strings.ReplaceAll(s, fstabEscapes[i].esc, fstabEscapes[i].ch)
	}
	return s
}

// ---- the running table ----

// activeMounts reads what is mounted now, and says whether the answer
// came from the kernel's own table or from parsing `mount`. The
// difference decides how much of a declaration can be compared against
// it: see contradictedOptions.
func activeMounts(c *exec.Context) (table *value.Map, fromKernel bool, err error) {
	if entries, err := readProcMounts(); err == nil && len(entries) > 0 {
		out := value.NewMap(len(entries))
		for _, e := range entries {
			out.Set(e.Point, e.asMap())
		}
		return out, true, nil
	}
	out, err := mountCommandOutput(c)
	return out, false, err
}

func activeMountAt(c *exec.Context, point string) (entry *value.Map, fromKernel bool, err error) {
	if point == "" {
		return nil, false, nil
	}
	all, fromKernel, err := activeMounts(c)
	if err != nil {
		return nil, fromKernel, err
	}
	m, _ := all.Get(point)
	entry, _ = m.(*value.Map)
	return entry, fromKernel, nil
}

// readProcMounts reads the kernel's own table, which is the answer
// rather than a rendering of it: the fields are fixed, the escaping is
// the fstab escaping, and nothing in it is localised.
func readProcMounts() ([]fstabEntry, error) {
	b, err := os.ReadFile(ProcMountsPath)
	if err != nil {
		return nil, err
	}
	var out []fstabEntry
	for _, line := range strings.Split(string(b), "\n") {
		if e, ok := parseFstabLine(line); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// mountCommandOutput parses `mount`, for the platforms with no
// /proc/self/mounts. The two shapes it prints are
//
//	/dev/sda1 on / type ext4 (rw,relatime)          — Linux
//	/dev/ada0p2 on / (ufs, local, soft-updates)     — the BSDs and macOS
//
// and the difference is only where the type is, so both are read here.
func mountCommandOutput(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"mount"}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(16)
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "on" {
			continue
		}
		e := fstabEntry{Device: fields[0], Point: fields[2], Dump: "0", Pass: "0"}
		if len(fields) > 4 && fields[3] == "type" {
			e.Type = fields[4]
		}
		if i := strings.Index(line, "("); i >= 0 {
			inner := strings.Trim(line[i:], "()")
			parts := splitOpts(inner)
			// On the BSDs the first item in the parentheses is the type,
			// and it is a type and not an option precisely because the
			// `type` keyword was not there to say so.
			if e.Type == "" && len(parts) > 0 {
				e.Type, parts = parts[0], parts[1:]
			}
			e.Opts = strings.Join(parts, ",")
		}
		out.Set(e.Point, e.asMap())
	}
	return out, nil
}

// activeSwaps reads the swap devices, from the kernel's file where there
// is one and from swapctl or swapon otherwise.
func activeSwaps(c *exec.Context) (*value.Map, error) {
	out := value.NewMap(4)
	if b, err := os.ReadFile("/proc/swaps"); err == nil {
		for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) < 4 {
				continue
			}
			out.Set(unescapeFstab(f[0]), value.MapOf(
				"type", f[1], "size", parseInt64(f[2]), "used", parseInt64(f[3])))
		}
		return out, nil
	}
	res, err := c.Run(exec.Command{Argv: []string{"swapctl", "-l"}, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		res, err = c.Run(exec.Command{Argv: []string{"swapon", "-s"}, IgnoreExitCode: true})
		if err != nil {
			return out, nil
		}
	}
	for i, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 3 {
			continue
		}
		out.Set(f[0], value.MapOf("size", parseInt64(f[1]), "used", parseInt64(f[2])))
	}
	return out, nil
}

// ---- mounting ----

type mountRequest struct {
	point, device, fstype, opts string
	mkmnt                       bool
}

func mountArgs(args *value.Map) mountRequest {
	return mountRequest{
		point:  states.Str(args, "name", ""),
		device: states.Str(args, "device", ""),
		fstype: states.Str(args, "fstype", ""),
		opts:   states.Str(args, "opts", "defaults"),
		mkmnt:  states.Bool(args, "mkmnt", false),
	}
}

func mountFilesystem(c *exec.Context, req mountRequest, remount bool) error {
	if req.point == "" || req.device == "" {
		return fmt.Errorf("mounting needs a mount point and a device")
	}
	if req.mkmnt && !remount {
		if err := os.MkdirAll(req.point, 0o755); err != nil {
			return err
		}
	}
	opts := req.opts
	if opts == "" {
		opts = "defaults"
	}
	if remount {
		opts = "remount," + opts
	}
	argv := []string{"mount"}
	if req.fstype != "" {
		argv = append(argv, "-t", req.fstype)
	}
	argv = append(argv, "-o", opts, req.device, req.point)

	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

func umountFilesystem(c *exec.Context, point string, force bool) error {
	if point == "" {
		return fmt.Errorf("unmounting needs a mount point")
	}
	argv := []string{"umount"}
	if force {
		argv = append(argv, "-f")
	}
	argv = append(argv, point)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// ---- options ----

func splitOpts(s string) []string {
	var out []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

func sameOptionSet(a, b string) bool {
	x, y := splitOpts(a), splitOpts(b)
	if len(x) != len(y) {
		return false
	}
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// kernelFlags are the mount flags the running table prints when they are
// in effect and leaves out when they are not, each with the spelling
// that means the opposite. `ro` is the exception that proves the shape:
// the table always carries `ro` or `rw`, so the same rule reads it.
//
// Comparing a declaration against the running mount is only safe for the
// flags in here, and that is the point of the table. The kernel does not
// report the options it was given; it reports the flags in effect, having
// filled in its own defaults and dropped everything it handled at mount
// time. `_netdev`, `nofail`, `defaults` and every `x-systemd.*` are never
// in the running table however they were declared, and a state that
// treated their absence as a difference would remount the filesystem on
// every run for ever.
var kernelFlags = map[string]string{
	"ro":         "rw",
	"nosuid":     "suid",
	"nodev":      "dev",
	"noexec":     "exec",
	"sync":       "async",
	"nodiratime": "diratime",
	"mand":       "nomand",
	"lazytime":   "nolazytime",
}

// kernelFlagAliases are the spellings the BSDs and macOS print for the
// same flag, so that a declaration written the Linux way is read
// correctly against their `mount` output.
var kernelFlagAliases = map[string]string{
	"read-only": "ro",
	"rdonly":    "ro",
	"noatime":   "noatime",
}

// atimeFamily is the one group where several spellings compete for the
// same flag and exactly one of them is reported.
var atimeFamily = map[string]bool{
	"atime": true, "noatime": true, "relatime": true, "strictatime": true,
}

// contradictedOptions returns the declared options the running mount
// disagrees with.
//
// fromKernel says whether the running table came from the kernel or from
// parsing `mount`. It decides how much can be concluded from an option
// being absent. The kernel's table has a closed vocabulary: a flag it
// does not print is a flag that is not set, so a declared `noexec` that
// is missing is a real difference. `mount` output does not — its
// vocabulary differs between platforms and versions, and an unfamiliar
// spelling would look like a missing flag — so there the absence of a
// flag concludes nothing, and only the running table saying the opposite
// counts. Under-reacting on a platform this build has not run on beats
// remounting its root filesystem on every run.
func contradictedOptions(declared, active []string, fromKernel bool) []string {
	has := make(map[string]bool, len(active))
	for _, o := range active {
		// An option with a value — uid=0, size=1G — is compared by its
		// whole spelling, which is how it is declared.
		has[o] = true
		if alias, ok := kernelFlagAliases[o]; ok {
			has[alias] = true
		}
	}

	// The effective atime flag, if the table names one.
	effectiveAtime := ""
	for a := range atimeFamily {
		if a != "atime" && has[a] {
			effectiveAtime = a
		}
	}

	var out []string
	for _, o := range declared {
		switch {
		case has[o]:
			// In effect, whatever else is true.
		case o == "ro":
			// Every platform's table says when a filesystem is mounted
			// read-only and none of them says when it is not, so this
			// one flag is conclusive by its absence everywhere.
			out = append(out, o)
		case o == "rw":
			// And its opposite is satisfied by that same absence — but
			// contradicted outright where the table does say read-only.
			if has["ro"] {
				out = append(out, o)
			}
		case atimeFamily[o]:
			// `atime` is the absence of the other three rather than a
			// word the table prints.
			if o == "atime" && effectiveAtime != "" {
				out = append(out, o)
			} else if o != "atime" && effectiveAtime != "" && effectiveAtime != o {
				out = append(out, o)
			} else if o != "atime" && effectiveAtime == "" && fromKernel {
				out = append(out, o)
			}
		case kernelFlags[o] != "":
			// A flag the table prints when it is set, and this table
			// does not print it.
			if fromKernel {
				out = append(out, o)
			}
		case oppositeOfKernelFlag(o) != "":
			// The other half: only ever reported when the table
			// positively carries the flag it contradicts.
			if has[oppositeOfKernelFlag(o)] {
				out = append(out, o)
			}
		}
	}
	return out
}

// oppositeOfKernelFlag returns the reported flag a declared option is the
// opposite of, or the empty string when it is not one.
func oppositeOfKernelFlag(o string) string {
	for flag, opposite := range kernelFlags {
		if opposite == o {
			return flag
		}
	}
	return ""
}

// ---- the states ----

func mountMounted(c *exec.Context, args *value.Map) (states.Result, error) {
	want, err := entryFromArgs(args)
	if err != nil {
		return states.False("This state needs a mount point and a device."), nil
	}
	path := fstabPathOf(args)
	persist := states.Bool(args, "persist", true)
	shouldMount := states.Bool(args, "mount", true)

	active, fromKernel, err := activeMountAt(c, want.Point)
	if err != nil {
		return states.False(fmt.Sprintf("The running mount table could not be read: %v", err)), nil
	}

	changes := value.NewMap(2)
	var actions []string
	remount := false

	if shouldMount {
		switch {
		case active == nil:
			changes.Set("mount", states.Change(nil, want.Device))
			actions = append(actions, "mounted")
		default:
			haveDev := states.Str(active, "device", "")
			haveType := states.Str(active, "fstype", "")
			switch {
			case haveDev != want.Device:
				changes.Set("device", states.Change(haveDev, want.Device))
				actions = append(actions, "remounted from a different device")
				remount = true
			case want.Type != "" && haveType != "" && haveType != want.Type:
				changes.Set("fstype", states.Change(haveType, want.Type))
				actions = append(actions, "remounted as a different type")
				remount = true
			default:
				if bad := contradictedOptions(splitOpts(want.Opts), states.Strings(active, "opts"), fromKernel); len(bad) > 0 {
					changes.Set("opts", states.Change(
						strings.Join(states.Strings(active, "opts"), ","), want.Opts))
					actions = append(actions, "remounted for "+strings.Join(bad, ", "))
					remount = true
				}
			}
		}
	}

	if persist {
		result, err := setFstab(&testingContext, path, want)
		if err != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", path, err)), nil
		}
		if result != "present" {
			changes.Set("fstab", states.Change(result == "change", true))
			actions = append(actions, "written to "+path)
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("%s is mounted from %s%s.",
			want.Point, want.Device, persistedSuffix(persist, path))), nil
	}

	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be %s.",
			want.Point, strings.Join(actions, ", and ")), changes), nil
	}

	if shouldMount && (active == nil || remount) {
		req := mountRequest{
			point: want.Point, device: want.Device, fstype: want.Type,
			opts: want.Opts, mkmnt: states.Bool(args, "mkmnt", false),
		}
		if err := mountFilesystem(c, req, remount); err != nil {
			return states.False(fmt.Sprintf("%s could not be mounted: %v", want.Point, err)), nil
		}
	}
	if persist {
		if _, err := setFstab(c, path, want); err != nil {
			return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("%s was %s.",
		want.Point, strings.Join(actions, ", and ")), changes), nil
}

// testingContext is a context in test mode, used to ask setFstab what it
// would do without letting it do it. The state's own context cannot be
// used for that: on a real run it would write the table before the
// filesystem was mounted.
var testingContext = exec.Context{Test: true}

func persistedSuffix(persist bool, path string) string {
	if persist {
		return ", and " + path + " agrees"
	}
	return ""
}

func mountUnmounted(c *exec.Context, args *value.Map) (states.Result, error) {
	point := states.Str(args, "name", "")
	if point == "" {
		return states.False("This state needs a mount point."), nil
	}
	device := states.Str(args, "device", "")
	path := fstabPathOf(args)
	persist := states.Bool(args, "persist", false)

	active, _, err := activeMountAt(c, point)
	if err != nil {
		return states.False(fmt.Sprintf("The running mount table could not be read: %v", err)), nil
	}

	changes := value.NewMap(2)
	var actions []string
	if active != nil && (device == "" || states.Str(active, "device", "") == device) {
		changes.Set("umount", states.Change(states.Str(active, "device", ""), nil))
		actions = append(actions, "unmounted")
	}
	inTable := false
	if persist {
		if removed, err := rmFstab(&testingContext, path, point, device); err != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", path, err)), nil
		} else if removed {
			inTable = true
			changes.Set("fstab", states.Change(true, false))
			actions = append(actions, "taken out of "+path)
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("Nothing is mounted at %s.", point)), nil
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be %s.",
			point, strings.Join(actions, ", and ")), changes), nil
	}

	if active != nil && (device == "" || states.Str(active, "device", "") == device) {
		if err := umountFilesystem(c, point, states.Bool(args, "force", false)); err != nil {
			return states.False(fmt.Sprintf("%s could not be unmounted: %v", point, err)), nil
		}
	}
	if inTable {
		if _, err := rmFstab(c, path, point, device); err != nil {
			return states.False(fmt.Sprintf("%s could not be written: %v", path, err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("%s was %s.", point, strings.Join(actions, ", and ")), changes), nil
}
