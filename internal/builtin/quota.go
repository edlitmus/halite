package builtin

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// quotaPlatforms are the platforms with filesystem quotas and the tools
// to drive them.
//
// macOS is absent. It has a quota system, but it is administered through
// `quota` files that Apple has not documented since the tools stopped
// shipping in a default install, and a module that guessed at it would
// be an assumption on a platform nobody here can check.
var quotaPlatforms = []string{"linux", "freebsd", "openbsd", "netbsd", "dragonfly"}

// quotaKinds are the two things a quota can be attached to.
var quotaKinds = map[string]string{"user": "-u", "group": "-g"}

// registerQuota installs the `quota` module of SPEC 15.3's Common Linux
// row.
//
// # Two tools that do the same job with different arguments
//
// Reading is `repquota` on both platforms and setting is not: Linux has
// `setquota`, which takes the limits as five arguments after the name,
// and FreeBSD has no `setquota` at all. Its equivalent is `edquota -e`,
// which takes them colon-packed onto the filesystem argument and puts
// the name last:
//
//	setquota -u alice 1024 2048 100 200 /home       # Linux
//	edquota -u -e /home:1024:2048:100:200 alice     # FreeBSD
//
// So the arguments are in a different order, the separator is different,
// and the tool has a different name. That is three chances to write one
// platform's spelling and have it look correct — the shape plan.md §1.4
// found in `hostname`, where a fixture forced the branch FreeBSD does
// not take and the branch it does take had no test at all. Here the
// argument vector is a table keyed by platform, and
// quota_platform_test.go checks every row of it from any host.
//
// # Reading a table written for a person, and not doing so where there
// # is an alternative
//
// `repquota`'s default output is a fixed-width report with a two
// character flags column that carries no separator, and on Linux the
// grace columns are *blank* when nothing is over its soft limit — so the
// number of whitespace-separated fields on a row varies with the state
// of the filesystem. Splitting that on whitespace is DIVERGENCE 5.31
// waiting to happen.
//
// Linux's quota-tools grew `-O csv` for exactly this reason, so that is
// what this asks for first, and it falls back to the table only when the
// tool does not know the option — logging which path it took, because a
// silent fallback is a defect that hides twice (DIVERGENCE 4.9). The
// FreeBSD table parser is written against the `printf` calls in
// usr.sbin/repquota/repquota.c rather than against remembered output.
//
// # A ZFS filesystem is not an unquota'd filesystem
//
// ZFS does not use this mechanism at all: its quotas are dataset
// properties, `zfs set userquota@alice=10G`. `repquota` on a ZFS
// filesystem reports nothing, and reporting that as "this filesystem has
// no quotas" would be false on a fleet that is entirely ZFS. Both
// reading functions say so by name and point at the `zfs` module
// instead.
func registerQuota(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "quota", Function: "report",
				Doc: "Return every account with a quota on a filesystem, with what it uses and what it may use.",
				Params: []signature.Param{
					req("filesystem", signature.Path, "The mount point, such as /home."),
					choice("kind", "user", "Whether to report user quotas or group quotas.", "user", "group"),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  quotaPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				fs := strings.TrimSpace(states.Str(args, "filesystem", ""))
				kind := states.Str(args, "kind", "user")
				if fs == "" {
					return nil, errors.New("a filesystem must be named")
				}
				if err := quotaCheckKind(kind); err != nil {
					return nil, err
				}
				if why := quotaWrongMechanism(c, fs); why != "" {
					return nil, errors.New(why)
				}
				return quotaReport(c, fs, kind)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "quota", Function: "get_mode",
				Doc: "Report whether user and group quotas are switched on for a filesystem.",
				Params: []signature.Param{
					req("filesystem", signature.Path, "The mount point, such as /home."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  quotaPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				fs := strings.TrimSpace(states.Str(args, "filesystem", ""))
				if fs == "" {
					return nil, errors.New("a filesystem must be named")
				}
				return quotaMode(c, fs)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "quota", Function: "stats",
				Doc: "Report every filesystem this node has quotas switched on for.",
				Params: []signature.Param{
					choice("kind", "user", "Whether to look at user quotas or group quotas.", "user", "group"),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  quotaPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				kind := states.Str(args, "kind", "user")
				if err := quotaCheckKind(kind); err != nil {
					return nil, err
				}
				return quotaStats(c, kind)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "quota", Function: "set",
				Doc: "Set one account's block and inode limits on a filesystem.",
				Params: []signature.Param{
					req("filesystem", signature.Path, "The mount point, such as /home."),
					req("name", signature.String, "The user or group the limits belong to."),
					choice("kind", "user", "Whether the name is a user or a group.", "user", "group"),
					opt("block_soft", signature.Int, int64(0), "Soft block limit in 1 KiB blocks. Zero is no limit."),
					opt("block_hard", signature.Int, int64(0), "Hard block limit in 1 KiB blocks. Zero is no limit."),
					opt("inode_soft", signature.Int, int64(0), "Soft limit on the number of files. Zero is no limit."),
					opt("inode_hard", signature.Int, int64(0), "Hard limit on the number of files. Zero is no limit."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  quotaPlatforms,
				Section:    "15.3",
			},
			Fn: quotaSet,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "quota", Function: "on",
				Doc: "Switch quotas on for a filesystem.",
				Params: []signature.Param{
					req("filesystem", signature.Path, "The mount point, or `-a` for every filesystem the table marks."),
					choice("kind", "user", "Which quotas to switch on.", "user", "group", "both"),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  quotaPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return quotaSwitch(c, args, true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "quota", Function: "off",
				Doc: "Switch quotas off for a filesystem.",
				Params: []signature.Param{
					req("filesystem", signature.Path, "The mount point, or `-a` for every filesystem the table marks."),
					choice("kind", "user", "Which quotas to switch off.", "user", "group", "both"),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  quotaPlatforms,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return quotaSwitch(c, args, false)
			},
		},
	)
}

// ---- the platform table ----
//
// Each of these is a pure function of the platform and its arguments, so
// that every platform's argument vector can be checked from any host.
// This is the fix plan.md §1.4 drew out of two fixtures that had each
// forced a branch their platform does not take: a decision made from
// runtime.GOOS is only checkable on the platform it decides for, and a
// decision made from an argument is checkable everywhere.

// quotaSetArgv is the command that writes a limit.
//
// Linux takes the five values as separate arguments with the filesystem
// last; FreeBSD colon-packs them onto the filesystem and puts the name
// last. Getting either the order or the separator wrong produces a
// command the tool refuses, which is the good case; getting the *order
// of the four limits* wrong produces a command it accepts, and sets an
// inode limit as a block limit on a filesystem somebody is relying on.
func quotaSetArgv(goos, kind, name, fs string, blockSoft, blockHard, inodeSoft, inodeHard int64) ([]string, error) {
	flag, ok := quotaKinds[kind]
	if !ok {
		return nil, fmt.Errorf("a quota belongs to a user or a group, not to a %q", kind)
	}
	switch goos {
	case "linux":
		return []string{
			"setquota", flag, name,
			strconv.FormatInt(blockSoft, 10), strconv.FormatInt(blockHard, 10),
			strconv.FormatInt(inodeSoft, 10), strconv.FormatInt(inodeHard, 10),
			fs,
		}, nil
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		spec := fmt.Sprintf("%s:%d:%d:%d:%d", fs, blockSoft, blockHard, inodeSoft, inodeHard)
		return []string{"edquota", flag, "-e", spec, name}, nil
	}
	return nil, fmt.Errorf("this build does not know how to set a quota on %s", goos)
}

// quotaSwitchArgv is quotaon or quotaoff.
//
// The two tools take the same flags on every platform here, which is why
// this is one function and not a second table. `-a` is passed as the
// filesystem rather than as a flag, because that is how both tools spell
// "every filesystem the table marks" and treating it as a special case
// would mean two spellings of one idea.
func quotaSwitchArgv(on bool, kinds []string, fs string) []string {
	argv := []string{"quotaoff"}
	if on {
		argv = []string{"quotaon"}
	}
	for _, k := range kinds {
		argv = append(argv, quotaKinds[k])
	}
	argv = append(argv, "-v")
	if fs == "-a" {
		return append(argv, "-a")
	}
	return append(argv, fs)
}

// quotaReportArgv asks repquota for one filesystem.
//
// `-n` is never passed, so names come back as names. `-h` is never
// passed either, and that one matters: it humanises the block counts to
// `1.5G`, which a reader expecting kilobytes would take for 1.5. The
// test that asserts the absence of both is not decoration — they are one
// character each and both would parse.
func quotaReportArgv(kind, fs string, csv bool) []string {
	argv := []string{"repquota", quotaKinds[kind], "-v"}
	if csv {
		argv = append(argv, "-O", "csv")
	}
	if fs == "-a" {
		return append(argv, "-a")
	}
	return append(argv, fs)
}

// ---- reading ----

// quotaCheckKind refuses anything but user or group.
func quotaCheckKind(kind string) error {
	if _, ok := quotaKinds[kind]; !ok {
		return fmt.Errorf("a quota belongs to a user or a group, not to a %q", kind)
	}
	return nil
}

// quotaReport runs repquota and parses whichever format came back.
func quotaReport(c *exec.Context, fs, kind string) (any, error) {
	rows, format, err := quotaRun(c, kind, fs)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(rows) + 1)
	entries := value.NewMap(len(rows))
	for _, row := range rows {
		entries.Set(row.Name, quotaRowValue(row))
	}
	out.Set("filesystem", fs)
	out.Set("kind", kind)
	out.Set("format", format)
	out.Set("quotas", entries)
	return out, nil
}

// quotaStats reports every filesystem with quotas on.
func quotaStats(c *exec.Context, kind string) (any, error) {
	rows, format, err := quotaRun(c, kind, "-a")
	if err != nil {
		return nil, err
	}
	byFilesystem := value.NewMap(0)
	for _, row := range rows {
		existing, ok := byFilesystem.GetString(row.Filesystem)
		if !ok {
			existing = value.NewMap(0)
			byFilesystem.Set(row.Filesystem, existing)
		}
		existing.(*value.Map).Set(row.Name, quotaRowValue(row))
	}
	out := value.NewMap(3)
	out.Set("kind", kind)
	out.Set("format", format)
	out.Set("filesystems", byFilesystem)
	return out, nil
}

// quotaMode reports whether quotas are switched on.
//
// It asks `quotaon -p`, which reports state rather than changing it, and
// falls back to reading the mount options where the tool does not have
// that flag. A filesystem whose quotas are off is not an error: it is the
// answer.
func quotaMode(c *exec.Context, fs string) (any, error) {
	out := value.NewMap(4)
	out.Set("filesystem", fs)
	if why := quotaWrongMechanism(c, fs); why != "" {
		out.Set("user", false)
		out.Set("group", false)
		out.Set("comment", why)
		return out, nil
	}
	for _, kind := range []string{"user", "group"} {
		res, err := c.Run(exec.Command{
			Argv:           []string{"quotaon", quotaKinds[kind], "-p", fs},
			IgnoreExitCode: true,
		})
		if err != nil {
			return nil, fmt.Errorf("quotaon could not be run on this node: %w", err)
		}
		// Both platforms print "... is on" or "... is off" for -p. A
		// tool that does not know -p says so on stderr and exits
		// non-zero, and "off" would be the wrong thing to report about a
		// question that was never asked.
		text := strings.ToLower(res.Stdout)
		switch {
		case strings.Contains(text, " is on"):
			out.Set(kind, true)
		case strings.Contains(text, " is off"):
			out.Set(kind, false)
		default:
			out := strings.TrimSpace(res.Stdout + res.Stderr)
			return nil, fmt.Errorf(
				"`quotaon %s -p %s` did not report a state; it printed %q and exited %d%s",
				quotaKinds[kind], fs, out, res.Code, quotaKernelSupportNote(out))
		}
	}
	return out, nil
}

// quotaKernelSupportNote explains the two answers from the quota tools
// that name a cause an operator cannot act on, or returns "".
//
// Both were found by running this on a real machine rather than
// reasoned about (DIVERGENCE 5.48), and both are worth translating
// because the tool's own wording sends the reader nowhere.
//
//   - "Quota format not supported in kernel" means the kernel has no
//     `quota_v2` format driver, so the classic `aquota.user` files
//     cannot be used at all. GitHub's Ubuntu runners are like this.
//   - ESRCH, which the tools render as "No such process", is the same
//     thing arriving as an errno rather than as a sentence.
//
// The way in on such a kernel is a filesystem built with ext4's own
// quota feature, where quotas are on from the moment it is mounted:
// `quota.report` and `quota.set` work there, and `quota.on` and
// `quota.off` have nothing to switch. So a failure here is not a
// filesystem without quotas, which is what the bare message reads as.
func quotaKernelSupportNote(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "format not supported"),
		strings.Contains(lower, "no such process"):
		return " -- this kernel has no classic quota format driver (`quota_v2`), so the aquota files " +
			"cannot be switched on. A filesystem built with ext4's own quota feature needs no " +
			"switching on: `quota.report` and `quota.set` work there, and `quota.on` and `quota.off` " +
			"have nothing to do"
	}
	return ""
}

// quotaRow is one account's quota on one filesystem.
type quotaRow struct {
	Name       string
	Filesystem string
	BlocksUsed int64
	BlockSoft  int64
	BlockHard  int64
	BlockGrace string
	FilesUsed  int64
	InodeSoft  int64
	InodeHard  int64
	InodeGrace string
	// OverBlocks and OverFiles are repquota's two flag characters, which
	// say that the soft limit is already exceeded and the grace period is
	// running. They are the reason to read this report at all, so they
	// are fields rather than something a caller recomputes.
	OverBlocks bool
	OverFiles  bool
}

func quotaRowValue(r quotaRow) *value.Map {
	m := value.NewMap(11)
	m.Set("filesystem", r.Filesystem)
	m.Set("blocks_used", r.BlocksUsed)
	m.Set("block_soft", r.BlockSoft)
	m.Set("block_hard", r.BlockHard)
	m.Set("block_grace", r.BlockGrace)
	m.Set("files_used", r.FilesUsed)
	m.Set("inode_soft", r.InodeSoft)
	m.Set("inode_hard", r.InodeHard)
	m.Set("inode_grace", r.InodeGrace)
	m.Set("over_blocks", r.OverBlocks)
	m.Set("over_files", r.OverFiles)
	return m
}

// quotaReadPlan says which of repquota's two formats a platform can be
// read from, and why.
//
// **The fixed-width report cannot be parsed on Linux, and this is the
// reason.** quota-tools leaves a grace column *blank* when nothing is
// over its soft limit, so a row carries between six and eight
// whitespace-separated numbers depending on the state of the
// filesystem — and a grace is not always distinguishable from a number,
// because a grace period of under an hour prints as a bare count of
// minutes. Seven fields after the flags is therefore genuinely
// ambiguous: either the block grace is running and the file grace is
// not, or the other way round, and nothing in the row says which.
//
// Reading it anyway would attribute one account's inode count to
// another's block limit. So Linux is read through `-O csv`, which
// quota-tools grew for this, and a node whose tools are too old to have
// it is told so rather than given a report assembled from a guess.
//
// The BSDs have no `-O` and need none: repquota.c prints `-` in a grace
// column that is not running, so a row always has ten fields.
func quotaReadPlan(goos string) (csv bool, why string) {
	switch goos {
	case "linux":
		return true, "quota-tools leaves a blank grace column, so its fixed-width report is ambiguous"
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return false, "repquota prints `-` for a grace that is not running, so its report is unambiguous"
	}
	return false, ""
}

// quotaRun asks repquota for the format this platform can be read from.
//
// Which one was used is reported to the caller and logged, because a
// reader that silently changed format would be a defect that hides
// twice: once when it is taken for a reason nobody sees, and again when
// the reason changes.
func quotaRun(c *exec.Context, kind, fs string) ([]quotaRow, string, error) {
	if c.Which("repquota") == "" {
		return nil, "", errors.New(
			"this node has no `repquota`; filesystem quotas are administered by the quota tools, " +
				"which are a separate package on most distributions")
	}
	csv, why := quotaReadPlan(runtime.GOOS)
	if why == "" {
		return nil, "", fmt.Errorf("this build does not know how to read a quota report on %s", runtime.GOOS)
	}

	if !csv {
		res, err := c.Run(exec.Command{Argv: quotaReportArgv(kind, fs, false)})
		if err != nil {
			return nil, "", fmt.Errorf("repquota could not be run on this node: %w", err)
		}
		rows, err := quotaParseTable(res.Stdout, fs)
		return rows, "table", err
	}

	res, err := c.Run(exec.Command{Argv: quotaReportArgv(kind, fs, true), IgnoreExitCode: true})
	if err != nil {
		return nil, "", fmt.Errorf("repquota could not be run on this node: %w", err)
	}
	if res.Code != 0 || !quotaLooksLikeCSV(res.Stdout) {
		return nil, "", fmt.Errorf(
			"`repquota -O csv` was refused on this node (exit %d: %s). "+
				"On Linux this build reads only that format, because %s -- "+
				"quota-tools 4.00 and later have it",
			res.Code, strings.TrimSpace(firstLine(res.Stderr+res.Stdout)), why)
	}
	rows, err := quotaParseCSV(res.Stdout, fs)
	return rows, "csv", err
}

// quotaLooksLikeCSV recognises quota-tools' own header.
//
// The header is checked rather than the exit code alone, because a tool
// that does not know `-O` may print its usage and exit zero, and a usage
// message parsed as a table is a report of quotas that do not exist.
func quotaLooksLikeCSV(out string) bool {
	first, _, _ := strings.Cut(out, "\n")
	return strings.HasPrefix(strings.TrimSpace(first), "User,") ||
		strings.HasPrefix(strings.TrimSpace(first), "Group,")
}

// quotaParseCSV reads quota-tools' machine-readable format.
//
// The columns are, in order: name, block status, file status, block
// used, block soft, block hard, block grace, file used, file soft, file
// hard, file grace. The grace columns are empty when nothing is over its
// limit, which is precisely why this format exists: in the fixed-width
// report they are empty *and* whitespace-separated, so the field count
// changes with the state of the filesystem.
func quotaParseCSV(out, fs string) ([]quotaRow, error) {
	var rows []quotaRow
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || i == 0 {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 11 {
			return nil, fmt.Errorf("repquota's CSV row %q has %d fields, and the format has 11", line, len(f))
		}
		rows = append(rows, quotaRow{
			Name:       f[0],
			Filesystem: fs,
			OverBlocks: !strings.EqualFold(f[1], "ok"),
			OverFiles:  !strings.EqualFold(f[2], "ok"),
			BlocksUsed: quotaNumber(f[3]),
			BlockSoft:  quotaNumber(f[4]),
			BlockHard:  quotaNumber(f[5]),
			BlockGrace: strings.TrimSpace(f[6]),
			FilesUsed:  quotaNumber(f[7]),
			InodeSoft:  quotaNumber(f[8]),
			InodeHard:  quotaNumber(f[9]),
			InodeGrace: strings.TrimSpace(f[10]),
		})
	}
	return rows, nil
}

// quotaParseTable reads the fixed-width report.
//
// Written against the printf calls in FreeBSD's usr.sbin/repquota
// repquota.c rather than against remembered output. Three things there
// decide how this is written:
//
//   - The two flag characters are printed with `%c%c` and no separator,
//     so they are one whitespace-separated field, `--`, `+-`, `-+` or
//     `++`. They are not part of the name and they are not a number.
//   - Every numeric column is `%6ju` or `%7ju` and a grace is `%6s`
//     holding `-` when nothing is over its limit, so a FreeBSD row has
//     ten fields whatever the state of the filesystem.
//   - The `*** Report for ...` line appears only with `-v`, and names
//     the mount point, so it is where a `-a` run learns which filesystem
//     the rows below it belong to.
//
// A row this cannot make sense of is an error rather than a skip. Unlike
// the PAM reader, there is no partial answer worth having here: a quota
// report missing the one account that is over its limit is worse than no
// report, because it will be believed.
func quotaParseTable(out, fs string) ([]quotaRow, error) {
	var (
		rows    []quotaRow
		current = fs
	)
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "***"):
			if mount, ok := quotaReportHeaderMount(trimmed); ok {
				current = mount
			}
			continue
		case strings.HasPrefix(trimmed, "User") || strings.HasPrefix(trimmed, "Group") ||
			strings.HasPrefix(trimmed, "Block limits") || strings.Contains(trimmed, "Block  limits") ||
			strings.HasPrefix(trimmed, "Block grace time") || strings.HasPrefix(trimmed, "---"):
			continue
		}
		row, err := quotaParseTableRow(trimmed, current)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// quotaReportHeaderMount pulls the mount point out of repquota's own
// banner.
//
// FreeBSD prints `*** Report for user quotas on /home (/dev/da0s1e)` and
// Linux prints `*** Report for user quotas on device /dev/sda1` — the
// mount point on one and the device on the other. Both are taken as
// written: naming the device is what Linux knows, and turning it into a
// mount point here would be this module inventing a fact.
func quotaReportHeaderMount(line string) (string, bool) {
	const marker = " quotas on "
	i := strings.Index(line, marker)
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSpace(line[i+len(marker):])
	rest = strings.TrimPrefix(rest, "device ")
	if open := strings.IndexByte(rest, '('); open >= 0 {
		rest = strings.TrimSpace(rest[:open])
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// quotaParseTableRow reads one account's row.
//
// A BSD row is exactly ten fields — name, the two flag characters, three
// block columns, a block grace, three file columns and a file grace —
// whatever the state of the filesystem, because repquota.c prints `-`
// rather than nothing for a grace that is not running. Anything else is
// a row this reader has misaligned, and it is refused: a quota report
// missing the one account that is over its limit is worse than no
// report, because it will be believed.
func quotaParseTableRow(line, fs string) (quotaRow, error) {
	f := strings.Fields(line)
	if len(f) != 10 {
		return quotaRow{}, fmt.Errorf(
			"repquota's row %q has %d fields; a report row has 10 -- "+
				"a name, the two status characters, three block columns and a grace, "+
				"and three file columns and a grace", line, len(f))
	}
	if !quotaIsFlags(f[1]) {
		return quotaRow{}, fmt.Errorf(
			"repquota's row %q has %q where its two status characters belong", line, f[1])
	}
	row := quotaRow{
		Name:       f[0],
		Filesystem: fs,
		OverBlocks: f[1][0] == '+',
		OverFiles:  f[1][1] == '+',
		BlocksUsed: quotaNumber(f[2]),
		BlockSoft:  quotaNumber(f[3]),
		BlockHard:  quotaNumber(f[4]),
		BlockGrace: quotaGrace(f[5]),
		FilesUsed:  quotaNumber(f[6]),
		InodeSoft:  quotaNumber(f[7]),
		InodeHard:  quotaNumber(f[8]),
		InodeGrace: quotaGrace(f[9]),
	}

	// A block column that did not parse as a number is a report this
	// build asked for in kilobytes and got in gigabytes. `repquota -h`
	// prints `1.5G`, which reads as 1, so an account using a gigabyte and
	// a half would be reported as using one block -- believable, wrong,
	// and invisible. This build never passes -h; a report that carries
	// one anyway is refused rather than read.
	for i, field := range f[2:5] {
		if _, err := strconv.ParseInt(field, 10, 64); err != nil {
			return quotaRow{}, fmt.Errorf(
				"repquota's row %q has %q in block column %d, which is not a count of kilobytes; "+
					"this build does not pass -h and cannot read a humanised report", line, field, i+1)
		}
	}
	return row, nil
}

// quotaIsFlags recognises repquota's two-character status column.
func quotaIsFlags(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, r := range s {
		if r != '+' && r != '-' {
			return false
		}
	}
	return true
}

// quotaGrace normalises repquota's "nothing is over its limit" spelling.
//
// FreeBSD writes `-` and Linux writes nothing at all, and both mean the
// same thing. An empty string is the one this module returns, so a caller
// asking "is a grace period running" does not have to know which
// platform answered.
func quotaGrace(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// quotaNumber reads a count, returning zero for anything that is not
// one. The caller checks the columns where a non-number means the row
// was misread.
func quotaNumber(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// quotaWrongMechanism reports that a filesystem has quotas under a
// system these tools cannot see, or an empty string.
//
// This is the difference between "there are no quotas here" and "the
// quotas here are somewhere else", and on a ZFS estate it is the whole
// of the answer. `repquota` on a ZFS dataset reports nothing at all, and
// a module that passed that on as "no quotas" would be describing a
// filesystem that may well have a quota on every account.
func quotaWrongMechanism(c *exec.Context, fs string) string {
	if quotaFilesystemType(c, fs) != "zfs" {
		return ""
	}
	return fmt.Sprintf(
		"%s is ZFS, whose quotas are dataset properties rather than quota files, so the quota tools "+
			"cannot see them: ask `zfs.get` for `userquota@<name>`, `groupquota@<name>` or `quota`", fs)
}

// quotaFilesystemType asks the mount table what a path is.
//
// It goes through this build's own `mount` module rather than running
// `mount(8)` again, so that a node with /proc/self/mounts and a node
// without give the same answer.
func quotaFilesystemType(c *exec.Context, fs string) string {
	entry, _, err := activeMountAt(c, fs)
	if err != nil || entry == nil {
		return ""
	}
	return states.Str(entry, "fstype", "")
}

// ---- writing ----

// quotaSet writes one account's limits.
func quotaSet(c *exec.Context, args *value.Map) (any, error) {
	fs := strings.TrimSpace(states.Str(args, "filesystem", ""))
	name := strings.TrimSpace(states.Str(args, "name", ""))
	kind := states.Str(args, "kind", "user")
	if fs == "" {
		return nil, errors.New("a filesystem must be named")
	}
	if name == "" {
		return nil, errors.New("a user or group must be named")
	}
	if err := quotaCheckKind(kind); err != nil {
		return nil, err
	}
	if why := quotaWrongMechanism(c, fs); why != "" {
		return nil, errors.New(why)
	}

	blockSoft := states.Int(args, "block_soft", 0)
	blockHard := states.Int(args, "block_hard", 0)
	inodeSoft := states.Int(args, "inode_soft", 0)
	inodeHard := states.Int(args, "inode_hard", 0)
	for label, n := range map[string]int64{
		"block_soft": blockSoft, "block_hard": blockHard,
		"inode_soft": inodeSoft, "inode_hard": inodeHard,
	} {
		if n < 0 {
			return nil, fmt.Errorf("%s is %d; a limit is zero, meaning unlimited, or a positive count", label, n)
		}
	}
	// A soft limit above its hard limit is accepted by both tools and
	// means the soft limit never fires, so the account gets no warning
	// and no grace period — it simply stops writing one day. Refusing it
	// is the one place this module is stricter than the tool it drives.
	if blockHard > 0 && blockSoft > blockHard {
		return nil, fmt.Errorf(
			"block_soft is %d and block_hard is %d: a soft limit above its hard limit never fires, "+
				"so the account gets no warning before it is stopped", blockSoft, blockHard)
	}
	if inodeHard > 0 && inodeSoft > inodeHard {
		return nil, fmt.Errorf(
			"inode_soft is %d and inode_hard is %d: a soft limit above its hard limit never fires, "+
				"so the account gets no warning before it is stopped", inodeSoft, inodeHard)
	}

	argv, err := quotaSetArgv(runtime.GOOS, kind, name, fs, blockSoft, blockHard, inodeSoft, inodeHard)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`, which is how %s sets a quota", argv[0], runtime.GOOS)
	}

	current := quotaCurrent(c, fs, kind, name)
	want := quotaRow{
		Name: name, Filesystem: fs,
		BlockSoft: blockSoft, BlockHard: blockHard,
		InodeSoft: inodeSoft, InodeHard: inodeHard,
	}
	if current != nil && quotaSameLimits(*current, want) {
		out := value.NewMap(3)
		out.Set("changed", false)
		out.Set("comment", fmt.Sprintf("%s already has these limits on %s.", name, fs))
		out.Set("limits", quotaLimitsValue(want))
		return out, nil
	}

	change := value.NewMap(2)
	if current != nil {
		change.Set("old", quotaLimitsValue(*current))
	} else {
		change.Set("old", nil)
	}
	change.Set("new", quotaLimitsValue(want))

	if c.Test {
		out := value.NewMap(4)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf(
			"%s's limits on %s would be set. Nothing was written: this was a test run.", name, fs))
		out.Set("command", exec.Command{Argv: argv}.String())
		out.Set("limits", change)
		return out, nil
	}
	if _, err := c.Run(exec.Command{Argv: argv}); err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}
	out := value.NewMap(3)
	out.Set("changed", true)
	out.Set("comment", fmt.Sprintf("%s's limits on %s were set.", name, fs))
	out.Set("limits", change)
	return out, nil
}

// quotaCurrent reads one account's existing limits, or nil if they
// cannot be read.
//
// A failure here is not fatal. The limits are read so that a set which
// changes nothing can say so, and a node whose repquota cannot be run —
// no quota tools, quotas switched off, an account that is not root — is
// still one where setting a quota is a reasonable thing to ask for. What
// it costs is the "already set" answer, and the comment says the change
// was made rather than claiming it was needed.
func quotaCurrent(c *exec.Context, fs, kind, name string) *quotaRow {
	rows, _, err := quotaRun(c, kind, fs)
	if err != nil {
		c.Logf("debug", "the current quotas on %s could not be read, so this set cannot report whether it changed anything: %v", fs, err)
		return nil
	}
	for _, row := range rows {
		if row.Name == name {
			return &row
		}
	}
	return nil
}

func quotaSameLimits(a, b quotaRow) bool {
	return a.BlockSoft == b.BlockSoft && a.BlockHard == b.BlockHard &&
		a.InodeSoft == b.InodeSoft && a.InodeHard == b.InodeHard
}

func quotaLimitsValue(r quotaRow) *value.Map {
	m := value.NewMap(4)
	m.Set("block_soft", r.BlockSoft)
	m.Set("block_hard", r.BlockHard)
	m.Set("inode_soft", r.InodeSoft)
	m.Set("inode_hard", r.InodeHard)
	return m
}

// quotaSwitch runs quotaon or quotaoff.
func quotaSwitch(c *exec.Context, args *value.Map, on bool) (any, error) {
	fs := strings.TrimSpace(states.Str(args, "filesystem", ""))
	kind := states.Str(args, "kind", "user")
	if fs == "" {
		return nil, errors.New("a filesystem must be named")
	}
	var kinds []string
	switch kind {
	case "both":
		kinds = []string{"user", "group"}
	case "user", "group":
		kinds = []string{kind}
	default:
		return nil, fmt.Errorf("a quota belongs to a user or a group, not to a %q", kind)
	}
	if fs != "-a" {
		if why := quotaWrongMechanism(c, fs); why != "" {
			return nil, errors.New(why)
		}
	}

	argv := quotaSwitchArgv(on, kinds, fs)
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}

	verb := "off"
	if on {
		verb = "on"
	}
	if c.Test {
		out := value.NewMap(3)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf(
			"quotas on %s would be switched %s. Nothing was changed: this was a test run.", fs, verb))
		out.Set("command", exec.Command{Argv: argv}.String())
		return out, nil
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		out := strings.TrimSpace(res.Stderr + res.Stdout)
		return nil, fmt.Errorf("`%s` exited %d: %s%s",
			exec.Command{Argv: argv}.String(), res.Code, out, quotaKernelSupportNote(out))
	}
	// `quotaon -v` prints a line per filesystem it acted on and nothing
	// at all for one that was already in the state asked for, which is
	// what makes this idempotent enough to sit in a tree.
	changed := strings.TrimSpace(res.Stdout) != ""
	out := value.NewMap(3)
	out.Set("changed", changed)
	if changed {
		out.Set("comment", fmt.Sprintf("quotas on %s were switched %s.", fs, verb))
	} else {
		out.Set("comment", fmt.Sprintf("quotas on %s were already %s.", fs, verb))
	}
	out.Set("output", strings.TrimSpace(res.Stdout))
	return out, nil
}
