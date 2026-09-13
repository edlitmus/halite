package builtin

import (
	"errors"
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerTmpfs installs the `tmpfs` module of SPEC 15.2.
//
// # Most of what "manage a tmpfs" means is already `mount`
//
// A tmpfs is, to mount(8), a filesystem like any other: `mount -t tmpfs
// tmpfs /some/path` (FreeBSD's own tmpfs(4) example) needs nothing a
// UFS or ext4 mount does not also need. `mount.mount`, `mount.mounted`,
// `mount.umount` and `mount.unmounted` already carry a device, an
// fstype, an opts string, `mkmnt`, and — the part worth naming
// specifically — the whole idempotent fstab-and-running-table
// reconciliation `mount.mounted` does for every other filesystem this
// build manages. A `tmpfs.mount`/`tmpfs.mounted` pair would either
// duplicate that state machine badly (a parallel implementation with
// its own bugs) or duplicate it exactly (a wrapper that only spares a
// caller from writing `device="tmpfs", fstype="tmpfs"` twice). Neither
// is worth a second module, so this one has no mount, umount, or
// mounted/unmounted of its own: `mount.mounted` with those two fields
// set is the tmpfs state, unmodified.
//
// # What is not already there: which mounts are tmpfs, and how full they are
//
// `mount.active` carries fstype but nothing about capacity;
// `disk.usage` carries capacity (from `df`) but nothing that lets a
// caller ask for "tmpfs only" without filtering the whole table by
// hand. Neither answers the question a tmpfs specifically raises that
// a disk-backed filesystem does not: an oversized or leaking tmpfs
// eats *memory*, not spare disk, so knowing it exists and how full it
// is deserves its own one-line question. `tmpfs.list` and
// `tmpfs.usage` are that: a join of the two tables this build already
// keeps, filtered to fstype == "tmpfs", built from `activeMounts` and
// `diskUsage` (mount.go, system.go) rather than a third parser for
// either.
//
// # What was tried and set aside: resizing
//
// Linux's tmpfs can be grown or shrunk live with `mount -o
// remount,size=X`, which is already exactly `mount.remount` — no new
// code needed there either. FreeBSD's tmpfs(4), read on the host this
// was built against, documents `size` only as a mount-time option; it
// says nothing about changing it on a running mount, and there was no
// root available in this session to mount a real tmpfs and find out
// whether nmount(2) accepts a changed size or silently ignores it. A
// `tmpfs.resize` built on that unread half would be exactly the
// mistake this project's ledger keeps naming: a feature whose FreeBSD
// leg was never run. Left out, named here rather than shipped
// silently thin.
func registerTmpfs(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "tmpfs", Function: "list",
				Doc:       "Return every mounted tmpfs, with its device, its options, and how much of it is used.",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return tmpfsEntries(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tmpfs", Function: "is_mounted",
				Doc: "Report whether name is mounted, and mounted as a tmpfs rather than some other filesystem.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: tmpfsIsMountedFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "tmpfs", Function: "usage",
				Doc: "Return the size, used space and available space of one tmpfs mount, as df reports it.",
				Params: []signature.Param{
					req("name", signature.Path, "The mount point."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: tmpfsUsageFn,
		},
	)
}

// tmpfsEntries joins the running mount table with df's usage figures,
// keeping only the entries whose fstype is tmpfs. It is the one place
// all three exec functions above go through, so `list` and `usage`
// can never disagree about what counts as a tmpfs.
func tmpfsEntries(c *exec.Context) (*value.Map, error) {
	table, _, err := activeMounts(c)
	if err != nil {
		return nil, err
	}
	usage, err := diskUsage(c)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(table.Len())
	for _, e := range table.Entries() {
		entry, ok := e.Val.(*value.Map)
		if !ok {
			continue
		}
		if fstype, _ := entry.GetString("fstype"); fstype != "tmpfs" {
			continue
		}
		device, _ := entry.GetString("device")
		opts, _ := entry.GetString("opts")
		m := value.NewMap(6)
		m.Set("device", device)
		m.Set("opts", opts)
		if u, ok := usage.Get(e.Key); ok {
			if um, ok := u.(*value.Map); ok {
				for _, field := range []string{"1K-blocks", "used", "available", "capacity"} {
					if v, ok := um.GetString(field); ok {
						m.Set(field, v)
					}
				}
			}
		}
		out.Set(e.Key, m)
	}
	return out, nil
}

func tmpfsIsMountedFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("tmpfs.is_mounted needs a mount point")
	}
	active, _, err := activeMountAt(c, path)
	if err != nil {
		return nil, err
	}
	if active == nil {
		return false, nil
	}
	fstype, _ := active.GetString("fstype")
	return fstype == "tmpfs", nil
}

func tmpfsUsageFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "name", "")
	if path == "" {
		return nil, errors.New("tmpfs.usage needs a mount point")
	}
	entries, err := tmpfsEntries(c)
	if err != nil {
		return nil, err
	}
	if m, ok := entries.Get(path); ok {
		return m, nil
	}
	// Distinguish "nothing there" from "something there, but not a
	// tmpfs" — the second is the mistake a caller is more likely to be
	// making, and the generic answer would hide it.
	active, _, aerr := activeMountAt(c, path)
	if aerr == nil && active != nil {
		fstype, _ := active.GetString("fstype")
		return nil, fmt.Errorf("%s is mounted, but as %q, not tmpfs", path, fstype)
	}
	return nil, fmt.Errorf("%s is not a mounted tmpfs", path)
}
