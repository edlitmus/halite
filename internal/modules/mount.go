package modules

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func init() {
	register("mount.mounted", mountMounted)
	register("mount.unmounted", mountUnmounted)
}

const fstabPath = "/etc/fstab"

// mountEntry is one line of fstab, or one currently mounted filesystem.
type mountEntry struct {
	Device string
	Point  string
	FSType string
	Opts   string
	Dump   string
	Pass   string
}

func (e mountEntry) fstabLine() string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
		e.Device, e.Point, e.FSType, e.Opts, e.Dump, e.Pass)
}

// mountMounted mounts a filesystem and records it in fstab.
//
//	/data:
//	  mount.mounted:
//	    - device: /dev/ada1p1
//	    - fstype: ufs
//	    - opts: rw,noatime
//
// Mount options are only enforced in fstab, not against an already-mounted
// filesystem: the kernel reports options the caller never asked for, so
// comparing them would remount on every run. Unmount and mount again to
// change the options of a live filesystem.
func mountMounted(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS == "windows" {
		return resFail("mount is not supported on Windows")
	}
	point := Str(args, "name", id)
	device := Str(args, "device", "")
	fstype := Str(args, "fstype", "")
	if device == "" || fstype == "" {
		return resFail("mount.mounted requires a device and an fstype")
	}
	want := mountEntry{
		Device: device,
		Point:  point,
		FSType: fstype,
		Opts:   Str(args, "opts", "rw"),
		Dump:   Str(args, "dump", "0"),
		Pass:   Str(args, "pass", "0"),
	}
	mkmnt := Bool(args, "mkmnt", true)
	persist := Bool(args, "persist", true)

	active, err := activeMounts()
	if err != nil {
		return resFail("%v", err)
	}
	current, mounted := active[point]
	if mounted && current.Device != device && current.Device != "" {
		return resFail("%s already has %s mounted; unmount it before mounting %s",
			point, current.Device, device)
	}

	needMount := !mounted
	needFstab := false
	if persist {
		needFstab, err = fstabDiffers(want)
		if err != nil {
			return resFail("%v", err)
		}
	}
	if !needMount && !needFstab {
		return resOK(fmt.Sprintf("%s is mounted from %s", point, device))
	}

	if c.Test {
		var actions []string
		if needMount {
			actions = append(actions, fmt.Sprintf("would mount %s at %s", device, point))
		}
		if needFstab {
			actions = append(actions, fmt.Sprintf("would record %s in %s", point, fstabPath))
		}
		return resWould(strings.Join(actions, "; "))
	}

	changes := map[string]string{}
	if needMount {
		if mkmnt {
			if err := os.MkdirAll(point, 0o755); err != nil {
				return resFail("create mount point %s: %v", point, err)
			}
		}
		if _, errOut, rc, err := run("mount", "-t", fstype, "-o", want.Opts, device, point); err != nil || rc != 0 {
			return resFail("mount %s at %s: %s", device, point, cmdError(errOut, err))
		}
		changes["mounted"] = fmt.Sprintf("%s at %s", device, point)
	}
	if needFstab {
		if err := writeFstabEntry(want); err != nil {
			return resFail("%v", err)
		}
		changes["fstab"] = want.fstabLine()
	}
	return resChanged(fmt.Sprintf("%s is mounted from %s", point, device), changes)
}

// mountUnmounted unmounts a filesystem, optionally removing its fstab entry.
//
//	/data:
//	  mount.unmounted:
//	    - persist: true
func mountUnmounted(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS == "windows" {
		return resFail("mount is not supported on Windows")
	}
	point := Str(args, "name", id)
	// Unlike mount.mounted, leaving fstab alone is the safe default here: an
	// operator unmounting a filesystem usually still wants it at next boot.
	persist := Bool(args, "persist", false)

	active, err := activeMounts()
	if err != nil {
		return resFail("%v", err)
	}
	_, mounted := active[point]

	inFstab := false
	if persist {
		entries, err := readFstab()
		if err != nil {
			return resFail("%v", err)
		}
		_, inFstab = entries[point]
	}
	if !mounted && !inFstab {
		return resOK(fmt.Sprintf("%s is not mounted", point))
	}

	if c.Test {
		var actions []string
		if mounted {
			actions = append(actions, fmt.Sprintf("would unmount %s", point))
		}
		if inFstab {
			actions = append(actions, fmt.Sprintf("would remove %s from %s", point, fstabPath))
		}
		return resWould(strings.Join(actions, "; "))
	}

	changes := map[string]string{}
	if mounted {
		if _, errOut, rc, err := run("umount", point); err != nil || rc != 0 {
			return resFail("unmount %s: %s", point, cmdError(errOut, err))
		}
		changes["unmounted"] = point
	}
	if inFstab {
		if err := removeFstabEntry(point); err != nil {
			return resFail("%v", err)
		}
		changes["fstab"] = "entry removed"
	}
	return resChanged(fmt.Sprintf("%s is not mounted", point), changes)
}

// activeMounts reports what is mounted right now, keyed by mount point.
func activeMounts() (map[string]mountEntry, error) {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/self/mounts")
		if err != nil {
			return nil, fmt.Errorf("read /proc/self/mounts: %w", err)
		}
		return parseFstabFormat(string(b)), nil
	case "darwin":
		out, errOut, rc, err := run("mount")
		if err != nil || rc != 0 {
			return nil, fmt.Errorf("mount: %s", cmdError(errOut, err))
		}
		return parseDarwinMounts(out), nil
	default:
		// FreeBSD and friends: `mount -p` prints fstab-format lines.
		out, errOut, rc, err := run("mount", "-p")
		if err != nil || rc != 0 {
			return nil, fmt.Errorf("mount -p: %s", cmdError(errOut, err))
		}
		return parseFstabFormat(out), nil
	}
}

// parseFstabFormat reads whitespace-separated fstab-style lines.
func parseFstabFormat(text string) map[string]mountEntry {
	out := map[string]mountEntry{}
	for _, line := range strings.Split(text, "\n") {
		entry, ok := parseFstabLine(line)
		if ok {
			out[entry.Point] = entry
		}
	}
	return out
}

func parseFstabLine(line string) (mountEntry, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return mountEntry{}, false
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 3 {
		return mountEntry{}, false
	}
	entry := mountEntry{
		Device: fields[0], Point: fields[1], FSType: fields[2],
		Opts: "rw", Dump: "0", Pass: "0",
	}
	if len(fields) > 3 {
		entry.Opts = fields[3]
	}
	if len(fields) > 4 {
		entry.Dump = fields[4]
	}
	if len(fields) > 5 {
		entry.Pass = fields[5]
	}
	return entry, true
}

// parseDarwinMounts reads `mount` output: "/dev/disk1s1 on / (apfs, local)".
func parseDarwinMounts(text string) map[string]mountEntry {
	out := map[string]mountEntry{}
	for _, line := range strings.Split(text, "\n") {
		device, rest, found := strings.Cut(strings.TrimSpace(line), " on ")
		if !found {
			continue
		}
		point, opts, found := strings.Cut(rest, " (")
		entry := mountEntry{Device: device, Point: strings.TrimSpace(point)}
		if found {
			fields := strings.SplitN(strings.TrimSuffix(strings.TrimSpace(opts), ")"), ",", 2)
			entry.FSType = strings.TrimSpace(fields[0])
		}
		out[entry.Point] = entry
	}
	return out
}

func readFstab() (map[string]mountEntry, error) {
	b, err := os.ReadFile(fstabPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]mountEntry{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", fstabPath, err)
	}
	return parseFstabFormat(string(b)), nil
}

// fstabDiffers reports whether fstab lacks the wanted entry or records a
// different one for the same mount point.
func fstabDiffers(want mountEntry) (bool, error) {
	entries, err := readFstab()
	if err != nil {
		return false, err
	}
	current, ok := entries[want.Point]
	if !ok {
		return true, nil
	}
	return current != want, nil
}

// writeFstabEntry replaces the line for this mount point, or appends one.
func writeFstabEntry(want mountEntry) error {
	lines, err := fstabLines()
	if err != nil {
		return err
	}
	replaced := false
	for i, line := range lines {
		if entry, ok := parseFstabLine(line); ok && entry.Point == want.Point {
			lines[i] = want.fstabLine()
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, want.fstabLine())
	}
	return writeFstabLines(lines)
}

func removeFstabEntry(point string) error {
	lines, err := fstabLines()
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if entry, ok := parseFstabLine(line); ok && entry.Point == point {
			continue
		}
		kept = append(kept, line)
	}
	return writeFstabLines(kept)
}

func fstabLines() ([]string, error) {
	b, err := os.ReadFile(fstabPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", fstabPath, err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n"), nil
}

func writeFstabLines(lines []string) error {
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(fstabPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", fstabPath, err)
	}
	return nil
}

// cmdError prefers a command's stderr over a bare exec error.
func cmdError(stderr string, err error) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	if err != nil {
		return err.Error()
	}
	return "command failed"
}
