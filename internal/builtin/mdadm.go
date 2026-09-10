package builtin

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// ProcMdstatPath is the kernel's RAID status file. A variable so a test
// can point it at a fixture.
var ProcMdstatPath = "/proc/mdstat"

// MdadmConfPaths is where `save_config` writes each distribution's
// boot-time array list. A variable so a test can redirect it.
var MdadmConfPaths = []string{"/etc/mdadm/mdadm.conf", "/etc/mdadm.conf"}

// registerMdadm installs the `mdadm` module of SPEC 15.3's Common Linux
// and Storage rows.
//
// # Reading: `--detail`, and /proc/mdstat for what it is doing right now
//
// `mdadm` has no JSON output. `--detail --export` is a `KEY=value` list
// but carries no health — not whether the array is degraded, not which
// member has failed. So `detail` parses the human `mdadm --detail`,
// which is `Label : Value` with a closed label vocabulary and a
// fixed-column member table, not the drifting `-o short` shape
// DIVERGENCE 5.31 was about. The one thing it does not show well is
// progress, so `mdstat` reads `/proc/mdstat` directly — the resync,
// recovery and reshape percentages, the way `zpool status` reads a
// scrub.
//
// # There is deliberately no `mdadm` state
//
// SPEC 15.5 names one for `lvm`, `zfs` and `zpool` and none for this,
// and that is right: creating or reshaping an array is a careful,
// destructive, one-time operation, not a target a convergence loop
// re-checks every run. Boot-time assembly is `mdadm.conf` and the
// initramfs's job, which `save_config` writes and `file.managed` can
// template. `pam` and `journald` have no state for the same kind of
// reason.
//
// # What `create` will not do without `force`
//
// `mdadm --create` overwrites whatever is on the member devices. This
// module refuses a device that already hosts an array, and refuses a
// member that already carries an md superblock (`--examine` succeeds)
// or that mdadm itself flags as holding a filesystem, unless the call
// passes `force`. `--run` is always passed so mdadm does not stop on
// its interactive prompt.
func registerMdadm(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "version",
				Doc:       "Return the mdadm version.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return mdadmVersion(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "list",
				Doc:        "Return every assembled md array, with its uuid, level and member count.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return mdadmList(c) },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "detail",
				Doc: "Return one array's level, state, member devices and their health, and any resync in progress.",
				Params: []signature.Param{
					req("device", signature.Path, "The array, such as /dev/md0."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				dev := strings.TrimSpace(states.Str(args, "device", ""))
				if dev == "" {
					return nil, errors.New("an array device must be named")
				}
				d, err := mdadmDetail(c, dev)
				if err != nil {
					return nil, err
				}
				return mdadmDetailValue(d), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "examine",
				Doc: "Return the md superblock a block device carries: which array it belongs to, its role and its event count.",
				Params: []signature.Param{
					req("device", signature.Path, "The component device, such as /dev/sdb1."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmExamineFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "mdstat",
				Doc:       "Return /proc/mdstat parsed: each array's state, members and any resync, recovery or reshape progress.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				text, err := os.ReadFile(ProcMdstatPath)
				if err != nil {
					return nil, fmt.Errorf("%s could not be read: %w", ProcMdstatPath, err)
				}
				return mdadmMdstatValue(mdadmParseMdstat(string(text))), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "create",
				Doc: "Create a new array. Refuses a device that is already an array, or a member that already holds one, without force.",
				Params: []signature.Param{
					req("device", signature.Path, "The array device to create, such as /dev/md0."),
					req("level", signature.String, "The RAID level: 0, 1, 4, 5, 6, 10, or a name like raid1."),
					req("devices", signature.List, "The member block devices."),
					opt("spares", signature.List, nil, "Extra devices to add as spares."),
					opt("metadata", signature.String, "", "Superblock version, such as 1.2 or 0.90. Empty leaves mdadm's default."),
					opt("name", signature.String, "", "A name for the array, stored in the superblock."),
					opt("chunk", signature.String, "", "Chunk size, such as 512K. For striped levels."),
					opt("assume_clean", signature.Bool, false, "Skip the initial resync. Only safe on devices known to be zeroed or when the data is disposable."),
					opt("force", signature.Bool, false, "Overwrite an existing md superblock or a detected filesystem on a member."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmCreateFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "assemble",
				Doc: "Assemble an existing array from its members, or from the superblocks found by a scan.",
				Params: []signature.Param{
					opt("device", signature.Path, "", "The array device. Empty with scan assembles everything found."),
					opt("devices", signature.List, nil, "The members to assemble. Empty uses a scan."),
					opt("uuid", signature.String, "", "Limit a scan to the array with this uuid."),
					opt("scan", signature.Bool, false, "Assemble from `mdadm.conf` and the superblocks on disk."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmAssembleFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "stop",
				Doc: "Stop a running array, releasing its members. Refuses one that is mounted or in use.",
				Params: []signature.Param{
					req("device", signature.Path, "The array to stop."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmStopFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "add",
				Doc: "Add a device to an array, as a spare or as a replacement for a missing member.",
				Params: []signature.Param{
					req("device", signature.Path, "The array."),
					req("component", signature.Path, "The block device to add."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return mdadmMember(c, args, "add") },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "fail",
				Doc: "Mark a member device as failed, so it can be replaced.",
				Params: []signature.Param{
					req("device", signature.Path, "The array."),
					req("component", signature.Path, "The member to fail."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return mdadmMember(c, args, "fail") },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "remove",
				Doc: "Remove a failed or detached member from an array.",
				Params: []signature.Param{
					req("device", signature.Path, "The array."),
					req("component", signature.Path, "The member to remove."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return mdadmMember(c, args, "remove") },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "grow",
				Doc: "Grow an array to more member devices, or to the full size of its members. A reshape can take hours.",
				Params: []signature.Param{
					req("device", signature.Path, "The array."),
					opt("raid_devices", signature.Int, int64(0), "The new member count. A reshape."),
					opt("size", signature.String, "", "`max` to use the full size of each member, or a size such as 100G."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmGrowFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mdadm", Function: "save_config",
				Doc: "Write the running arrays into the distribution's mdadm.conf, so they assemble at boot. Non-ARRAY lines are kept.",
				Params: []signature.Param{
					opt("path", signature.Path, "", "Where to write. Empty picks the distribution's own path."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmSaveConfigFn,
		},
	)
}

// ---- tools ----

func mdadmToolPresent(c *exec.Context) error {
	if c.Which("mdadm") == "" {
		return errors.New(
			"this node has no `mdadm`; Linux software RAID is the `mdadm` package, and it is not installed " +
				"by default on a machine with no md arrays")
	}
	return nil
}

func mdadmVersion(c *exec.Context) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	// mdadm prints its version banner to stderr and exits non-zero for
	// `--version`.
	res, err := c.Run(exec.Command{Argv: []string{"mdadm", "--version"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("`mdadm --version` could not be run: %w", err)
	}
	line := strings.TrimSpace(res.Stderr + res.Stdout)
	// `mdadm - v4.3 - 2024-02-15 - Ubuntu 4.3-1ubuntu2.1`
	out := value.NewMap(1)
	if _, rest, ok := strings.Cut(line, " - "); ok {
		ver, _, _ := strings.Cut(rest, " - ")
		out.Set("version", strings.TrimPrefix(strings.TrimSpace(ver), "v"))
	} else {
		out.Set("version", line)
	}
	return out, nil
}

// ---- reading ----

// mdadmArray is one array, as `detail` reports it.
type mdadmArray struct {
	Device        string
	Name          string
	Level         string
	UUID          string
	Metadata      string
	State         string
	RaidDevices   int64
	TotalDevices  int64
	ActiveDevices int64
	WorkDevices   int64
	FailedDevices int64
	SpareDevices  int64
	ArraySizeKB   int64
	ResyncAction  string // "resync", "recover", "reshape", "check", or ""
	ResyncPct     string // "27% complete", or ""
	Members       []mdadmMemberInfo
}

type mdadmMemberInfo struct {
	Device     string
	RaidDevice string // "0".."N", or "-" for a spare/faulty/removed slot
	State      string // "active sync", "spare", "faulty", "removed", "spare rebuilding"
}

// mdadmDetail runs `mdadm --detail` and parses the human report.
func mdadmDetail(c *exec.Context, device string) (*mdadmArray, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"mdadm", "--detail", device}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("mdadm could not be run: %w", err)
	}
	if res.Code != 0 {
		msg := strings.TrimSpace(res.Stderr + res.Stdout)
		if strings.Contains(msg, "does not appear to be an md device") ||
			strings.Contains(msg, "cannot open") || strings.Contains(msg, "No such file") {
			return nil, fmt.Errorf("%s is not an assembled md array", device)
		}
		return nil, fmt.Errorf("`mdadm --detail %s` exited %d: %s", device, res.Code, firstLine(msg))
	}
	a := mdadmParseDetail(res.Stdout)
	a.Device = device
	return a, nil
}

// mdadmParseDetail reads the `mdadm --detail` report.
//
// The header is `Label : Value` with a fixed label set; the split is on
// the first colon only, because a value like a creation time carries its
// own. The member table begins after the `Number Major Minor` line and
// each row is `Number Major Minor RaidDevice <state words...> [/dev/...]`
// — a `removed` slot has no device path, which is why the path is taken
// from the end rather than a fixed column.
func mdadmParseDetail(out string) *mdadmArray {
	a := &mdadmArray{}
	inMembers := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Number") && strings.Contains(trimmed, "RaidDevice") {
			inMembers = true
			continue
		}
		if inMembers {
			if m, ok := mdadmParseMemberRow(trimmed); ok {
				a.Members = append(a.Members, m)
			}
			continue
		}
		i := strings.IndexByte(trimmed, ':')
		if i < 0 {
			continue
		}
		label := strings.TrimSpace(trimmed[:i])
		val := strings.TrimSpace(trimmed[i+1:])
		switch label {
		case "Name":
			// `system76-pc:haltest  (local to host system76-pc)` -- the
			// parenthetical is mdadm's note, not part of the name.
			if k := strings.Index(val, "  ("); k >= 0 {
				val = strings.TrimSpace(val[:k])
			}
			a.Name = val
		case "Raid Level":
			a.Level = val
		case "UUID":
			a.UUID = val
		case "Version":
			a.Metadata = val
		case "State":
			a.State = strings.TrimSpace(val)
		case "Raid Devices":
			a.RaidDevices = mdadmInt(val)
		case "Total Devices":
			a.TotalDevices = mdadmInt(val)
		case "Active Devices":
			a.ActiveDevices = mdadmInt(val)
		case "Working Devices":
			a.WorkDevices = mdadmInt(val)
		case "Failed Devices":
			a.FailedDevices = mdadmInt(val)
		case "Spare Devices":
			a.SpareDevices = mdadmInt(val)
		case "Array Size":
			// `64512 (63.00 MiB 66.06 MB)` -- the leading number is 1K blocks.
			a.ArraySizeKB = mdadmInt(val)
		case "Resync Status", "Rebuild Status", "Reshape Status", "Check Status":
			a.ResyncPct = val
			a.ResyncAction = strings.ToLower(strings.TrimSuffix(label, " Status"))
			if a.ResyncAction == "rebuild" {
				a.ResyncAction = "recover"
			}
		}
	}
	return a
}

// mdadmParseMemberRow reads one line of the member table.
func mdadmParseMemberRow(line string) (mdadmMemberInfo, bool) {
	f := strings.Fields(line)
	if len(f) < 5 {
		return mdadmMemberInfo{}, false
	}
	var m mdadmMemberInfo
	m.RaidDevice = f[3]
	rest := f[4:]
	if last := rest[len(rest)-1]; strings.HasPrefix(last, "/dev/") {
		m.Device = last
		rest = rest[:len(rest)-1]
	}
	m.State = strings.Join(rest, " ")
	if m.State == "" {
		return mdadmMemberInfo{}, false
	}
	return m, true
}

func mdadmInt(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func mdadmDetailValue(a *mdadmArray) *value.Map {
	m := value.NewMap(16)
	m.Set("device", a.Device)
	m.Set("name", nilIfEmpty(a.Name))
	m.Set("level", a.Level)
	m.Set("uuid", nilIfEmpty(a.UUID))
	m.Set("metadata", nilIfEmpty(a.Metadata))
	m.Set("state", a.State)
	m.Set("degraded", strings.Contains(a.State, "degraded"))
	m.Set("raid_devices", a.RaidDevices)
	m.Set("total_devices", a.TotalDevices)
	m.Set("active_devices", a.ActiveDevices)
	m.Set("working_devices", a.WorkDevices)
	m.Set("failed_devices", a.FailedDevices)
	m.Set("spare_devices", a.SpareDevices)
	m.Set("array_size_kb", a.ArraySizeKB)
	if a.ResyncAction != "" || a.ResyncPct != "" {
		rs := value.NewMap(2)
		rs.Set("action", nilIfEmpty(a.ResyncAction))
		rs.Set("progress", nilIfEmpty(a.ResyncPct))
		m.Set("resync", rs)
	} else {
		m.Set("resync", nil)
	}
	members := make([]any, len(a.Members))
	for i, mem := range a.Members {
		mm := value.NewMap(4)
		mm.Set("device", nilIfEmpty(mem.Device))
		mm.Set("raid_device", nilIfDash(mem.RaidDevice))
		mm.Set("state", mem.State)
		mm.Set("faulty", strings.Contains(mem.State, "faulty"))
		members[i] = mm
	}
	m.Set("members", members)
	return m
}

func nilIfDash(s string) any {
	if s == "" || s == "-" {
		return nil
	}
	return s
}

// mdadmList reads `mdadm --detail --scan`.
func mdadmList(c *exec.Context) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"mdadm", "--detail", "--scan"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("mdadm could not be run: %w", err)
	}
	var out []any
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ARRAY ") {
			continue
		}
		fields := strings.Fields(line)
		entry := value.NewMap(5)
		entry.Set("device", fields[1])
		for _, kv := range fields[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "metadata":
				entry.Set("metadata", v)
			case "UUID":
				entry.Set("uuid", v)
			case "name":
				entry.Set("name", v)
			case "spares":
				entry.Set("spares", mdadmInt(v))
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func mdadmExamineFn(c *exec.Context, args *value.Map) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	dev := strings.TrimSpace(states.Str(args, "device", ""))
	if dev == "" {
		return nil, errors.New("a component device must be named")
	}
	res, err := c.Run(exec.Command{Argv: []string{"mdadm", "--examine", "--export", dev}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("mdadm could not be run: %w", err)
	}
	if res.Code != 0 {
		msg := strings.TrimSpace(res.Stderr + res.Stdout)
		if strings.Contains(msg, "No md superblock detected") {
			out := value.NewMap(2)
			out.Set("device", dev)
			out.Set("has_superblock", false)
			return out, nil
		}
		return nil, fmt.Errorf("`mdadm --examine %s` exited %d: %s", dev, res.Code, firstLine(msg))
	}
	out := value.NewMap(8)
	out.Set("device", dev)
	out.Set("has_superblock", true)
	for _, line := range strings.Split(res.Stdout, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		out.Set(strings.ToLower(strings.TrimPrefix(k, "MD_")), v)
	}
	return out, nil
}

// ---- /proc/mdstat ----

type mdadmMdstatArray struct {
	Device      string
	State       string   // "active", "inactive", "active (auto-read-only)"
	Personality string   // "raid1", "raid0", ...
	Members     []string // "loop4[0]", "loop8[2](S)"
	BlocksLine  string
	Progress    string // the "resync = 18.8% ..." line, or ""
}

// mdadmParseMdstat reads /proc/mdstat.
//
//	Personalities : [raid0] [raid1]
//	md127 : active raid1 loop8[2](S) loop7[1] loop4[0]
//	      64512 blocks super 1.2 [2/2] [UU]
//	      [===>.......]  resync = 18.8% (12288/64512) finish=0.0min speed=12288K/sec
func mdadmParseMdstat(text string) []mdadmMdstatArray {
	var (
		arrays []mdadmMdstatArray
		cur    *mdadmMdstatArray
	)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Personalities") || strings.HasPrefix(line, "unused devices") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// `md127 : active raid1 loop8[2](S) ...`
			name, rest, ok := strings.Cut(line, " : ")
			if !ok {
				continue
			}
			arrays = append(arrays, mdadmMdstatArray{Device: "/dev/" + strings.TrimSpace(name)})
			cur = &arrays[len(arrays)-1]
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				cur.State = fields[0]
			}
			for _, f := range fields[1:] {
				if strings.Contains(f, "[") {
					cur.Members = append(cur.Members, f)
				} else if cur.Personality == "" && !strings.HasPrefix(f, "(") {
					cur.Personality = f
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		t := strings.TrimSpace(line)
		switch {
		case strings.Contains(t, "blocks"):
			cur.BlocksLine = t
		case strings.Contains(t, "=") && (strings.Contains(t, "resync") || strings.Contains(t, "recovery") ||
			strings.Contains(t, "reshape") || strings.Contains(t, "check")):
			cur.Progress = t
		}
	}
	return arrays
}

func mdadmMdstatValue(arrays []mdadmMdstatArray) any {
	out := make([]any, len(arrays))
	for i, a := range arrays {
		m := value.NewMap(6)
		m.Set("device", a.Device)
		m.Set("state", a.State)
		m.Set("personality", nilIfEmpty(a.Personality))
		members := make([]any, 0, len(a.Members))
		for _, spec := range a.Members {
			members = append(members, mdadmMemberSpecValue(spec))
		}
		m.Set("members", members)
		if a.BlocksLine != "" {
			total, active, ok := mdadmParseUpCount(a.BlocksLine)
			if ok {
				m.Set("raid_devices", total)
				m.Set("active_devices", active)
				m.Set("degraded", active < total)
			}
		}
		m.Set("progress", nilIfEmpty(a.Progress))
		out[i] = m
	}
	return out
}

// mdadmMemberSpecValue reads `loop8[2](S)` into {name, role, flags}.
func mdadmMemberSpecValue(spec string) *value.Map {
	m := value.NewMap(3)
	name := spec
	var role, flags string
	if i := strings.IndexByte(spec, '['); i >= 0 {
		name = spec[:i]
		rest := spec[i+1:]
		if j := strings.IndexByte(rest, ']'); j >= 0 {
			role = rest[:j]
			flags = strings.TrimSpace(rest[j+1:])
		}
	}
	m.Set("name", name)
	m.Set("role", nilIfEmpty(role))
	m.Set("spare", strings.Contains(flags, "(S)"))
	m.Set("faulty", strings.Contains(flags, "(F)"))
	m.Set("write_mostly", strings.Contains(flags, "(W)"))
	return m
}

// mdadmParseUpCount reads `[2/1]` out of the blocks line.
func mdadmParseUpCount(line string) (total, active int64, ok bool) {
	open := strings.IndexByte(line, '[')
	for open >= 0 {
		close := strings.IndexByte(line[open:], ']')
		if close < 0 {
			return 0, 0, false
		}
		inside := line[open+1 : open+close]
		if a, b, found := strings.Cut(inside, "/"); found {
			t, e1 := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
			ac, e2 := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
			if e1 == nil && e2 == nil {
				return t, ac, true
			}
		}
		line = line[open+close+1:]
		open = strings.IndexByte(line, '[')
	}
	return 0, 0, false
}

// ---- argument vectors ----

func mdadmCreateArgv(o mdadmCreateOpts) ([]string, error) {
	if o.Device == "" {
		return nil, errors.New("the array device must be named")
	}
	if len(o.Devices) < 1 {
		return nil, errors.New("at least one member device is required")
	}
	level := strings.TrimSpace(o.Level)
	if level == "" {
		return nil, errors.New("a RAID level is required")
	}
	argv := []string{"mdadm", "--create", o.Device, "--run", "--level=" + level,
		"--raid-devices=" + strconv.Itoa(len(o.Devices))}
	if len(o.Spares) > 0 {
		argv = append(argv, "--spare-devices="+strconv.Itoa(len(o.Spares)))
	}
	if o.Metadata != "" {
		argv = append(argv, "--metadata="+o.Metadata)
	}
	if o.Name != "" {
		argv = append(argv, "--name="+o.Name)
	}
	if o.Chunk != "" {
		argv = append(argv, "--chunk="+o.Chunk)
	}
	if o.AssumeClean {
		argv = append(argv, "--assume-clean")
	}
	if o.Force {
		argv = append(argv, "--force")
	}
	argv = append(argv, o.Devices...)
	argv = append(argv, o.Spares...)
	return argv, nil
}

type mdadmCreateOpts struct {
	Device      string
	Level       string
	Devices     []string
	Spares      []string
	Metadata    string
	Name        string
	Chunk       string
	AssumeClean bool
	Force       bool
}

func mdadmGrowArgv(device string, raidDevices int64, size string) ([]string, error) {
	if raidDevices <= 0 && size == "" {
		return nil, errors.New("grow needs raid_devices or size")
	}
	argv := []string{"mdadm", "--grow", device}
	if raidDevices > 0 {
		argv = append(argv, "--raid-devices="+strconv.FormatInt(raidDevices, 10))
	}
	if size != "" {
		argv = append(argv, "--size="+size)
	}
	return argv, nil
}

// ---- writing ----

func mdadmMutateResult(c *exec.Context, changed bool, comment string, change *value.Map) *value.Map {
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

func mdadmRun(c *exec.Context, argv []string) (string, error) {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("mdadm could not be run: %w", err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return strings.TrimSpace(res.Stderr + res.Stdout), nil
}

func mdadmArrayExists(c *exec.Context, device string) bool {
	_, err := mdadmDetail(c, device)
	return err == nil
}

func mdadmCreateFn(c *exec.Context, args *value.Map) (any, error) {
	opts := mdadmCreateOpts{
		Device:      strings.TrimSpace(states.Str(args, "device", "")),
		Level:       strings.TrimSpace(states.Str(args, "level", "")),
		Devices:     mdadmDevList(args, "devices"),
		Spares:      mdadmDevList(args, "spares"),
		Metadata:    strings.TrimSpace(states.Str(args, "metadata", "")),
		Name:        strings.TrimSpace(states.Str(args, "name", "")),
		Chunk:       strings.TrimSpace(states.Str(args, "chunk", "")),
		AssumeClean: states.Bool(args, "assume_clean", false),
		Force:       states.Bool(args, "force", false),
	}
	argv, err := mdadmCreateArgv(opts)
	if err != nil {
		return nil, err
	}
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}

	if mdadmArrayExists(c, opts.Device) {
		return mdadmMutateResult(c, false, fmt.Sprintf("%s is already an assembled array.", opts.Device), nil), nil
	}
	// A member that already carries an md superblock is refused unless
	// force -- creating over it would abandon whatever array it was part
	// of.
	if !opts.Force {
		for _, dev := range append(append([]string{}, opts.Devices...), opts.Spares...) {
			ex, err := mdadmExamineFn(c, value.MapOf("device", dev))
			if err != nil {
				continue
			}
			if has, _ := ex.(*value.Map).GetString("has_superblock"); has == true {
				return nil, fmt.Errorf(
					"%s already carries an md superblock; it may belong to another array. "+
						"Pass force to overwrite it", dev)
			}
		}
	}

	levelName := mdadmLevelName(opts.Level)
	change := value.MapOf(opts.Device, states.Change(nil, value.MapOf(
		"level", levelName, "devices", opts.Devices)))
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf(
			"array %s would be created as %s across %s.", opts.Device, levelName, strings.Join(opts.Devices, ", ")), change), nil
	}
	if _, err := mdadmRun(c, argv); err != nil {
		return nil, err
	}
	return mdadmMutateResult(c, true, fmt.Sprintf(
		"array %s was created as %s across %s.", opts.Device, levelName, strings.Join(opts.Devices, ", ")), change), nil
}

// mdadmLevelName renders a level for a message: a bare number gains the
// `raid` prefix, so `1` reads as `raid1`.
func mdadmLevelName(level string) string {
	if level == "" {
		return level
	}
	if level[0] >= '0' && level[0] <= '9' {
		return "raid" + level
	}
	return level
}

func mdadmAssembleFn(c *exec.Context, args *value.Map) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	device := strings.TrimSpace(states.Str(args, "device", ""))
	devices := mdadmDevList(args, "devices")
	uuid := strings.TrimSpace(states.Str(args, "uuid", ""))
	scan := states.Bool(args, "scan", false) || (device == "" && len(devices) == 0)

	if device != "" && mdadmArrayExists(c, device) {
		return mdadmMutateResult(c, false, fmt.Sprintf("%s is already assembled.", device), nil), nil
	}

	argv := []string{"mdadm", "--assemble"}
	if scan {
		argv = append(argv, "--scan")
	}
	if device != "" {
		argv = append(argv, device)
	}
	if uuid != "" {
		argv = append(argv, "--uuid="+uuid)
	}
	argv = append(argv, devices...)

	target := device
	if target == "" {
		target = "every array a scan finds"
	}
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf("%s would be assembled.", target), nil), nil
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("mdadm could not be run: %w", err)
	}
	msg := strings.TrimSpace(res.Stderr + res.Stdout)
	// A scan that assembles nothing new exits 1 with "No arrays found" or
	// "already active" -- neither is a failure of this call.
	if res.Code != 0 && (strings.Contains(msg, "No arrays found") || strings.Contains(msg, "already active") ||
		strings.Contains(msg, "Unable to add") && strings.Contains(msg, "already active")) {
		return mdadmMutateResult(c, false, fmt.Sprintf("%s was already assembled.", target), nil), nil
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`mdadm --assemble` exited %d: %s", res.Code, firstLine(msg))
	}
	return mdadmMutateResult(c, true, fmt.Sprintf("%s was assembled.", target), value.MapOf(target, states.Change(nil, "assembled"))), nil
}

func mdadmStopFn(c *exec.Context, args *value.Map) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	device := strings.TrimSpace(states.Str(args, "device", ""))
	if device == "" {
		return nil, errors.New("an array device must be named")
	}
	if !mdadmArrayExists(c, device) {
		return mdadmMutateResult(c, false, fmt.Sprintf("%s is not a running array.", device), nil), nil
	}
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf("array %s would be stopped.", device),
			value.MapOf(device, states.Change("running", "stopped"))), nil
	}
	if _, err := mdadmRun(c, []string{"mdadm", "--stop", device}); err != nil {
		return nil, err
	}
	return mdadmMutateResult(c, true, fmt.Sprintf("array %s was stopped.", device),
		value.MapOf(device, states.Change("running", "stopped"))), nil
}

func mdadmMember(c *exec.Context, args *value.Map, verb string) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	device := strings.TrimSpace(states.Str(args, "device", ""))
	comp := strings.TrimSpace(states.Str(args, "component", ""))
	if device == "" || comp == "" {
		return nil, errors.New("both the array and the component device must be named")
	}
	a, err := mdadmDetail(c, device)
	if err != nil {
		return nil, err
	}
	member := mdadmFindMember(a, comp)

	switch verb {
	case "add":
		if member != nil {
			return mdadmMutateResult(c, false, fmt.Sprintf("%s is already in %s.", comp, device), nil), nil
		}
	case "remove":
		if member == nil {
			return mdadmMutateResult(c, false, fmt.Sprintf("%s is not in %s.", comp, device), nil), nil
		}
	case "fail":
		if member == nil {
			return nil, fmt.Errorf("%s is not a member of %s", comp, device)
		}
		if strings.Contains(member.State, "faulty") {
			return mdadmMutateResult(c, false, fmt.Sprintf("%s is already failed in %s.", comp, device), nil), nil
		}
	}

	flag := map[string]string{"add": "--add", "fail": "--fail", "remove": "--remove"}[verb]
	past := map[string]string{"add": "added to", "fail": "failed in", "remove": "removed from"}[verb]
	change := value.MapOf(comp, states.Change(mdadmMemberState(member), verb))
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf("%s would be %s %s.", comp, past, device), change), nil
	}
	if _, err := mdadmRun(c, []string{"mdadm", device, flag, comp}); err != nil {
		return nil, err
	}
	return mdadmMutateResult(c, true, fmt.Sprintf("%s was %s %s.", comp, past, device), change), nil
}

func mdadmFindMember(a *mdadmArray, comp string) *mdadmMemberInfo {
	for i := range a.Members {
		if a.Members[i].Device == comp {
			return &a.Members[i]
		}
	}
	return nil
}

func mdadmMemberState(m *mdadmMemberInfo) any {
	if m == nil {
		return nil
	}
	return m.State
}

func mdadmGrowFn(c *exec.Context, args *value.Map) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	device := strings.TrimSpace(states.Str(args, "device", ""))
	if device == "" {
		return nil, errors.New("an array device must be named")
	}
	raidDevices := states.Int(args, "raid_devices", 0)
	size := strings.TrimSpace(states.Str(args, "size", ""))
	argv, err := mdadmGrowArgv(device, raidDevices, size)
	if err != nil {
		return nil, err
	}

	a, err := mdadmDetail(c, device)
	if err != nil {
		return nil, err
	}
	if raidDevices > 0 && raidDevices == a.RaidDevices && size == "" {
		return mdadmMutateResult(c, false, fmt.Sprintf("%s already spans %d devices.", device, raidDevices), nil), nil
	}

	change := value.NewMap(1)
	if raidDevices > 0 {
		change.Set("raid_devices", states.Change(a.RaidDevices, raidDevices))
	}
	if size != "" {
		change.Set("size", states.Change(a.ArraySizeKB, size))
	}
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf("array %s would be grown; a reshape can take hours.", device), change), nil
	}
	out, err := mdadmRun(c, argv)
	if err != nil {
		return nil, err
	}
	res := mdadmMutateResult(c, true, fmt.Sprintf("array %s was grown; the reshape runs in the background.", device), change)
	if out != "" {
		res.Set("output", out)
	}
	return res, nil
}

// mdadmSaveConfigFn writes the running arrays into mdadm.conf, keeping
// any non-ARRAY lines an operator put there.
func mdadmSaveConfigFn(c *exec.Context, args *value.Map) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(states.Str(args, "path", ""))
	if path == "" {
		path = mdadmDefaultConfPath()
		if path == "" {
			return nil, errors.New(
				"this node has no directory for an mdadm.conf: install the `mdadm` package, or pass an explicit `path`")
		}
	}

	res, err := c.Run(exec.Command{Argv: []string{"mdadm", "--detail", "--scan"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("mdadm could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`mdadm --detail --scan` exited %d: %s", res.Code, firstLine(res.Stderr))
	}
	var arrayLines []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ARRAY ") {
			arrayLines = append(arrayLines, strings.TrimRight(line, "\r"))
		}
	}

	existing, _ := os.ReadFile(path)
	var kept []string
	oldArrays := 0
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ARRAY ") {
			oldArrays++
			continue
		}
		kept = append(kept, strings.TrimRight(line, "\r"))
	}
	// Drop trailing blank lines from the kept block so the file does not
	// grow a blank line per run.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	body := strings.Join(kept, "\n")
	if body != "" {
		body += "\n"
	}
	if len(arrayLines) > 0 {
		body += strings.Join(arrayLines, "\n") + "\n"
	}

	if string(existing) == body {
		return mdadmMutateResult(c, false, fmt.Sprintf("%s already lists the running arrays.", path), nil), nil
	}
	change := value.MapOf(path, states.Change(
		fmt.Sprintf("%d ARRAY line(s)", oldArrays),
		fmt.Sprintf("%d ARRAY line(s)", len(arrayLines))))
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf("%s would be rewritten with %d ARRAY line(s).", path, len(arrayLines)), change), nil
	}
	if err := atomicfile.Write(path, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("%s could not be written: %w", path, err)
	}
	return mdadmMutateResult(c, true, fmt.Sprintf("%s now lists %d array(s).", path, len(arrayLines)), change), nil
}

func mdadmDefaultConfPath() string {
	for _, p := range MdadmConfPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, p := range MdadmConfPaths {
		dir := p[:strings.LastIndexByte(p, '/')]
		if _, err := os.Stat(dir); err == nil {
			return p
		}
	}
	return ""
}

func mdadmDevList(args *value.Map, key string) []string {
	var out []string
	for _, d := range states.Strings(args, key) {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
