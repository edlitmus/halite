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
// `mdadm --create` overwrites whatever is on the member devices. Without
// `force` this module refuses:
//
//   - an array device that is already an assembled array (that one is a
//     no-change, not an error: the array is there);
//   - a member or spare whose `mdadm --examine` finds an md superblock;
//   - a member or spare on which `wipefs` lists *any* signature -- a
//     filesystem, a partition table, an LVM or ZFS member, swap, LUKS,
//     another RAID member -- see mdadmProbeSignatures;
//   - a member or spare that could not be probed at all. Either check
//     failing for any reason other than a clean "nothing here" is a
//     refusal, not a pass: an unread disk is not an empty one.
//
// `--run` is still passed, and that is only sound *because* of the probe
// above. mdadm's own Create looks for a filesystem or partition table on
// each member and asks "Continue creating array?"; `--run` suppresses
// that question and answers it yes. Without `--run` the question would
// be read from this process's stdin, which is /dev/null, so it would be
// answered by EOF -- and mdadm asks the same question for things that are
// not a danger at all (the RAID1 "metadata at the start and may not be
// suitable as a boot device" note), so dropping `--run` would trade a
// silent overwrite for a create that cannot run unattended. So the
// question mdadm would have asked is asked here first, by a probe whose
// answer this module reads, and `--run` only reaches mdadm once it has
// said "nothing here" for every device, or the caller passed `force`.
// DIVERGENCE 5.290 is how the old wording here claimed a filesystem check
// that did not exist.
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
				Doc: "Create a new array. Without force, refuses a member or spare that carries an md superblock or any " +
					"signature `wipefs` lists (a filesystem, a partition table, an LVM or ZFS member), or that could not be probed.",
				Params: []signature.Param{
					req("device", signature.Path, "The array device to create, such as /dev/md0."),
					req("level", signature.String, "The RAID level: 0, 1, 4, 5, 6, 10, or a name like raid1."),
					req("devices", signature.List, "The member block devices."),
					opt("spares", signature.List, nil, "Extra devices to add as spares."),
					opt("metadata", signature.String, "", "Superblock version, such as 1.2 or 0.90. Empty leaves mdadm's default."),
					opt("name", signature.String, "", "A name for the array, stored in the superblock."),
					opt("chunk", signature.String, "", "Chunk size, such as 512K. For striped levels."),
					opt("assume_clean", signature.Bool, false, "Skip the initial resync. Only safe on devices known to be zeroed or when the data is disposable."),
					opt("force", signature.Bool, false, "Create even over a member whose `mdadm --examine` finds an md superblock, "+
						"on which `wipefs` lists a signature, or which could not be probed. Whatever was found is named in the comment."),
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
				Module: "mdadm", Function: "destroy",
				Doc: "Stop an array and zero its members' superblocks, so they are no longer part of any array. " +
					"**This destroys the array and everything on it.**",
				Params: []signature.Param{
					req("device", signature.Path, "The array to destroy."),
					opt("path", signature.Path, "",
						"The mdadm.conf to drop the array's ARRAY line from. Empty finds this distribution's."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: mdadmDestroyFn,
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

// mdadmDestroyFn stops an array and zeroes its members' superblocks.
//
// # This is the one function here that destroys data on purpose
//
// `create` refuses a member that already carries a superblock unless
// `force` is set, because there the damage is *incidental*: somebody
// asked for a new array and would not expect an old one to be eaten.
// Here it is the whole request. The verb is the confirmation, which is
// also how Salt's `raid.destroy` treats it, and adding a `force` to a
// function whose only purpose is destruction would be ceremony rather
// than a safeguard.
//
// # The order matters, and so does the refusal
//
// The members are read *before* the array is stopped, because once it is
// stopped `--detail` can no longer say what belonged to it. The
// superblocks are zeroed only if the stop actually succeeded -- Salt has
// the same guard, and it is the important one: zeroing the members of a
// still-running array is how a mounted filesystem is destroyed under
// itself. A stop that fails is reported as the error it is, with nothing
// zeroed.
//
// # Idempotence
//
// Destroying an array that is not there is not an error. A second run
// finds no array, finds no ARRAY line left to drop, and reports no
// change -- which is what lets this be called from a tree that wants an
// array gone. An array that is already stopped but still has a line in
// mdadm.conf is a real change: the line goes.
func mdadmDestroyFn(c *exec.Context, args *value.Map) (any, error) {
	if err := mdadmToolPresent(c); err != nil {
		return nil, err
	}
	device := strings.TrimSpace(states.Str(args, "device", ""))
	if device == "" {
		return nil, errors.New("an array device must be named")
	}
	confPath := strings.TrimSpace(states.Str(args, "path", ""))
	if confPath == "" {
		confPath = mdadmDefaultConfPath()
	}

	// Read first: after `--stop` there is nothing left to ask.
	detail, detailErr := mdadmDetail(c, device)

	if detailErr != nil {
		// No array. The only thing that can still be stale is the config.
		dropped, err := mdadmDropConfEntry(confPath, device, "", c.Test)
		if err != nil {
			return nil, err
		}
		if dropped {
			return mdadmMutateResult(c, true,
				fmt.Sprintf("%s is not a running array; its ARRAY line was dropped from %s.", device, confPath),
				value.MapOf(confPath, states.Change("1 ARRAY line", "0 ARRAY lines"))), nil
		}
		return mdadmMutateResult(c, false,
			fmt.Sprintf("%s is not a running array and no ARRAY line names it.", device), nil), nil
	}

	members := make([]string, 0, len(detail.Members))
	for _, m := range detail.Members {
		// A removed or failed slot has no device path, and there is
		// nothing to zero for it.
		if m.Device != "" {
			members = append(members, m.Device)
		}
	}

	change := value.MapOf(device, states.Change("array of "+strings.Join(members, ", "), "destroyed"))
	if c.Test {
		return mdadmMutateResult(c, true,
			fmt.Sprintf("Array %s would be stopped and the superblocks on %d member(s) zeroed.",
				device, len(members)), change), nil
	}

	if _, err := mdadmRun(c, []string{"mdadm", "--stop", device}); err != nil {
		return nil, fmt.Errorf("%s was not stopped, so nothing was zeroed: %w", device, err)
	}
	if len(members) > 0 {
		if _, err := mdadmRun(c, append([]string{"mdadm", "--zero-superblock"}, members...)); err != nil {
			return nil, fmt.Errorf("%s was stopped but its superblocks were not zeroed, so its members "+
				"still look like array members: %w", device, err)
		}
	}
	if _, err := mdadmDropConfEntry(confPath, device, detail.UUID, false); err != nil {
		return nil, err
	}
	return mdadmMutateResult(c, true,
		fmt.Sprintf("Array %s was destroyed and the superblocks on %d member(s) zeroed.",
			device, len(members)), change), nil
}

// mdadmDropConfEntry removes the ARRAY line naming this array from
// mdadm.conf, and reports whether there was one.
//
// Matched on the device path *or* the UUID, where Salt matches on the
// device path alone (`ARRAY {device} .*`). The extra clause is not
// decoration: `save_config` writes whatever `mdadm --detail --scan`
// printed, and that names an array by whichever path it resolved --
// frequently `/dev/md/<name>` rather than the `/dev/mdN` an operator
// passed in. Matching only the caller's spelling would leave the line
// behind on exactly the arrays that have a name. The UUID is the thing
// that does not change.
//
// An empty path, or a file that is not there, is not an error: a node
// that has never had an mdadm.conf has no stale line in it.
func mdadmDropConfEntry(path, device, uuid string, test bool) (bool, error) {
	if path == "" {
		return false, nil
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("%s could not be read: %w", path, err)
	}
	lines := strings.Split(string(existing), "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		if mdadmConfLineNames(line, device, uuid) {
			dropped = true
			continue
		}
		kept = append(kept, strings.TrimRight(line, "\r"))
	}
	if !dropped || test {
		return dropped, nil
	}
	body := strings.Join(kept, "\n")
	if err := atomicfile.Write(path, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("%s could not be written: %w", path, err)
	}
	return true, nil
}

// mdadmConfLineNames reports whether one mdadm.conf line is the ARRAY
// entry for this array.
func mdadmConfLineNames(line, device, uuid string) bool {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || fields[0] != "ARRAY" {
		return false
	}
	if fields[1] == device {
		return true
	}
	if uuid == "" {
		return false
	}
	for _, f := range fields[2:] {
		if strings.EqualFold(f, "UUID="+uuid) {
			return true
		}
	}
	return false
}

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
	// Every member and spare is probed before anything is written. Without
	// force, anything found -- or any probe that could not give a clean
	// answer -- is a refusal. With force the same probes run, so that the
	// comment can say what was overwritten rather than leaving the
	// operator to find out from the data that is no longer there.
	var overridden []string
	for _, dev := range append(append([]string{}, opts.Devices...), opts.Spares...) {
		found, err := mdadmMemberInUse(c, dev)
		if err != nil {
			if !opts.Force {
				return nil, fmt.Errorf("%s could not be checked for existing data, so nothing was created: %w. "+
					"Pass force to create over it unchecked", dev, err)
			}
			overridden = append(overridden, fmt.Sprintf("%s was not checked (%v)", dev, err))
			continue
		}
		if found == "" {
			continue
		}
		if !opts.Force {
			return nil, fmt.Errorf("%s already %s, so nothing was created. Pass force to overwrite it", dev, found)
		}
		overridden = append(overridden, dev+" "+found)
	}
	forceNote := ""
	if len(overridden) > 0 {
		forceNote = " force overrode: " + strings.Join(overridden, "; ") + "."
	}

	levelName := mdadmLevelName(opts.Level)
	change := value.MapOf(opts.Device, states.Change(nil, value.MapOf(
		"level", levelName, "devices", opts.Devices)))
	if c.Test {
		return mdadmMutateResult(c, true, fmt.Sprintf(
			"Array %s would be created as %s across %s.%s", opts.Device, levelName, strings.Join(opts.Devices, ", "),
			forceNote), change), nil
	}
	if _, err := mdadmRun(c, argv); err != nil {
		return nil, err
	}
	return mdadmMutateResult(c, true, fmt.Sprintf(
		"Array %s was created as %s across %s.%s", opts.Device, levelName, strings.Join(opts.Devices, ", "),
		forceNote), change), nil
}

// mdadmMemberInUse reports what, if anything, already lives on a
// prospective member: "" for nothing, a phrase such as "carries an md
// superblock" or "holds signatures: ext4 0x438" otherwise, and an error
// when it could not be told.
//
// The md superblock is asked of mdadm first, because that refusal has
// the most useful thing to say (it may be another array's member). It is
// not enough on its own: `--examine` knows md superblocks and nothing
// else, so a disk holding ext4, XFS, an LVM PV, a ZFS vdev or just a
// partition table answers "No md superblock detected" and, before 5.290,
// went straight on to a `--create --run` that wrote over it.
func mdadmMemberInUse(c *exec.Context, dev string) (string, error) {
	ex, err := mdadmExamineFn(c, value.MapOf("device", dev))
	if err != nil {
		return "", err
	}
	if has, _ := ex.(*value.Map).GetString("has_superblock"); has == true {
		return "carries an md superblock; it may belong to another array", nil
	}
	sigs, err := mdadmProbeSignatures(c, dev)
	if err != nil {
		return "", err
	}
	if len(sigs) > 0 {
		return "holds signatures (" + strings.Join(sigs, ", ") + ")", nil
	}
	return "", nil
}

// mdadmProbeSignatures lists the on-disk signatures `wipefs` finds on a
// device, one string per signature.
//
// # Why wipefs, and not blkid
//
// Both are util-linux and both use libblkid. `wipefs` is used because the
// contract its manual describes separates the two answers this needs
// separated: exit 0 with nothing listed for a device with no signatures,
// non-zero when the device could not be opened or probed. `blkid -p`
// documents exit 2 for "nothing identified", and whether a device it
// could not open also lands on 2 was not established -- if it does,
// "empty" and "unreadable" would differ only in stderr text. Neither
// contract has yet been watched on a real machine for this (5.290). `wipefs`
// without `-a`/`-o` only lists; `--no-act` is passed as well so that no
// later edit adding an erase option can make this probe write.
//
// # How the output is read: by whether there is any
//
// `--noheadings --output TYPE,OFFSET` prints one line per signature. The
// only decision made from it is "empty or not", so a column layout that
// differs between util-linux versions cannot turn a found signature into
// a clean answer: every non-blank line is reported as a signature, and a
// line this does not understand refuses the create rather than passing
// it. The lines are only quoted back to the operator, never parsed for
// meaning.
//
// No output of a real wipefs is held as a fixture yet: the tests use
// output written to this shape and say so, and DIVERGENCE 5.290 lists
// what a lab run has to capture.
func mdadmProbeSignatures(c *exec.Context, dev string) ([]string, error) {
	if c.Which("wipefs") == "" {
		return nil, errors.New("this node has no `wipefs` (util-linux) to look for an existing filesystem or " +
			"partition table")
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"wipefs", "--no-act", "--noheadings", "--output", "TYPE,OFFSET", dev},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("wipefs could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`wipefs %s` exited %d: %s", dev, res.Code, firstLine(strings.TrimSpace(res.Stderr+res.Stdout)))
	}
	var sigs []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if f := strings.Join(strings.Fields(line), " "); f != "" {
			sigs = append(sigs, f)
		}
	}
	return sigs, nil
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
		return mdadmMutateResult(c, true, fmt.Sprintf("Array %s would be stopped.", device),
			value.MapOf(device, states.Change("running", "stopped"))), nil
	}
	if _, err := mdadmRun(c, []string{"mdadm", "--stop", device}); err != nil {
		return nil, err
	}
	return mdadmMutateResult(c, true, fmt.Sprintf("Array %s was stopped.", device),
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
		return mdadmMutateResult(c, true, fmt.Sprintf("Array %s would be grown; a reshape can take hours.", device), change), nil
	}
	out, err := mdadmRun(c, argv)
	if err != nil {
		return nil, err
	}
	res := mdadmMutateResult(c, true, fmt.Sprintf("Array %s was grown; the reshape runs in the background.", device), change)
	if out != "" {
		res.Set("output", out)
	}
	return res, nil
}

// mdadmSaveConfigFn writes the running arrays into mdadm.conf, keeping
// any non-ARRAY lines an operator put there.
//
// # The line is a snapshot, and that includes the parts that move
//
// What gets written is whatever `mdadm --detail --scan` printed, which
// is what Salt's `raid.save_config` writes too. Some of that is not
// stable: `spares=` is a live count, so an array that is still building
// reports one value and the same array a few seconds later reports
// another. Two calls really did produce
//
//	ARRAY /dev/md/halNNN metadata=1.2 spares=2 UUID=...
//	ARRAY /dev/md/halNNN metadata=1.2 spares=1 UUID=...
//
// on a real AlmaLinux node, so calling this twice during a resync
// rewrites the file and reports a change both times.
//
// **That is left as it is, deliberately.** Filtering fields out of what
// mdadm printed would make this build's mdadm.conf disagree with the
// tool that reads it, for the sake of a difference that only exists
// while an array is rebuilding. Salt does filter -- but only `name=` and
// `metadata=`, only on Ubuntu, and to work around a device-naming bug
// rather than for convergence.
//
// What keeps it from being a problem is where it is *called from*.
// There is no `mdadm` state (see the package comment), so nothing in a
// convergence loop reaches this: it is an operator's command, or one a
// tree calls deliberately. Salt arrives at the same place from the other
// direction -- its `raid.present` calls `save_config` only inside
// `if not present`, so a converged node never calls it either. A tree
// that does call this on every highstate will see a change reported for
// as long as a resync is running, and the honest answer is to call it
// after the array settles, which is what the live test does with
// `mdadm --wait`.
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
