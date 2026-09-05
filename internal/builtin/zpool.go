package builtin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// zfsPlatforms are the platforms with ZFS. It is the same list the zfs
// module uses, and they are declared rather than left open because a
// `zpool` on a node without one is a state that cannot succeed.
var zfsPlatforms = []string{"freebsd", "linux", "illumos"}

// registerZpool installs the zpool module of SPEC sections 15.3 and 15.5.
//
// What a pool state can and cannot manage is the whole design here, and
// it is not symmetric. Creating a pool is declarative: a layout, some
// properties, and the tool does the rest. Changing the layout of a pool
// that already exists is not. A top-level vdev cannot be removed from
// most pools at all, `zpool add` to the wrong place turns a mirror into
// a stripe with no undo, and the failure mode of getting it wrong is the
// permanent loss of everything on the pool.
//
// So `zpool.present` creates a pool that is not there, and on one that
// is, it manages the properties and *reports* a layout that does not
// match rather than acting on it. A warning an operator reads is worth
// more than a state that silently widens a stripe across the disk that
// was meant to mirror it.
func registerZpool(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "list",
				Doc:       "Return the pools and their space and health.",
				TestMode:  signature.TestNotApplicable,
				Platforms: zfsPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return zpoolList(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "healthy",
				Doc: "Report whether every pool is ONLINE. A pool that is DEGRADED or worse " +
					"makes this false, which is what a beacon or a check state watches.",
				TestMode:  signature.TestNotApplicable,
				Platforms: zfsPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := zfsRun(c, "zpool", "list", "-H", "-o", "health")
				if err != nil {
					return nil, err
				}
				for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
					if h := strings.TrimSpace(line); h != "ONLINE" && h != "" {
						return false, nil
					}
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "exists",
				Doc:       "Report whether a pool is imported on this node.",
				Params:    []signature.Param{req("zpool", signature.String, "The pool.")},
				TestMode:  signature.TestNotApplicable,
				Platforms: zfsPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return zpoolExists(c, states.Str(args, "zpool", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "get",
				Doc: "Return a pool's properties.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
					opt("properties", signature.List, nil, "Which properties; defaults to all."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: zfsPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				which := "all"
				if p := states.Strings(args, "properties"); len(p) > 0 {
					which = strings.Join(p, ",")
				}
				return zpoolProperties(c, states.Str(args, "zpool", ""), which)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "vdevs",
				Doc: "Return a pool's top-level vdevs and the devices under each.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: zfsPlatforms,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				vdevs, err := zpoolVdevs(c, states.Str(args, "zpool", ""))
				if err != nil {
					return nil, err
				}
				out := make([]any, 0, len(vdevs))
				for _, v := range vdevs {
					out = append(out, v.asMap())
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "create",
				Doc: "Create a pool.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
					req("layout", signature.Any, "The vdevs: a list of devices, or a mapping of vdev type to devices, or a list of those."),
					opt("properties", signature.Map, nil, "Pool properties, set with -o."),
					opt("filesystem_properties", signature.Map, nil, "Properties of the pool's root dataset, set with -O."),
					opt("mountpoint", signature.Path, "", "Where the root dataset mounts."),
					opt("force", signature.Bool, false, "Use a device that already carries a filesystem or a pool."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name := states.Str(args, "zpool", "")
				vdevs, err := parseLayout(argValue(args, "layout"))
				if err != nil {
					return nil, err
				}
				if c.Test {
					return true, nil
				}
				return true, zpoolCreate(c, name, vdevs, args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "destroy",
				Doc: "Destroy a pool and everything on it.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
					opt("force", signature.Bool, false, "Destroy it even while a dataset is in use."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, zpoolDestroy(c, states.Str(args, "zpool", ""), states.Bool(args, "force", false))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "export",
				Doc: "Export a pool, leaving its data intact for another node to import.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
					opt("force", signature.Bool, false, "Export it even while a dataset is in use."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				return true, zpoolExport(c, states.Str(args, "zpool", ""), states.Bool(args, "force", false))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "import",
				Doc: "Import a pool that is on this node's devices but not attached.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
					opt("device_dir", signature.Path, "", "Where to look for devices, for a pool on files rather than on disks."),
					opt("new_name", signature.String, "", "Import it under a different name."),
					opt("force", signature.Bool, false, "Import it even though it was not cleanly exported."),
					opt("recovery", signature.Bool, false, "Discard the last transactions to import a damaged pool. This loses data, and is refused unless force is set too."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if states.Bool(args, "recovery", false) && !states.Bool(args, "force", false) {
					return nil, fmt.Errorf(
						"recovery discards the pool's last transactions, which loses whatever " +
							"was in them; set force as well to say that is intended")
				}
				if c.Test {
					return true, nil
				}
				return true, zpoolImport(c, args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "scrub",
				Doc: "Start, pause or stop a scrub.",
				Params: []signature.Param{
					req("zpool", signature.String, "The pool."),
					opt("stop", signature.Bool, false, "Stop a scrub that is running."),
					opt("pause", signature.Bool, false, "Pause a scrub that is running."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				argv := []string{"zpool", "scrub"}
				switch {
				case states.Bool(args, "stop", false):
					argv = append(argv, "-s")
				case states.Bool(args, "pause", false):
					argv = append(argv, "-p")
				}
				argv = append(argv, states.Str(args, "zpool", ""))
				if c.Test {
					return true, nil
				}
				if _, err := zfsRun(c, argv...); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "present",
				Doc: "Ensure a pool exists, with the given properties.",
				Params: []signature.Param{
					nameParam("The pool. Defaults to the state ID."),
					opt("layout", signature.Any, nil, "The vdevs to create it from. Not required when the pool already exists, and never used to change one that does."),
					opt("properties", signature.Map, nil, "Pool properties."),
					opt("filesystem_properties", signature.Map, nil, "Properties of the pool's root dataset, set only at creation."),
					opt("mountpoint", signature.Path, "", "Where the root dataset mounts."),
					opt("import", signature.Bool, true, "Look for an exported pool of this name before creating one."),
					opt("device_dir", signature.Path, "", "Where to look for devices when importing, for a pool on files."),
					opt("force", signature.Bool, false, "Use a device that already carries a filesystem or a pool."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.5",
			},
			Fn: zpoolPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "zpool", Function: "absent",
				Doc: "Ensure a pool is not attached to this node, by exporting it or by destroying it.",
				Params: []signature.Param{
					nameParam("The pool. Defaults to the state ID."),
					opt("export", signature.Bool, true, "Export the pool, keeping its data. False destroys it and everything on it."),
					opt("force", signature.Bool, false, "Act even while a dataset is in use."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  zfsPlatforms,
				Section:    "15.5",
			},
			Fn: zpoolAbsent,
		},
	)
}

// ---- reading ----

func zpoolList(c *exec.Context) (*value.Map, error) {
	res, err := zfsRun(c, "zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,health")
	if err != nil {
		return nil, err
	}
	out := value.NewMap(4)
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		out.Set(f[0], value.MapOf(
			"size", parseInt64(f[1]),
			"allocated", parseInt64(f[2]),
			"free", parseInt64(f[3]),
			"health", f[4],
		))
	}
	return out, nil
}

func zpoolExists(c *exec.Context, name string) bool {
	if name == "" || c.Which("zpool") == "" {
		return false
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"zpool", "list", "-H", "-o", "name", name},
		IgnoreExitCode: true,
	})
	return err == nil && res.Code == 0
}

func zpoolProperties(c *exec.Context, name, which string) (*value.Map, error) {
	res, err := zfsRun(c, "zpool", "get", "-H", "-p", "-o", "property,value", which, name)
	if err != nil {
		return nil, err
	}
	return parseTabPairs(res.Stdout), nil
}

// zpoolVdev is one top-level vdev of a pool.
type zpoolVdev struct {
	// Type is mirror, raidz2, log, cache, spare and so on. Empty is a
	// bare device, which is a stripe column.
	Type string
	// Devices are the leaves under it.
	Devices []string
}

func (v zpoolVdev) asMap() *value.Map {
	devices := make([]any, 0, len(v.Devices))
	for _, d := range v.Devices {
		devices = append(devices, d)
	}
	return value.MapOf("type", v.Type, "devices", devices)
}

// zpoolVdevs reads a pool's layout.
//
// `zpool list -v` draws a tree, and its scripted mode does not: every row
// under the pool is indented by exactly one tab whether it is a top-level
// vdev or a device inside one. That was checked against ZFS 2.2.2 rather
// than assumed, because the first version of this read the indentation
// for depth and found every pool empty. So the grouping is by name: a row
// naming a vdev type opens a group and the rows after it are its members,
// and a row that is a device before any group is its own top-level vdev,
// which is what a stripe column is.
//
// The other thing checked rather than assumed is that a pool's log,
// cache and spare devices are introduced by a section header — `logs`,
// `cache`, `spare` — which is printed *unindented and space-padded*,
// ignoring -H altogether. A reader that took every unindented row for
// the pool's own row swallowed the header and filed the log device as
// another leaf of the mirror above it. This one did.
//
// One shape still cannot be recovered, and it is worth naming: a bare
// device added as a stripe column beside a mirror looks exactly like
// another leaf of the mirror, because the depth that would tell them
// apart is what scripted mode drops. It reads as a wider mirror. The
// layout is only ever used for a warning — never to act — and this is
// one of the reasons why.
func zpoolVdevs(c *exec.Context, name string) ([]zpoolVdev, error) {
	if name == "" {
		return nil, fmt.Errorf("reading a layout needs a pool name")
	}
	res, err := zfsRun(c, "zpool", "list", "-v", "-H", "-p", name)
	if err != nil {
		return nil, err
	}
	var out []zpoolVdev
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != '\t' && line[0] != ' ' {
			// Unindented: either the pool's own row, or one of the
			// section headers, which are the only other thing out here.
			if t := vdevSections[strings.Fields(line)[0]]; t != "" {
				out = append(out, zpoolVdev{Type: t})
			}
			continue
		}
		entry := strings.TrimSpace(strings.SplitN(strings.TrimLeft(line, " \t"), "\t", 2)[0])
		if entry == "" {
			continue
		}
		if t := vdevTypeOf(entry); t != "" {
			out = append(out, zpoolVdev{Type: t})
			continue
		}
		if n := len(out); n > 0 && out[n-1].Type != "" {
			out[n-1].Devices = append(out[n-1].Devices, entry)
			continue
		}
		out = append(out, zpoolVdev{Devices: []string{entry}})
	}
	// A section header followed straight away by a vdev row — a mirrored
	// log, say — opens a group that never gets a device of its own.
	// Reporting it as a vdev of nothing would be a difference that is
	// not there.
	kept := out[:0]
	for _, v := range out {
		if len(v.Devices) > 0 {
			kept = append(kept, v)
		}
	}
	return kept, nil
}

// vdevSections maps the section headers `zpool list -v` prints onto the
// vdev type they introduce. They are plural in the listing and singular
// on the command line, which is why this is a translation and not a set.
var vdevSections = map[string]string{
	"logs": "log", "log": "log",
	"cache":   "cache",
	"spares":  "spare",
	"spare":   "spare",
	"special": "special",
	"dedup":   "dedup",
}

// vdevTypes are the words zpool uses for a vdev that groups devices.
// `zpool list -v` names them with a trailing index — mirror-0, raidz2-1 —
// and the index is positional rather than part of the type.
var vdevTypes = map[string]bool{
	"mirror": true, "raidz": true, "raidz1": true, "raidz2": true,
	"raidz3": true, "draid": true, "draid1": true, "draid2": true,
	"draid3": true, "log": true, "cache": true, "spare": true,
	"special": true, "dedup": true,
}

// vdevTypeOf returns the vdev type a listing row names, or the empty
// string when the row is a device rather than a group.
func vdevTypeOf(entry string) string {
	base := entry
	if i := strings.LastIndex(base, "-"); i > 0 {
		if _, err := fmt.Sscanf(base[i+1:], "%d", new(int)); err == nil {
			base = base[:i]
		}
	}
	if vdevTypes[base] {
		return base
	}
	return ""
}

// ---- the declared layout ----

// parseLayout reads the vdev layout a state declares.
//
// Three spellings are taken, because a tree being migrated will have one
// of them and the difference is not worth an edit:
//
//	layout: [/dev/sda, /dev/sdb]              a stripe of two
//	layout: {mirror: [/dev/sda, /dev/sdb]}    one mirror
//	layout:                                   two mirrors and a log
//	  - mirror: [/dev/sda, /dev/sdb]
//	  - mirror: [/dev/sdc, /dev/sdd]
//	  - log: [/dev/nvme0n1]
//
// Salt writes the second and third with the positional index left on —
// `mirror-0:` — and with the devices as a mapping to nothing rather than
// a list, so both of those are read too.
func parseLayout(raw any) ([]zpoolVdev, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return []zpoolVdev{{Devices: []string{v}}}, nil
	case *value.Map:
		return vdevsFromMap(v)
	case []any:
		var out []zpoolVdev
		for _, item := range v {
			switch entry := item.(type) {
			case *value.Map:
				group, err := vdevsFromMap(entry)
				if err != nil {
					return nil, err
				}
				out = append(out, group...)
			case string:
				out = append(out, zpoolVdev{Devices: []string{entry}})
			default:
				return nil, fmt.Errorf("a layout entry is %T, and has to be a device or a vdev", item)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("a layout is %T, and has to be a device, a list of them, or a mapping of vdev to devices", raw)
}

func vdevsFromMap(m *value.Map) ([]zpoolVdev, error) {
	out := make([]zpoolVdev, 0, m.Len())
	for _, e := range m.Entries() {
		key := value.KeyString(e.Key)
		kind := vdevTypeOf(key)
		if kind == "" {
			return nil, fmt.Errorf("%q is not a vdev type; the ones ZFS has are %s",
				key, strings.Join(sortedVdevTypes(), ", "))
		}
		devices, err := deviceList(e.Val)
		if err != nil {
			return nil, fmt.Errorf("the devices of %s: %w", key, err)
		}
		if len(devices) == 0 {
			return nil, fmt.Errorf("%s has no devices under it", key)
		}
		out = append(out, zpoolVdev{Type: kind, Devices: devices})
	}
	return out, nil
}

// deviceList reads the devices under a vdev, as a list or as Salt's
// mapping of device name to nothing.
func deviceList(raw any) ([]string, error) {
	switch v := raw.(type) {
	case string:
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("a device is %T, and has to be a path", item)
			}
			out = append(out, s)
		}
		return out, nil
	case *value.Map:
		out := make([]string, 0, v.Len())
		for _, e := range v.Entries() {
			out = append(out, value.KeyString(e.Key))
		}
		return out, nil
	}
	return nil, fmt.Errorf("the devices are %T, and have to be a path or a list of them", raw)
}

func sortedVdevTypes() []string {
	out := make([]string, 0, len(vdevTypes))
	for t := range vdevTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// vdevArgv renders the layout as zpool create takes it.
func vdevArgv(vdevs []zpoolVdev) []string {
	var argv []string
	for _, v := range vdevs {
		if v.Type != "" {
			argv = append(argv, v.Type)
		}
		argv = append(argv, v.Devices...)
	}
	return argv
}

// sameLayout compares a declared layout against one read back from the
// pool. Only the shape is compared — the vdev types, in order, and how
// many devices are under each — because the device *names* a pool
// reports are not the ones it was created from: a pool built on
// /dev/sdb is listed under whatever /dev/disk/by-id name that disk has,
// and a pool built on a file is listed by its absolute path.
func sameLayout(declared, actual []zpoolVdev) bool {
	if len(declared) != len(actual) {
		return false
	}
	for i := range declared {
		if declared[i].Type != actual[i].Type {
			return false
		}
		if len(declared[i].Devices) != len(actual[i].Devices) {
			return false
		}
	}
	return true
}

func describeLayout(vdevs []zpoolVdev) string {
	if len(vdevs) == 0 {
		return "nothing"
	}
	parts := make([]string, 0, len(vdevs))
	for _, v := range vdevs {
		kind := v.Type
		if kind == "" {
			kind = "device"
		}
		parts = append(parts, fmt.Sprintf("%s of %d", kind, len(v.Devices)))
	}
	return strings.Join(parts, ", ")
}

// ---- writing ----

func zpoolCreate(c *exec.Context, name string, vdevs []zpoolVdev, args *value.Map) error {
	if name == "" {
		return fmt.Errorf("creating a pool needs a name")
	}
	if len(vdevs) == 0 {
		return fmt.Errorf("creating %s needs a layout to create it from", name)
	}
	argv := []string{"zpool", "create"}
	if states.Bool(args, "force", false) {
		argv = append(argv, "-f")
	}
	if m := states.Str(args, "mountpoint", ""); m != "" {
		argv = append(argv, "-m", m)
	}
	for _, p := range sortedPropertyNames(states.Mapping(args, "properties")) {
		v, _ := states.Mapping(args, "properties").Get(p)
		argv = append(argv, "-o", p+"="+value.KeyString(v))
	}
	for _, p := range sortedPropertyNames(states.Mapping(args, "filesystem_properties")) {
		v, _ := states.Mapping(args, "filesystem_properties").Get(p)
		argv = append(argv, "-O", p+"="+value.KeyString(v))
	}
	argv = append(argv, name)
	argv = append(argv, vdevArgv(vdevs)...)
	return zpoolAct(c, argv)
}

func zpoolDestroy(c *exec.Context, name string, force bool) error {
	argv := []string{"zpool", "destroy"}
	if force {
		argv = append(argv, "-f")
	}
	return zpoolAct(c, append(argv, name))
}

func zpoolExport(c *exec.Context, name string, force bool) error {
	argv := []string{"zpool", "export"}
	if force {
		argv = append(argv, "-f")
	}
	return zpoolAct(c, append(argv, name))
}

func zpoolImport(c *exec.Context, args *value.Map) error {
	argv := []string{"zpool", "import"}
	if d := states.Str(args, "device_dir", ""); d != "" {
		argv = append(argv, "-d", d)
	}
	if states.Bool(args, "force", false) {
		argv = append(argv, "-f")
	}
	if states.Bool(args, "recovery", false) {
		argv = append(argv, "-F")
	}
	argv = append(argv, states.Str(args, "zpool", ""))
	if n := states.Str(args, "new_name", ""); n != "" {
		argv = append(argv, n)
	}
	return zpoolAct(c, argv)
}

// zpoolAct runs a command that changes a pool and reports what the tool
// said when it refuses. zpool's refusals name the device and the reason
// — "is part of active pool", "contains a filesystem" — and an operator
// needs that rather than an exit code.
func zpoolAct(c *exec.Context, argv []string) error {
	if c.Which("zpool") == "" {
		return fmt.Errorf("zpool was not found on this node")
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// tryImport attempts to attach an exported pool of this name, and says
// whether it found one. A pool that is not there is not an error: it is
// the ordinary case where the pool has to be created instead.
func tryImport(c *exec.Context, name, deviceDir string) bool {
	argv := []string{"zpool", "import"}
	if deviceDir != "" {
		argv = append(argv, "-d", deviceDir)
	}
	argv = append(argv, name)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	return err == nil && res.Code == 0
}

// ---- the states ----

func zpoolPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False("This state needs a pool name."), nil
	}
	if c.Which("zpool") == "" {
		return states.False("zpool was not found on this node."), nil
	}
	declared, err := parseLayout(argValue(args, "layout"))
	if err != nil {
		return states.False(fmt.Sprintf("The layout of %s could not be read: %v", name, err)), nil
	}

	if !zpoolExists(c, name) {
		return zpoolCreateState(c, args, name, declared)
	}
	return zpoolUpdateState(c, args, name, declared)
}

// zpoolCreateState is the pool that is not there yet.
func zpoolCreateState(c *exec.Context, args *value.Map, name string, declared []zpoolVdev) (states.Result, error) {
	// An exported pool of this name is imported rather than created
	// over: creating a pool on the devices of one that already holds
	// data destroys the data, and doing that because a node rebooted
	// into a state run is not a thing a state should be able to do.
	importFirst := states.Bool(args, "import", true)
	deviceDir := states.Str(args, "device_dir", "")

	if len(declared) == 0 && !importFirst {
		return states.False(fmt.Sprintf(
			"The pool %s does not exist and no layout was declared to create it from.", name)), nil
	}

	changes := value.MapOf(name, states.Change(nil, "present"))
	if c.Test {
		what := fmt.Sprintf("created from %s", describeLayout(declared))
		if importFirst {
			what = "imported if it is on this node's devices, and " + what + " if it is not"
		}
		return states.WouldChange(fmt.Sprintf("The pool %s would be %s.", name, what), changes), nil
	}

	if importFirst && tryImport(c, name, deviceDir) {
		return states.Changed(fmt.Sprintf("The pool %s was imported.", name), changes), nil
	}
	if len(declared) == 0 {
		return states.False(fmt.Sprintf(
			"The pool %s was not found to import and no layout was declared to create it from.", name)), nil
	}
	if err := zpoolCreate(c, name, declared, args); err != nil {
		return states.False(fmt.Sprintf("The pool %s could not be created: %v", name, err)), nil
	}
	// The declared properties went to `zpool create -o`, which is the
	// only way some of them can be set at all: ashift is fixed when the
	// vdevs are written and `zpool set ashift=` on a live pool is
	// refused.
	return states.Changed(fmt.Sprintf("The pool %s was created from %s.",
		name, describeLayout(declared)), changes), nil
}

// zpoolUpdateState is the pool that already exists, where the only thing
// this build will change is a property.
func zpoolUpdateState(c *exec.Context, args *value.Map, name string, declared []zpoolVdev) (states.Result, error) {
	props := states.Mapping(args, "properties")
	changes := value.NewMap(4)
	var warnings []string

	if props != nil && props.Len() > 0 {
		have, err := zpoolProperties(c, name, propertyList(props))
		if err != nil {
			return states.False(fmt.Sprintf("The properties of %s could not be read: %v", name, err)), nil
		}
		for _, e := range props.Entries() {
			prop := value.KeyString(e.Key)
			want := value.KeyString(e.Val)
			cur, ok := have.Get(prop)
			if !ok || value.KeyString(cur) != want {
				changes.Set(prop, states.Change(cur, want))
			}
		}
	}

	// A layout that does not match is reported and not acted on. A
	// top-level vdev cannot be removed from most pools, and `zpool add`
	// aimed at what was meant to be a mirror turns it into a stripe with
	// no undo — so the difference between "this pool is not what you
	// declared" and "make it so" is one an operator has to close.
	if len(declared) > 0 {
		actual, err := zpoolVdevs(c, name)
		if err == nil && !sameLayout(declared, actual) {
			warnings = append(warnings, fmt.Sprintf(
				"The pool %s is %s and the declaration asks for %s. This state does not "+
					"reshape a pool that exists: a top-level vdev cannot be removed from "+
					"most pools, and adding one to a mirror makes it a stripe with no undo.",
				name, describeLayout(actual), describeLayout(declared)))
		}
	}
	// Set at creation and never afterwards, so a declaration carrying
	// them against an existing pool is saying something this state
	// cannot do anything about.
	if fs := states.Mapping(args, "filesystem_properties"); fs != nil && fs.Len() > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"filesystem_properties are set when a pool is created, and %s already exists; "+
				"use zfs.filesystem_present on %s to manage them now.", name, name))
	}

	if changes.Len() == 0 {
		res := states.True(fmt.Sprintf("The pool %s exists with the requested properties.", name))
		res.Warnings = warnings
		return res, nil
	}
	if c.Test {
		res := states.WouldChange(fmt.Sprintf("The properties %s of %s would be set.",
			states.SortedNames(changes.StringKeys()), name), changes)
		res.Warnings = warnings
		return res, nil
	}
	for _, e := range changes.Entries() {
		prop := value.KeyString(e.Key)
		v, _ := props.Get(prop)
		if err := zpoolAct(c, []string{"zpool", "set", prop + "=" + value.KeyString(v), name}); err != nil {
			return states.False(fmt.Sprintf("The property %s of %s could not be set: %v", prop, name, err)), nil
		}
	}
	res := states.Changed(fmt.Sprintf("The properties %s of %s were set.",
		states.SortedNames(changes.StringKeys()), name), changes)
	res.Warnings = warnings
	return res, nil
}

func zpoolAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False("This state needs a pool name."), nil
	}
	if c.Which("zpool") == "" {
		return states.False("zpool was not found on this node."), nil
	}
	if !zpoolExists(c, name) {
		return states.True(fmt.Sprintf("The pool %s is not attached to this node.", name)), nil
	}

	export := states.Bool(args, "export", true)
	force := states.Bool(args, "force", false)
	verb, past := "exported", "exported"
	if !export {
		verb, past = "destroyed, with everything on it", "destroyed"
	}

	changes := value.MapOf(name, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The pool %s would be %s.", name, verb), changes), nil
	}

	var err error
	if export {
		err = zpoolExport(c, name, force)
	} else {
		err = zpoolDestroy(c, name, force)
	}
	if err != nil {
		return states.False(fmt.Sprintf("The pool %s could not be %s: %v", name, past, err)), nil
	}
	return states.Changed(fmt.Sprintf("The pool %s was %s.", name, past), changes), nil
}

// argValue reads an argument without coercing it, for the ones whose
// shape is the thing being read.
func argValue(args *value.Map, name string) any {
	v, _ := args.Get(name)
	return v
}
