package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerLVM installs the `lvm` module of SPEC 15.3's Common Linux row,
// and the `lvm` states of SPEC 15.5.
//
// # Why the reporting tools are read as JSON and not as columns
//
// `pvs`, `vgs` and `lvs` print a padded table for a person by default,
// and the width of a column is whatever the widest value on the machine
// happened to be. A `vg_name` with a space in it — LVM allows one — tears
// a `strings.Fields` row in half, and a device path long enough to
// overflow its column pushes every field after it left by one. That is
// the shape DIVERGENCE 5.31 found in `pf`: a reader written against
// remembered output that no real machine quite prints.
//
// LVM2 has emitted `--reportformat json` since 2.02.107 (2014), and every
// distribution this module targets is far past that. Asked with
// `--units b --nosuffix` every size comes back as an exact byte count
// with no `1.5g` to misread as one and a half, and asked with `-o` the
// fields come back named rather than positional. `jail` made the same
// choice against `jls --libxo=json` for the same reason (DIVERGENCE
// 5.32), and SPEC 15.3's own `journald` row asks for the native protocol
// "rather than by parsing `journalctl` output".
//
// # Why every function needs root
//
// LVM keeps its metadata in a label on each PV and a lock under
// `/run/lock/lvm`, and a non-root `pvs` prints a warning and an empty
// report rather than an error. An empty report read as "this node has no
// volume groups" is a false answer to give about a node whose root
// filesystem is on LVM, so the reads are declared `root` alongside the
// writes rather than left to hand back a hollow answer.
//
// # What the mutating half will and will not do
//
// It creates, extends and grows. It does not shrink a volume group by
// removing a disk from it, and `lv_present` grows a logical volume but
// never shrinks one — a shrink that outruns the filesystem on top of it
// destroys data, and `lvresize` here refuses one outright unless a call
// passes `force: true` and names the smaller size on purpose. That is
// the same stance `zpool.present` takes: it "does not reshape an
// existing pool, and reports a layout that does not match as a warning
// instead".
func registerLVM(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "version",
				Doc:       "Return the LVM, device-mapper library and driver versions this node has.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lvmVersion(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "pvs",
				Doc: "Return every LVM physical volume, with its size, free space and the group it belongs to.",
				Params: []signature.Param{
					opt("device", signature.Path, "", "Limit the report to one device, such as /dev/sdb1. Empty reports them all."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lvmReportValue(c, "pv", strings.TrimSpace(states.Str(args, "device", "")))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "vgs",
				Doc: "Return every LVM volume group, with its size, free space and the count of disks and volumes in it.",
				Params: []signature.Param{
					opt("name", signature.String, "", "Limit the report to one volume group. Empty reports them all."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lvmReportValue(c, "vg", strings.TrimSpace(states.Str(args, "name", "")))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "lvs",
				Doc: "Return every LVM logical volume, with its size, device path and type.",
				Params: []signature.Param{
					opt("name", signature.String, "", "Limit the report to `vg/lv`, or to a volume group by naming it alone. Empty reports them all."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lvmReportValue(c, "lv", strings.TrimSpace(states.Str(args, "name", "")))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "pvcreate",
				Doc: "Write an LVM label onto one or more block devices so they can join a volume group.",
				Params: []signature.Param{
					req("devices", signature.List, "The block devices to label, such as [/dev/sdb1]."),
					opt("force", signature.Bool, false, "Pass `-ff`, which lets pvcreate label a device that already carries a filesystem or a partition signature."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmPVCreate,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "pvremove",
				Doc: "Wipe the LVM label from one or more block devices.",
				Params: []signature.Param{
					req("devices", signature.List, "The devices to wipe."),
					opt("force", signature.Bool, false, "Pass `-ff`, which wipes a label even when LVM thinks the device is still in use."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmPVRemove,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "vgcreate",
				Doc: "Create a volume group from one or more physical volumes.",
				Params: []signature.Param{
					req("name", signature.String, "The volume group's name."),
					req("devices", signature.List, "The devices or physical volumes to build it from. A device that is not yet a physical volume is labelled first."),
					opt("physicalextentsize", signature.String, "", "The extent size, such as 4M or 16M. Empty leaves LVM's default."),
					opt("force", signature.Bool, false, "Pass `-f`."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmVGCreate,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "vgextend",
				Doc: "Add one or more physical volumes to an existing volume group.",
				Params: []signature.Param{
					req("name", signature.String, "The volume group to extend."),
					req("devices", signature.List, "The devices to add. One already in the group is skipped."),
					opt("force", signature.Bool, false, "Pass `-f`."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmVGExtend,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "vgremove",
				Doc: "Remove a volume group. Refuses one that still has logical volumes unless `force` is set.",
				Params: []signature.Param{
					req("name", signature.String, "The volume group to remove."),
					opt("force", signature.Bool, false, "Pass `-f`, which removes the group and every logical volume still in it."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmVGRemove,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "lvcreate",
				Doc: "Create a logical volume in a volume group.",
				Params: []signature.Param{
					req("name", signature.String, "The logical volume's name."),
					req("vgname", signature.String, "The volume group to create it in."),
					opt("size", signature.String, "", "An absolute size, such as 10G or 512M. One of size or extents is required unless a thin volume names its pool."),
					opt("extents", signature.String, "", "A size in extents, such as 50%FREE or 256. Mutually exclusive with size."),
					opt("thinpool", signature.String, "", "Create the volume as a thin volume in this pool of the same group. `size` then sets its virtual size."),
					opt("thinpool_create", signature.Bool, false, "Create `name` itself as a thin pool rather than a plain volume. `size` or `extents` sets the pool's real size."),
					opt("stripes", signature.Int, int64(0), "Stripe across this many physical volumes. Zero leaves LVM's default."),
					opt("force", signature.Bool, false, "Pass `-y` to answer LVM's prompts, such as the one about wiping signatures."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmLVCreate,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "lvresize",
				Doc: "Resize a logical volume. A shrink is refused unless `force` is set and a smaller absolute size is named.",
				Params: []signature.Param{
					req("name", signature.String, "The logical volume, as `lv` or `vg/lv`."),
					opt("vgname", signature.String, "", "The volume group, when `name` did not carry it."),
					opt("size", signature.String, "", "The new size. Absolute (10G), or a delta (+2G, -1G). One of size or extents is required."),
					opt("extents", signature.String, "", "The new size in extents, such as 100%VG or +50. Mutually exclusive with size."),
					opt("resizefs", signature.Bool, false, "Pass `--resizefs`, so the filesystem on the volume is resized to match."),
					opt("force", signature.Bool, false, "Allow a shrink. Without this a size smaller than the current one is refused before LVM is called."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmLVResize,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "lvremove",
				Doc: "Remove a logical volume. `-f` is always passed, because an unforced lvremove prompts and a prompt in automation hangs.",
				Params: []signature.Param{
					req("name", signature.String, "The logical volume, as `lv` or `vg/lv`."),
					opt("vgname", signature.String, "", "The volume group, when `name` did not carry it."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: lvmLVRemove,
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "pv_present",
				Doc: "Ensure a block device carries an LVM label.",
				Params: []signature.Param{
					pathParam("The block device. Defaults to the state ID."),
					opt("force", signature.Bool, false, "Label a device that already carries a filesystem signature."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: lvmPVPresentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "pv_absent",
				Doc: "Ensure a block device carries no LVM label.",
				Params: []signature.Param{
					pathParam("The block device. Defaults to the state ID."),
					opt("force", signature.Bool, false, "Wipe the label even when LVM thinks the device is in use."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: lvmPVAbsentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "vg_present",
				Doc: "Ensure a volume group exists and spans at least the named devices.",
				Params: []signature.Param{
					nameParam("The volume group's name. Defaults to the state ID."),
					opt("devices", signature.List, nil, "The devices the group must span. A device not yet in the group is added; one already in it is left alone. Devices are never removed."),
					opt("physicalextentsize", signature.String, "", "The extent size for a group being created. Ignored for one that exists."),
					opt("force", signature.Bool, false, "Pass `-f` to the create or extend."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: lvmVGPresentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "vg_absent",
				Doc: "Ensure a volume group does not exist.",
				Params: []signature.Param{
					nameParam("The volume group's name. Defaults to the state ID."),
					opt("force", signature.Bool, false, "Remove the group even when it still holds logical volumes."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: lvmVGAbsentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "lv_present",
				Doc: "Ensure a logical volume exists, and grow it to a named size. It is never shrunk.",
				Params: []signature.Param{
					nameParam("The logical volume's name. Defaults to the state ID."),
					req("vgname", signature.String, "The volume group it lives in."),
					opt("size", signature.String, "", "An absolute size. On a volume that already exists a larger size grows it; a smaller one is reported and not acted on."),
					opt("extents", signature.String, "", "A size in extents, for creation. Mutually exclusive with size."),
					opt("thinpool", signature.String, "", "Create as a thin volume in this pool."),
					opt("thinpool_create", signature.Bool, false, "Create `name` itself as a thin pool."),
					opt("resizefs", signature.Bool, false, "Resize the filesystem along with a grow."),
					opt("force", signature.Bool, false, "Answer LVM's creation prompts."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: lvmLVPresentState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "lvm", Function: "lv_absent",
				Doc: "Ensure a logical volume does not exist.",
				Params: []signature.Param{
					nameParam("The logical volume's name. Defaults to the state ID."),
					req("vgname", signature.String, "The volume group it lives in."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.5",
			},
			Fn: lvmLVAbsentState,
		},
	)
}

// ---- the report fields ----
//
// Each list is the `-o` argument for one tool. They are named constants
// rather than literals in the call so that lvm_test.go can assert the
// exact set: a field dropped from here is a key that silently reads back
// as the empty string, and a size field added without a matching parse
// below is one nothing looks at.

var (
	lvmPVFields = []string{"pv_name", "vg_name", "pv_size", "pv_free", "pv_uuid", "pv_attr"}
	lvmVGFields = []string{"vg_name", "vg_uuid", "vg_size", "vg_free", "vg_extent_size",
		"vg_extent_count", "vg_free_count", "pv_count", "lv_count"}
	lvmLVFields = []string{"lv_name", "vg_name", "lv_uuid", "lv_path", "lv_size",
		"lv_attr", "origin", "pool_lv", "data_percent"}
)

// lvmToolFor maps the singular report noun to the command that produces
// it. The noun is also the JSON key LVM nests the rows under.
var lvmToolFor = map[string]string{"pv": "pvs", "vg": "vgs", "lv": "lvs"}

func lvmFieldsFor(kind string) []string {
	switch kind {
	case "pv":
		return lvmPVFields
	case "vg":
		return lvmVGFields
	case "lv":
		return lvmLVFields
	}
	return nil
}

// ---- reading ----

// lvmToolsPresent reports whether this node has the LVM command line, or
// an error that names the package it is in.
func lvmToolsPresent(c *exec.Context) error {
	for _, tool := range []string{"pvs", "vgs", "lvs", "lvm"} {
		if c.Which(tool) == "" {
			return fmt.Errorf(
				"this node has no `%s`; the LVM2 command line is the `lvm2` package on Debian and RHEL "+
					"and `lvm2` on Alpine, and it is not installed by default on a machine with no LVM volumes", tool)
		}
	}
	return nil
}

// lvmReportArgv is the read command for one report kind, as a pure
// function of its arguments so lvm_test.go can pin it from any host.
//
// `--units b --nosuffix` is the load-bearing part: without it a size
// comes back as `1.50g`, which parses as 1.5 and is wrong by a factor of
// a billion. `--reportformat json` is the other half — a named-field
// object rather than a table whose columns move with the widest value on
// the machine.
func lvmReportArgv(kind, filter string) []string {
	argv := []string{
		lvmToolFor[kind],
		"--reportformat", "json",
		"--units", "b",
		"--nosuffix",
		"-o", strings.Join(lvmFieldsFor(kind), ","),
	}
	if filter != "" {
		argv = append(argv, filter)
	}
	return argv
}

// lvmReport runs one of pvs/vgs/lvs and returns its rows as name→value
// maps, in the order LVM listed them.
func lvmReport(c *exec.Context, kind, filter string) ([]map[string]string, error) {
	if _, ok := lvmToolFor[kind]; !ok {
		return nil, fmt.Errorf("lvm has no %q report", kind)
	}
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}
	// A filter that names nothing — `vgs vg9` on a node with no vg9 — is
	// an exit 5, not a failure of the report. The rows are read from
	// stdout either way, and an empty list is the honest answer.
	res, err := c.Run(exec.Command{Argv: lvmReportArgv(kind, filter), IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("%s could not be run on this node: %w", lvmToolFor[kind], err)
	}
	rows, err := lvmParseReport(res.Stdout, kind)
	if err != nil {
		if res.Code != 0 && strings.TrimSpace(res.Stdout) == "" {
			// No JSON and a non-zero exit: report what LVM said rather than
			// the parse error, which would only describe the empty string.
			return nil, fmt.Errorf("%s exited %d: %s",
				lvmToolFor[kind], res.Code, strings.TrimSpace(firstLine(res.Stderr)))
		}
		return nil, err
	}
	return rows, nil
}

// lvmParseReport reads LVM's JSON report.
//
// The shape is `{"report":[{"pv":[ {field:value, ...}, ... ]}]}` — an
// array of report objects, each holding an array under the singular noun.
// Every value is a JSON string even where it is a number, which is why
// the rows are `map[string]string` and the numeric columns are parsed
// where they are read rather than here.
func lvmParseReport(out, kind string) ([]map[string]string, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, fmt.Errorf("%s produced no output; expected a JSON report", lvmToolFor[kind])
	}
	var doc struct {
		Report []map[string][]map[string]string `json:"report"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("%s's JSON report did not parse: %w", lvmToolFor[kind], err)
	}
	var rows []map[string]string
	for _, section := range doc.Report {
		rows = append(rows, section[kind]...)
	}
	return rows, nil
}

// lvmReportValue turns a report into the map a caller gets back.
func lvmReportValue(c *exec.Context, kind, filter string) (any, error) {
	rows, err := lvmReport(c, kind, filter)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, lvmRowValue(kind, row))
	}
	return out, nil
}

// lvmRowValue renders one report row, with the byte and count columns as
// integers and everything else as it came.
func lvmRowValue(kind string, row map[string]string) *value.Map {
	numeric := map[string]bool{
		"pv_size": true, "pv_free": true,
		"vg_size": true, "vg_free": true, "vg_extent_size": true,
		"vg_extent_count": true, "vg_free_count": true, "pv_count": true, "lv_count": true,
		"lv_size": true,
	}
	m := value.NewMap(len(row))
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if numeric[k] {
			m.Set(k, lvmBytes(row[k]))
			continue
		}
		m.Set(k, row[k])
	}
	return m
}

// lvmBytes reads a whole-number column, returning zero for a blank or
// non-numeric value — the columns where that matters are checked by the
// caller.
func lvmBytes(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// lvmVersion parses `lvm version`.
func lvmVersion(c *exec.Context) (any, error) {
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"lvm", "version"}})
	if err != nil {
		return nil, fmt.Errorf("`lvm version` could not be run: %w", err)
	}
	out := value.NewMap(3)
	for _, line := range strings.Split(res.Stdout, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "lvm version":
			out.Set("lvm", val)
		case "library version":
			out.Set("library", val)
		case "driver version":
			out.Set("driver", val)
		}
	}
	if _, ok := out.GetString("lvm"); !ok {
		return nil, fmt.Errorf("`lvm version` did not report a version; it printed %q", firstLine(res.Stdout))
	}
	return out, nil
}

// ---- finders, for idempotence ----

// lvmPV is one physical volume, reduced to what a decision turns on.
type lvmPV struct {
	Name  string
	Group string // "" when the PV is not in a group
}

func lvmPVList(c *exec.Context) ([]lvmPV, error) {
	rows, err := lvmReport(c, "pv", "")
	if err != nil {
		return nil, err
	}
	out := make([]lvmPV, 0, len(rows))
	for _, row := range rows {
		out = append(out, lvmPV{Name: row["pv_name"], Group: strings.TrimSpace(row["vg_name"])})
	}
	return out, nil
}

// lvmFindPV returns the PV record for a device, or nil if the device
// carries no LVM label.
func lvmFindPV(c *exec.Context, device string) (*lvmPV, error) {
	pvs, err := lvmPVList(c)
	if err != nil {
		return nil, err
	}
	for i := range pvs {
		if pvs[i].Name == device {
			return &pvs[i], nil
		}
	}
	return nil, nil
}

// lvmVGExists reports whether a volume group is present.
func lvmVGExists(c *exec.Context, name string) (bool, error) {
	rows, err := lvmReport(c, "vg", "")
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row["vg_name"] == name {
			return true, nil
		}
	}
	return false, nil
}

// lvmLV is one logical volume, reduced to what a decision turns on.
type lvmLV struct {
	Name  string
	Group string
	Bytes int64
	Attr  string
}

// lvmFindLV returns the LV record for `vg/lv`, or nil if it is not there.
func lvmFindLV(c *exec.Context, vg, lv string) (*lvmLV, error) {
	rows, err := lvmReport(c, "lv", "")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row["vg_name"] == vg && row["lv_name"] == lv {
			return &lvmLV{
				Name:  lv,
				Group: vg,
				Bytes: lvmBytes(row["lv_size"]),
				Attr:  row["lv_attr"],
			}, nil
		}
	}
	return nil, nil
}

// ---- argument vectors ----
//
// Each of these is a pure function of its inputs, the fix plan.md §1.4
// drew out of two fixtures that had each forced a branch their platform
// does not take. lvm_test.go pins every one of them without an LVM on
// the box.

func lvmPVCreateArgv(devices []string, force bool) []string {
	argv := []string{"pvcreate"}
	if force {
		argv = append(argv, "-ff", "-y")
	}
	return append(argv, devices...)
}

func lvmPVRemoveArgv(devices []string, force bool) []string {
	argv := []string{"pvremove"}
	if force {
		argv = append(argv, "-ff", "-y")
	}
	return append(argv, devices...)
}

func lvmVGCreateArgv(name string, devices []string, extentSize string, force bool) []string {
	argv := []string{"vgcreate"}
	if force {
		argv = append(argv, "-f")
	}
	if extentSize != "" {
		argv = append(argv, "-s", extentSize)
	}
	argv = append(argv, name)
	return append(argv, devices...)
}

func lvmVGExtendArgv(name string, devices []string, force bool) []string {
	argv := []string{"vgextend"}
	if force {
		argv = append(argv, "-f")
	}
	argv = append(argv, name)
	return append(argv, devices...)
}

func lvmVGRemoveArgv(name string, force bool) []string {
	argv := []string{"vgremove"}
	if force {
		argv = append(argv, "-f")
	}
	return append(argv, name)
}

// lvmLVCreateArgv builds `lvcreate`. It is the widest of these because a
// logical volume has the most shapes: plain, a thin pool, or a thin
// volume inside one.
func lvmLVCreateArgv(o lvmLVCreateOpts) ([]string, error) {
	if o.Name == "" || o.Group == "" {
		return nil, errors.New("a logical volume needs a name and a volume group")
	}
	if strings.ContainsRune(o.Name, '/') {
		return nil, fmt.Errorf("%q is not a logical volume name; name the group separately", o.Name)
	}
	if o.Size != "" && o.Extents != "" {
		return nil, errors.New("size and extents are two ways to say the same thing; give one")
	}
	sizeFlag, sizeVal := "-L", o.Size
	if o.Extents != "" {
		sizeFlag, sizeVal = "-l", o.Extents
	}

	argv := []string{"lvcreate", "-n", o.Name}
	if o.Force {
		argv = append(argv, "-y")
	}
	if o.Stripes > 0 {
		argv = append(argv, "-i", strconv.FormatInt(o.Stripes, 10))
	}

	switch {
	case o.ThinPool != "":
		// A thin volume: virtual size is mandatory, and it lands in an
		// existing pool of the same group.
		if sizeVal == "" || sizeFlag != "-L" {
			return nil, errors.New("a thin volume needs a virtual size given as `size`")
		}
		argv = append(argv, "-V", sizeVal, "--thinpool", o.ThinPool, o.Group)
	case o.ThinPoolCreate:
		if sizeVal == "" {
			return nil, errors.New("a thin pool needs a real size given as `size` or `extents`")
		}
		argv = append(argv, "--type", "thin-pool", sizeFlag, sizeVal, o.Group)
	default:
		if sizeVal == "" {
			return nil, errors.New("a logical volume needs a size, given as `size` or `extents`")
		}
		argv = append(argv, sizeFlag, sizeVal, o.Group)
	}
	return argv, nil
}

type lvmLVCreateOpts struct {
	Name           string
	Group          string
	Size           string
	Extents        string
	ThinPool       string
	ThinPoolCreate bool
	Stripes        int64
	Force          bool
}

// lvmLVResizeArgv builds `lvresize`. The shrink guard is in the caller,
// not here, because the caller is the one that has read the current
// size.
func lvmLVResizeArgv(ref, size, extents string, resizefs, force bool) ([]string, error) {
	if size != "" && extents != "" {
		return nil, errors.New("size and extents are two ways to say the same thing; give one")
	}
	if size == "" && extents == "" {
		return nil, errors.New("lvresize needs a new size, given as `size` or `extents`")
	}
	argv := []string{"lvresize"}
	if force {
		// lvresize prompts before a shrink and again before touching a
		// mounted filesystem; -f answers both. It is only ever passed
		// when a call asked for it.
		argv = append(argv, "-f")
	}
	if resizefs {
		argv = append(argv, "--resizefs")
	}
	if size != "" {
		argv = append(argv, "-L", size)
	} else {
		argv = append(argv, "-l", extents)
	}
	return append(argv, ref), nil
}

func lvmLVRemoveArgv(ref string, force bool) []string {
	argv := []string{"lvremove"}
	if force {
		argv = append(argv, "-f")
	}
	return append(argv, ref)
}

// lvmLVRef resolves the two spellings of a logical volume — `vg/lv`, or
// `lv` with the group in a separate argument — into the `vg/lv` LVM
// wants, and refuses the ambiguous case where both name a group and they
// disagree.
func lvmLVRef(name, vgname string) (vg, lv, ref string, err error) {
	name = strings.TrimSpace(name)
	vgname = strings.TrimSpace(vgname)
	if name == "" {
		return "", "", "", errors.New("a logical volume must be named")
	}
	if g, l, ok := strings.Cut(name, "/"); ok {
		if vgname != "" && vgname != g {
			return "", "", "", fmt.Errorf("name says the group is %q and vgname says %q", g, vgname)
		}
		if g == "" || l == "" {
			return "", "", "", fmt.Errorf("%q is not a `vg/lv` reference", name)
		}
		return g, l, g + "/" + l, nil
	}
	if vgname == "" {
		return "", "", "", fmt.Errorf("%q does not name its volume group; pass vgname or write it as vg/lv", name)
	}
	return vgname, name, vgname + "/" + name, nil
}

// lvmParseSize turns an LVM size argument into a byte count.
//
// ok is false for a relative size (+2G, -1G) or one in percent
// (100%FREE): neither can be compared against a current size without
// knowing the group's free space and extent size, so those are handed
// straight to LVM, which does that arithmetic itself. This is only used
// to decide whether a resize is a grow or a shrink when the target is
// stated absolutely.
func lvmParseSize(s string) (bytes int64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") || strings.Contains(s, "%") {
		return 0, false
	}
	unit := byte('m') // LVM's default unit for -L is MiB.
	digits := s
	if last := s[len(s)-1]; (last >= 'a' && last <= 'z') || (last >= 'A' && last <= 'Z') {
		unit = last | 0x20
		digits = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(digits), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	var mult float64
	switch unit {
	case 'b':
		mult = 1
	case 's':
		mult = 512 // a sector
	case 'k':
		mult = 1 << 10
	case 'm':
		mult = 1 << 20
	case 'g':
		mult = 1 << 30
	case 't':
		mult = 1 << 40
	case 'p':
		mult = 1 << 50
	case 'e':
		mult = 1 << 60
	default:
		return 0, false
	}
	return int64(f * mult), true
}

// ---- execution: writing ----

func lvmDevices(args *value.Map) ([]string, error) {
	var out []string
	for _, d := range states.Strings(args, "devices") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one device must be named")
	}
	return out, nil
}

func lvmMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
	out := value.NewMap(3)
	out.Set("changed", changed)
	if c.Test && changed {
		out.Set("comment", comment+" Nothing was changed: this was a test run.")
	} else {
		out.Set("comment", comment)
	}
	if change != nil {
		out.Set("changes", change)
	}
	return out
}

// lvmRun executes a mutating LVM command, turning a non-zero exit into an
// error that carries what LVM said.
func lvmRun(c *exec.Context, argv []string) error {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

func lvmPVCreate(c *exec.Context, args *value.Map) (any, error) {
	devices, err := lvmDevices(args)
	if err != nil {
		return nil, err
	}
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	existing, err := lvmPVList(c)
	if err != nil {
		return nil, err
	}
	isPV := map[string]bool{}
	for _, pv := range existing {
		isPV[pv.Name] = true
	}
	var todo []string
	for _, d := range devices {
		if !isPV[d] {
			todo = append(todo, d)
		}
	}
	if len(todo) == 0 {
		return lvmMutateResult(c, false, fmt.Sprintf("%s already carry an LVM label.", strings.Join(devices, ", ")), nil), nil
	}

	change := value.MapOf("labelled", states.Change(nil, todo))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("%s would be labelled as physical volumes.", strings.Join(todo, ", ")), change), nil
	}
	if err := lvmRun(c, lvmPVCreateArgv(todo, force)); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("%s were labelled as physical volumes.", strings.Join(todo, ", ")), change), nil
}

func lvmPVRemove(c *exec.Context, args *value.Map) (any, error) {
	devices, err := lvmDevices(args)
	if err != nil {
		return nil, err
	}
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	existing, err := lvmPVList(c)
	if err != nil {
		return nil, err
	}
	group := map[string]string{}
	isPV := map[string]bool{}
	for _, pv := range existing {
		isPV[pv.Name] = true
		group[pv.Name] = pv.Group
	}
	var todo []string
	for _, d := range devices {
		if !isPV[d] {
			continue
		}
		if g := group[d]; g != "" && !force {
			return nil, fmt.Errorf(
				"%s is still in volume group %q; remove it from the group first, or pass force to wipe it anyway", d, g)
		}
		todo = append(todo, d)
	}
	if len(todo) == 0 {
		return lvmMutateResult(c, false, fmt.Sprintf("%s carry no LVM label.", strings.Join(devices, ", ")), nil), nil
	}

	change := value.MapOf("wiped", states.Change(todo, nil))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("the LVM label would be wiped from %s.", strings.Join(todo, ", ")), change), nil
	}
	if err := lvmRun(c, lvmPVRemoveArgv(todo, force)); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("the LVM label was wiped from %s.", strings.Join(todo, ", ")), change), nil
}

func lvmVGCreate(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a volume group must be named")
	}
	if strings.ContainsRune(name, '/') {
		return nil, fmt.Errorf("%q is not a volume group name", name)
	}
	devices, err := lvmDevices(args)
	if err != nil {
		return nil, err
	}
	extentSize := strings.TrimSpace(states.Str(args, "physicalextentsize", ""))
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	exists, err := lvmVGExists(c, name)
	if err != nil {
		return nil, err
	}
	if exists {
		// vgcreate does not extend an existing group, and re-running it
		// against one is an error. The idempotent answer is to say it is
		// already there and point at the tool that would add to it.
		return lvmMutateResult(c, false,
			fmt.Sprintf("volume group %q already exists; use lvm.vgextend to add devices to it.", name), nil), nil
	}
	if err := lvmDevicesFreeFor(c, name, devices); err != nil {
		return nil, err
	}

	change := value.MapOf("created", states.Change(nil, value.MapOf("name", name, "devices", devices)))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("volume group %q would be created from %s.", name, strings.Join(devices, ", ")), change), nil
	}
	if err := lvmRun(c, lvmVGCreateArgv(name, devices, extentSize, force)); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("volume group %q was created from %s.", name, strings.Join(devices, ", ")), change), nil
}

// lvmDevicesFreeFor refuses a device that already belongs to a different
// volume group, before LVM is asked to do it and fails less clearly.
func lvmDevicesFreeFor(c *exec.Context, vg string, devices []string) error {
	pvs, err := lvmPVList(c)
	if err != nil {
		return err
	}
	owner := map[string]string{}
	for _, pv := range pvs {
		owner[pv.Name] = pv.Group
	}
	for _, d := range devices {
		if g := owner[d]; g != "" && g != vg {
			return fmt.Errorf("%s already belongs to volume group %q", d, g)
		}
	}
	return nil
}

func lvmVGExtend(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a volume group must be named")
	}
	devices, err := lvmDevices(args)
	if err != nil {
		return nil, err
	}
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	exists, err := lvmVGExists(c, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("volume group %q does not exist; create it with lvm.vgcreate", name)
	}

	pvs, err := lvmPVList(c)
	if err != nil {
		return nil, err
	}
	owner := map[string]string{}
	for _, pv := range pvs {
		owner[pv.Name] = pv.Group
	}
	var todo []string
	for _, d := range devices {
		switch owner[d] {
		case name:
			// already a member
		case "":
			todo = append(todo, d)
		default:
			return nil, fmt.Errorf("%s already belongs to volume group %q", d, owner[d])
		}
	}
	if len(todo) == 0 {
		return lvmMutateResult(c, false, fmt.Sprintf("volume group %q already spans %s.", name, strings.Join(devices, ", ")), nil), nil
	}

	change := value.MapOf("added", states.Change(nil, todo))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("%s would be added to volume group %q.", strings.Join(todo, ", "), name), change), nil
	}
	if err := lvmRun(c, lvmVGExtendArgv(name, todo, force)); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("%s were added to volume group %q.", strings.Join(todo, ", "), name), change), nil
}

func lvmVGRemove(c *exec.Context, args *value.Map) (any, error) {
	name := strings.TrimSpace(states.Str(args, "name", ""))
	if name == "" {
		return nil, errors.New("a volume group must be named")
	}
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	exists, err := lvmVGExists(c, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return lvmMutateResult(c, false, fmt.Sprintf("volume group %q does not exist.", name), nil), nil
	}

	lvs, err := lvmReport(c, "lv", "")
	if err != nil {
		return nil, err
	}
	var held []string
	for _, row := range lvs {
		if row["vg_name"] == name {
			held = append(held, row["lv_name"])
		}
	}
	if len(held) > 0 && !force {
		sort.Strings(held)
		return nil, fmt.Errorf(
			"volume group %q still holds logical volumes (%s); remove them first, or pass force to take the group and all of them",
			name, strings.Join(held, ", "))
	}

	change := value.MapOf("removed", states.Change(name, nil))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("volume group %q would be removed.", name), change), nil
	}
	if err := lvmRun(c, lvmVGRemoveArgv(name, force)); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("volume group %q was removed.", name), change), nil
}

func lvmLVCreate(c *exec.Context, args *value.Map) (any, error) {
	opts := lvmLVCreateOpts{
		Name:           strings.TrimSpace(states.Str(args, "name", "")),
		Group:          strings.TrimSpace(states.Str(args, "vgname", "")),
		Size:           strings.TrimSpace(states.Str(args, "size", "")),
		Extents:        strings.TrimSpace(states.Str(args, "extents", "")),
		ThinPool:       strings.TrimSpace(states.Str(args, "thinpool", "")),
		ThinPoolCreate: states.Bool(args, "thinpool_create", false),
		Stripes:        states.Int(args, "stripes", 0),
		Force:          states.Bool(args, "force", false),
	}
	argv, err := lvmLVCreateArgv(opts)
	if err != nil {
		return nil, err
	}
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	exists, err := lvmVGExists(c, opts.Group)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("volume group %q does not exist", opts.Group)
	}
	lv, err := lvmFindLV(c, opts.Group, opts.Name)
	if err != nil {
		return nil, err
	}
	if lv != nil {
		return lvmMutateResult(c, false, fmt.Sprintf("logical volume %s/%s already exists.", opts.Group, opts.Name), nil), nil
	}

	change := value.MapOf("created", states.Change(nil, value.MapOf(
		"name", opts.Group+"/"+opts.Name, "size", firstNonEmpty(opts.Size, opts.Extents))))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("logical volume %s/%s would be created.", opts.Group, opts.Name), change), nil
	}
	if err := lvmRun(c, argv); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("logical volume %s/%s was created.", opts.Group, opts.Name), change), nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func lvmLVResize(c *exec.Context, args *value.Map) (any, error) {
	vg, lv, ref, err := lvmLVRef(states.Str(args, "name", ""), states.Str(args, "vgname", ""))
	if err != nil {
		return nil, err
	}
	size := strings.TrimSpace(states.Str(args, "size", ""))
	extents := strings.TrimSpace(states.Str(args, "extents", ""))
	resizefs := states.Bool(args, "resizefs", false)
	force := states.Bool(args, "force", false)

	argv, err := lvmLVResizeArgv(ref, size, extents, resizefs, force)
	if err != nil {
		return nil, err
	}
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	current, err := lvmFindLV(c, vg, lv)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("logical volume %s does not exist", ref)
	}

	// The shrink guard. An absolute target below the current size is
	// refused here, before LVM is called, unless the call passed force
	// *and* the smaller size was stated on purpose. A relative or
	// percentage size cannot be judged from here and is left to LVM,
	// which has its own prompt.
	if want, ok := lvmParseSize(size); ok {
		switch {
		case want == current.Bytes:
			return lvmMutateResult(c, false, fmt.Sprintf("logical volume %s is already %d bytes.", ref, want), nil), nil
		case want < current.Bytes && !force:
			return nil, fmt.Errorf(
				"logical volume %s is %d bytes and the target is %d: a shrink can outrun the filesystem on top of it and lose data; "+
					"pass force to allow it", ref, current.Bytes, want)
		}
	}

	change := value.MapOf("size", states.Change(current.Bytes, firstNonEmpty(size, extents)))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("logical volume %s would be resized.", ref), change), nil
	}
	if err := lvmRun(c, argv); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("logical volume %s was resized.", ref), change), nil
}

func lvmLVRemove(c *exec.Context, args *value.Map) (any, error) {
	vg, lv, ref, err := lvmLVRef(states.Str(args, "name", ""), states.Str(args, "vgname", ""))
	if err != nil {
		return nil, err
	}
	if err := lvmToolsPresent(c); err != nil {
		return nil, err
	}

	current, err := lvmFindLV(c, vg, lv)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return lvmMutateResult(c, false, fmt.Sprintf("logical volume %s does not exist.", ref), nil), nil
	}

	change := value.MapOf("removed", states.Change(ref, nil))
	if c.Test {
		return lvmMutateResult(c, true, fmt.Sprintf("logical volume %s would be removed.", ref), change), nil
	}
	if err := lvmRun(c, lvmLVRemoveArgv(ref, true)); err != nil {
		return nil, err
	}
	return lvmMutateResult(c, true, fmt.Sprintf("logical volume %s was removed.", ref), change), nil
}

// ---- states ----

func lvmStateName(args *value.Map) string {
	return strings.TrimSpace(states.Str(args, "name", ""))
}

func lvmPVPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	device := lvmStateName(args)
	if device == "" {
		return states.False("a block device must be named"), nil
	}
	if err := lvmToolsPresent(c); err != nil {
		return states.False(err.Error()), nil
	}
	pv, err := lvmFindPV(c, device)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if pv != nil {
		return states.True(fmt.Sprintf("%s already carries an LVM label.", device)), nil
	}

	changes := value.MapOf(device, states.Change("no label", "physical volume"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be labelled as a physical volume.", device), changes), nil
	}
	if err := lvmRun(c, lvmPVCreateArgv([]string{device}, states.Bool(args, "force", false))); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("%s was labelled as a physical volume.", device), changes), nil
}

func lvmPVAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	device := lvmStateName(args)
	if device == "" {
		return states.False("a block device must be named"), nil
	}
	if err := lvmToolsPresent(c); err != nil {
		return states.False(err.Error()), nil
	}
	pv, err := lvmFindPV(c, device)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if pv == nil {
		return states.True(fmt.Sprintf("%s carries no LVM label.", device)), nil
	}
	force := states.Bool(args, "force", false)
	if pv.Group != "" && !force {
		return states.False(fmt.Sprintf(
			"%s is still in volume group %q; remove it from the group first, or set force", device, pv.Group)), nil
	}

	changes := value.MapOf(device, states.Change("physical volume", "no label"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("the LVM label would be wiped from %s.", device), changes), nil
	}
	if err := lvmRun(c, lvmPVRemoveArgv([]string{device}, force)); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("the LVM label was wiped from %s.", device), changes), nil
}

func lvmVGPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := lvmStateName(args)
	if name == "" {
		return states.False("a volume group must be named"), nil
	}
	devices := states.Strings(args, "devices")
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return states.False(err.Error()), nil
	}

	exists, err := lvmVGExists(c, name)
	if err != nil {
		return states.False(err.Error()), nil
	}

	if !exists {
		if len(devices) == 0 {
			return states.False(fmt.Sprintf(
				"volume group %q does not exist and no devices were given to build it from", name)), nil
		}
		if err := lvmDevicesFreeFor(c, name, devices); err != nil {
			return states.False(err.Error()), nil
		}
		changes := value.MapOf(name, states.Change(nil, value.MapOf("devices", devices)))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("volume group %q would be created from %s.",
				name, strings.Join(devices, ", ")), changes), nil
		}
		extentSize := strings.TrimSpace(states.Str(args, "physicalextentsize", ""))
		if err := lvmRun(c, lvmVGCreateArgv(name, devices, extentSize, force)); err != nil {
			return states.False(err.Error()), nil
		}
		return states.Changed(fmt.Sprintf("volume group %q was created from %s.",
			name, strings.Join(devices, ", ")), changes), nil
	}

	// The group is there. The only convergence left is adding any named
	// devices it does not span yet. Removing a device is never done: it
	// moves the data that is on it, which is not a thing a state should
	// do as a side effect of a list that got shorter.
	if len(devices) == 0 {
		return states.True(fmt.Sprintf("volume group %q exists.", name)), nil
	}
	pvs, err := lvmPVList(c)
	if err != nil {
		return states.False(err.Error()), nil
	}
	owner := map[string]string{}
	for _, pv := range pvs {
		owner[pv.Name] = pv.Group
	}
	var todo []string
	for _, d := range devices {
		d = strings.TrimSpace(d)
		switch owner[d] {
		case name, "":
			if owner[d] == "" {
				todo = append(todo, d)
			}
		default:
			return states.False(fmt.Sprintf("%s already belongs to volume group %q", d, owner[d])), nil
		}
	}
	if len(todo) == 0 {
		return states.True(fmt.Sprintf("volume group %q already spans %s.", name, strings.Join(devices, ", "))), nil
	}

	changes := value.MapOf(name, states.Change("without "+strings.Join(todo, ", "), "with "+strings.Join(todo, ", ")))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be added to volume group %q.",
			strings.Join(todo, ", "), name), changes), nil
	}
	if err := lvmRun(c, lvmVGExtendArgv(name, todo, force)); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("%s were added to volume group %q.", strings.Join(todo, ", "), name), changes), nil
}

func lvmVGAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	name := lvmStateName(args)
	if name == "" {
		return states.False("a volume group must be named"), nil
	}
	force := states.Bool(args, "force", false)
	if err := lvmToolsPresent(c); err != nil {
		return states.False(err.Error()), nil
	}
	exists, err := lvmVGExists(c, name)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if !exists {
		return states.True(fmt.Sprintf("volume group %q does not exist.", name)), nil
	}

	lvs, err := lvmReport(c, "lv", "")
	if err != nil {
		return states.False(err.Error()), nil
	}
	var held []string
	for _, row := range lvs {
		if row["vg_name"] == name {
			held = append(held, row["lv_name"])
		}
	}
	if len(held) > 0 && !force {
		sort.Strings(held)
		return states.False(fmt.Sprintf(
			"volume group %q still holds logical volumes (%s); set force to remove the group and all of them",
			name, strings.Join(held, ", "))), nil
	}

	changes := value.MapOf(name, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("volume group %q would be removed.", name), changes), nil
	}
	if err := lvmRun(c, lvmVGRemoveArgv(name, force)); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("volume group %q was removed.", name), changes), nil
}

func lvmLVPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	lvName := lvmStateName(args)
	vg := strings.TrimSpace(states.Str(args, "vgname", ""))
	if lvName == "" || vg == "" {
		return states.False("a logical volume needs a name and a vgname"), nil
	}
	size := strings.TrimSpace(states.Str(args, "size", ""))
	extents := strings.TrimSpace(states.Str(args, "extents", ""))
	if err := lvmToolsPresent(c); err != nil {
		return states.False(err.Error()), nil
	}

	exists, err := lvmVGExists(c, vg)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if !exists {
		return states.False(fmt.Sprintf("volume group %q does not exist", vg)), nil
	}
	current, err := lvmFindLV(c, vg, lvName)
	if err != nil {
		return states.False(err.Error()), nil
	}

	if current == nil {
		opts := lvmLVCreateOpts{
			Name:           lvName,
			Group:          vg,
			Size:           size,
			Extents:        extents,
			ThinPool:       strings.TrimSpace(states.Str(args, "thinpool", "")),
			ThinPoolCreate: states.Bool(args, "thinpool_create", false),
			Force:          states.Bool(args, "force", false),
		}
		argv, err := lvmLVCreateArgv(opts)
		if err != nil {
			return states.False(err.Error()), nil
		}
		changes := value.MapOf(vg+"/"+lvName, states.Change(nil, "created"))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("logical volume %s/%s would be created.", vg, lvName), changes), nil
		}
		if err := lvmRun(c, argv); err != nil {
			return states.False(err.Error()), nil
		}
		return states.Changed(fmt.Sprintf("logical volume %s/%s was created.", vg, lvName), changes), nil
	}

	// The volume is there. A grow to a larger absolute size is done; a
	// smaller one is reported and left, because shrinking a logical
	// volume out from under its filesystem loses data and a state that
	// did it because a number in a tree got smaller would be a bad
	// surprise. lvm.lvresize with force is the deliberate path for that.
	want, absolute := lvmParseSize(size)
	if size == "" || !absolute || want <= current.Bytes {
		res := states.True(fmt.Sprintf("logical volume %s/%s exists.", vg, lvName))
		if absolute && want < current.Bytes {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s/%s is %d bytes, larger than the declared %d; lvm.lv_present never shrinks a volume. Use lvm.lvresize with force.",
					vg, lvName, current.Bytes, want))
		}
		return res, nil
	}

	ref := vg + "/" + lvName
	resizefs := states.Bool(args, "resizefs", false)
	argv, err := lvmLVResizeArgv(ref, size, "", resizefs, false)
	if err != nil {
		return states.False(err.Error()), nil
	}
	changes := value.MapOf(ref, states.Change(current.Bytes, size))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("logical volume %s would be grown to %s.", ref, size), changes), nil
	}
	if err := lvmRun(c, argv); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("logical volume %s was grown to %s.", ref, size), changes), nil
}

func lvmLVAbsentState(c *exec.Context, args *value.Map) (states.Result, error) {
	lvName := lvmStateName(args)
	vg := strings.TrimSpace(states.Str(args, "vgname", ""))
	if lvName == "" || vg == "" {
		return states.False("a logical volume needs a name and a vgname"), nil
	}
	ref := vg + "/" + lvName
	if err := lvmToolsPresent(c); err != nil {
		return states.False(err.Error()), nil
	}
	current, err := lvmFindLV(c, vg, lvName)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if current == nil {
		return states.True(fmt.Sprintf("logical volume %s does not exist.", ref)), nil
	}

	changes := value.MapOf(ref, states.Change("present", nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("logical volume %s would be removed.", ref), changes), nil
	}
	if err := lvmRun(c, lvmLVRemoveArgv(ref, true)); err != nil {
		return states.False(err.Error()), nil
	}
	return states.Changed(fmt.Sprintf("logical volume %s was removed.", ref), changes), nil
}
