package modules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func init() {
	register("zfs.filesystem_present", zfsFilesystemPresent)
	register("zfs.filesystem_absent", zfsFilesystemAbsent)
	register("zfs.snapshot_present", zfsSnapshotPresent)
	register("zfs.snapshot_absent", zfsSnapshotAbsent)
}

// zfsAvailable reports whether this host has ZFS at all. The states carry
// Salt's names, and Salt's zfs states are equally useless without it.
func zfsAvailable() error {
	if !has("zfs") {
		return fmt.Errorf("zfs is not available on this host")
	}
	return nil
}

// zfsFilesystemPresent ensures a dataset exists with the given properties.
//
//	zroot/jails/www:
//	  zfs.filesystem_present:
//	    - parents: true
//	    - properties:
//	        compression: lz4
//	        mountpoint: /usr/local/jails/www
//	        quota: 10G
//
// Properties are compared and set on an existing dataset, so this is the
// state that owns them rather than only creating them once.
func zfsFilesystemPresent(c *Ctx, id string, args map[string]any) Result {
	if err := zfsAvailable(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if strings.Contains(name, "@") {
		return resFail("%s names a snapshot; use zfs.snapshot_present", name)
	}
	properties, err := zfsProperties(args)
	if err != nil {
		return resFail("%v", err)
	}

	if !zfsExists(name) {
		if c.Test {
			return resWould(fmt.Sprintf("dataset %s would be created", name))
		}
		argv := []string{"create"}
		if Bool(args, "parents", false) {
			argv = append(argv, "-p")
		}
		for _, pair := range properties {
			argv = append(argv, "-o", pair)
		}
		if out, err := pkgRun(append(append([]string{"zfs"}, argv...), name)...); err != nil {
			return resFail("zfs create %s: %v: %s", name, err, strings.TrimSpace(out))
		}
		return resChanged(fmt.Sprintf("dataset %s created", name),
			map[string]string{name: "created"})
	}

	drift, err := zfsPropertyDrift(name, properties)
	if err != nil {
		return resFail("%v", err)
	}
	if len(drift) == 0 {
		return resOK(fmt.Sprintf("dataset %s exists", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("dataset %s: %s", name, strings.Join(driftComments(drift), ", ")))
	}
	changes := map[string]string{}
	for _, d := range drift {
		if out, err := pkgRun("zfs", "set", d.property+"="+d.want, name); err != nil {
			return resFail("zfs set %s on %s: %v: %s", d.property, name, err, strings.TrimSpace(out))
		}
		changes[d.property] = d.have + " -> " + d.want
	}
	return resChanged(fmt.Sprintf("dataset %s: %s", name, strings.Join(driftComments(drift), ", ")), changes)
}

// zfsFilesystemAbsent destroys a dataset.
//
// A dataset with snapshots or children is refused unless `recursive: true`
// says to take them too: `zfs destroy -r` is the most expensive typo in
// this module, and it should be spelled out.
func zfsFilesystemAbsent(c *Ctx, id string, args map[string]any) Result {
	if err := zfsAvailable(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if !zfsExists(name) {
		return resOK(fmt.Sprintf("dataset %s is absent", name))
	}
	recursive := Bool(args, "recursive", false)
	dependents := zfsDependents(name)
	if len(dependents) > 0 && !recursive {
		return resFail("dataset %s has %d snapshot(s) or child dataset(s): "+
			"set recursive: true to destroy them with it", name, len(dependents))
	}
	if c.Test {
		if len(dependents) > 0 {
			return resWould(fmt.Sprintf("dataset %s and %d dependent(s) would be destroyed",
				name, len(dependents)))
		}
		return resWould(fmt.Sprintf("dataset %s would be destroyed", name))
	}
	argv := []string{"zfs", "destroy"}
	if recursive {
		argv = append(argv, "-r")
	}
	if out, err := pkgRun(append(argv, name)...); err != nil {
		return resFail("zfs destroy %s: %v: %s", name, err, strings.TrimSpace(out))
	}
	return resChanged(fmt.Sprintf("dataset %s destroyed", name),
		map[string]string{name: "destroyed"})
}

// zfsSnapshotPresent ensures a snapshot exists.
//
//	zroot/jails/www@before-upgrade:
//	  zfs.snapshot_present:
//	    - recursive: true
//
// A snapshot is immutable, so this state only ever creates: a name that is
// already taken is already right.
func zfsSnapshotPresent(c *Ctx, id string, args map[string]any) Result {
	if err := zfsAvailable(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if !strings.Contains(name, "@") {
		return resFail("%s is not a snapshot name (dataset@snapshot)", name)
	}
	if zfsExists(name) {
		return resOK(fmt.Sprintf("snapshot %s exists", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("snapshot %s would be taken", name))
	}
	argv := []string{"zfs", "snapshot"}
	if Bool(args, "recursive", false) {
		argv = append(argv, "-r")
	}
	if out, err := pkgRun(append(argv, name)...); err != nil {
		return resFail("zfs snapshot %s: %v: %s", name, err, strings.TrimSpace(out))
	}
	return resChanged(fmt.Sprintf("snapshot %s taken", name), map[string]string{name: "created"})
}

// zfsSnapshotAbsent destroys a snapshot.
func zfsSnapshotAbsent(c *Ctx, id string, args map[string]any) Result {
	if err := zfsAvailable(); err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if !strings.Contains(name, "@") {
		return resFail("%s is not a snapshot name (dataset@snapshot)", name)
	}
	if !zfsExists(name) {
		return resOK(fmt.Sprintf("snapshot %s is absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("snapshot %s would be destroyed", name))
	}
	argv := []string{"zfs", "destroy"}
	if Bool(args, "recursive", false) {
		argv = append(argv, "-r")
	}
	if out, err := pkgRun(append(argv, name)...); err != nil {
		return resFail("zfs destroy %s: %v: %s", name, err, strings.TrimSpace(out))
	}
	return resChanged(fmt.Sprintf("snapshot %s destroyed", name),
		map[string]string{name: "destroyed"})
}

// zfsExists reports whether a dataset or snapshot is there.
func zfsExists(name string) bool {
	_, ok := pkgQuery("zfs", "list", "-H", "-o", "name", name)
	return ok
}

// zfsDependents lists the snapshots and child datasets that a destroy
// would have to take with it.
func zfsDependents(name string) []string {
	out, ok := pkgQuery("zfs", "list", "-H", "-r", "-t", "all", "-o", "name", name)
	if !ok {
		return nil
	}
	var dependents []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" && line != name {
			dependents = append(dependents, line)
		}
	}
	return dependents
}

// zfsProperties turns the `properties` mapping into the property=value
// arguments zfs takes, sorted so a command line is the same twice.
func zfsProperties(args map[string]any) ([]string, error) {
	mapping, declared := Map(args, "properties")
	if !declared {
		return nil, nil
	}
	if mapping == nil {
		return nil, fmt.Errorf("properties must be a mapping of zfs properties")
	}
	pairs := make([]string, 0, len(mapping))
	for name, value := range mapping {
		if strings.ContainsAny(name, "= ") {
			return nil, fmt.Errorf("%q is not a property name", name)
		}
		pairs = append(pairs, fmt.Sprintf("%s=%v", name, value))
	}
	sort.Strings(pairs)
	return pairs, nil
}

// zfsDrift is one property that is not what the state says.
type zfsDrift struct{ property, want, have string }

func driftComments(drift []zfsDrift) []string {
	out := make([]string, 0, len(drift))
	for _, d := range drift {
		out = append(out, fmt.Sprintf("%s %s -> %s", d.property, d.have, d.want))
	}
	return out
}

// zfsPropertyDrift compares what a dataset holds against what the state
// says it should.
func zfsPropertyDrift(name string, properties []string) ([]zfsDrift, error) {
	var drift []zfsDrift
	for _, pair := range properties {
		property, want, found := strings.Cut(pair, "=")
		if !found {
			return nil, fmt.Errorf("property %q is not name=value", pair)
		}
		have, ok := pkgQuery("zfs", "get", "-H", "-o", "value", property, name)
		if !ok {
			return nil, fmt.Errorf("cannot read property %s of %s", property, name)
		}
		have = strings.TrimSpace(have)
		if zfsValuesEqual(want, have) {
			continue
		}
		drift = append(drift, zfsDrift{property: property, want: want, have: have})
	}
	return drift, nil
}

// zfsValuesEqual compares a wanted property value with what zfs reports.
//
// Sizes are the reason this is not a string comparison: a state asking for
// `quota: 10G` reads back as `10G` on one release and `10.0G` on another,
// and comparing the text would set the property — and report a change — on
// every run forever.
func zfsValuesEqual(want, have string) bool {
	if strings.EqualFold(strings.TrimSpace(want), strings.TrimSpace(have)) {
		return true
	}
	wantSize, wantOK := zfsSize(want)
	haveSize, haveOK := zfsSize(have)
	return wantOK && haveOK && wantSize == haveSize
}

// zfsSize parses the sizes zfs prints and accepts: a number with an
// optional binary suffix. "none" and "0" are the same absence of a limit.
func zfsSize(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	switch strings.ToLower(value) {
	case "none", "-", "":
		return 0, true
	}
	// zfs prints 10G, and accepts 10G, 10GB and 10GiB for the same size.
	digits := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(value), "b"), "i")
	units := map[byte]int64{'k': 1 << 10, 'm': 1 << 20, 'g': 1 << 30, 't': 1 << 40, 'p': 1 << 50}
	multiplier := int64(1)
	if len(digits) > 0 {
		if unit, known := units[digits[len(digits)-1]]; known {
			multiplier = unit
			digits = digits[:len(digits)-1]
		}
	}
	number, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, false
	}
	return int64(number * float64(multiplier)), true
}
