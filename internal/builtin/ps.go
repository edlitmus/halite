package builtin

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerPS installs the `ps` module of SPEC 15.2.
//
// # Why this shells out
//
// The ledger recorded this module as not implemented with the reason
// "process enumeration is per-platform; FreeBSD needs `kvm` or `sysctl
// kern.proc`". Both of those are true and neither is reachable:
// `libkvm` is C, and `sysctl kern.proc` from Go needs either cgo or
// golang.org/x/sys, which SPEC 4.2 rules out. Linux's /proc could be
// read directly and no other platform has one, so a reader written that
// way would be a Linux module wearing a portable name.
//
// So it asks `ps`, which every unix has and which is specified: the
// column names used here are POSIX, and the two families that matter
// spell the same set the same way.
//
// # The machine-readable interface where there is one
//
// FreeBSD's `ps` speaks libxo, so on FreeBSD this asks for JSON and
// parses that. It is the `jls` decision of DIVERGENCE 5.32 for the same
// reason: parsing a human-aligned table where a structured interface
// exists is choosing the surface that bit `pf` in 5.31. Elsewhere the
// columns are requested explicitly and the command is taken as
// everything after the last fixed field, because a command line holds
// spaces and nothing else on the row does.
//
// # What it does not do
//
// No Windows. `ps` is a unix program and a Windows process list is a
// different mechanism entirely, which `win_task` and `win_service`
// already reach for what they need; a `ps` that quietly meant something
// else on one platform would be worse than one that says it does not
// run there.
//
// No CPU or memory totals. `status.loadavg`, `status.meminfo` and
// `status.uptime` already answer those, and a second spelling of the
// same number is a thing to keep in agreement for no gain.
func registerPS(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "pid_list",
				Doc:       "Every process ID on the node, in numeric order.",
				Returns:   "a list of process IDs",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				procs, err := psList(c)
				if err != nil {
					return nil, err
				}
				out := make([]any, len(procs))
				for i, p := range procs {
					out[i] = p.PID
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "proc_info",
				Doc: "What the node knows about one process. Refuses a process that is " +
					"not there, rather than answering with an empty mapping.",
				Params: []signature.Param{
					req("pid", signature.Int, "The process ID."),
				},
				Returns:   "a mapping of the process's fields",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: psProcInfoFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "pgrep",
				Doc: "The process IDs whose name matches a pattern. `full` matches the " +
					"whole command line rather than the program name, and `user` " +
					"restricts the search to one account.",
				Params: []signature.Param{
					req("pattern", signature.String, "A regular expression."),
					opt("full", signature.Bool, false, "Match the whole command line."),
					opt("user", signature.String, "", "Only this account's processes."),
				},
				Returns:   "a list of process IDs",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				matched, err := psMatch(c, args)
				if err != nil {
					return nil, err
				}
				out := make([]any, len(matched))
				for i, p := range matched {
					out[i] = p.PID
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "psaux",
				Doc: "The matching processes in full, which is what an operator wants " +
					"when `pgrep` has answered with a number they cannot place.",
				Params: []signature.Param{
					req("pattern", signature.String, "A regular expression."),
					opt("full", signature.Bool, true, "Match the whole command line."),
					opt("user", signature.String, "", "Only this account's processes."),
				},
				Returns:   "a list of mappings, one per matching process",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				matched, err := psMatch(c, args)
				if err != nil {
					return nil, err
				}
				return psMaps(matched), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "top",
				Doc: "The heaviest processes, by processor share or by resident memory. " +
					"This is the incident question: what is eating the machine.",
				Params: []signature.Param{
					opt("num_processes", signature.Int, 5, "How many to return."),
					opt("by", signature.String, "cpu", "cpu or memory."),
				},
				Returns:   "a list of mappings, heaviest first",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: psTopFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "kill_pid",
				Doc: "Signal one process. The default is TERM, which asks; KILL, which " +
					"does not, has to be named.",
				Params: []signature.Param{
					req("pid", signature.Int, "The process ID."),
					opt("signal", signature.String, "TERM", "A signal name or number."),
				},
				Returns:    "true when the signal was delivered",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root to signal another account's process"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: psKillPIDFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "ps", Function: "pkill",
				Doc: "Signal every process matching a pattern. Refuses a pattern that " +
					"matches nothing, so a tree cannot report success for a name it " +
					"has misspelled.",
				Params: []signature.Param{
					req("pattern", signature.String, "A regular expression."),
					opt("signal", signature.String, "TERM", "A signal name or number."),
					opt("full", signature.Bool, false, "Match the whole command line."),
					opt("user", signature.String, "", "Only this account's processes."),
				},
				Returns:    "a mapping of the process IDs signalled",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root to signal another account's process"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: psPkillFn,
		},
	)
}

// psProcess is one row of the process table.
//
// The fields are the ones every unix `ps` reports under the same
// names. Anything platform-specific is deliberately absent: a mapping
// whose keys depend on the node is one a tree cannot be written
// against.
type psProcess struct {
	PID     int64
	PPID    int64
	User    string
	CPU     float64
	Mem     float64
	RSS     int64
	VSZ     int64
	State   string
	Command string
}

// Name is the program, without its path or its arguments, which is what
// `pgrep` matches unless it is asked for the whole line.
func (p psProcess) Name() string {
	field := p.Command
	if i := strings.IndexByte(field, ' '); i >= 0 {
		field = field[:i]
	}
	// A kernel thread is bracketed on Linux -- `[kworker/0:1]` -- and
	// the slash inside one is part of the name rather than a path.
	// Taking the last path element first turned that into `0:1`, so a
	// `pgrep kworker` matched nothing; the brackets come off and the
	// rest is left alone.
	if strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]") {
		return strings.Trim(field, "[]")
	}
	if i := strings.LastIndexByte(field, '/'); i >= 0 {
		field = field[i+1:]
	}
	return field
}

func (p psProcess) Map() *value.Map {
	m := value.NewMap(9)
	m.Set("pid", p.PID)
	m.Set("ppid", p.PPID)
	m.Set("user", p.User)
	m.Set("name", p.Name())
	m.Set("cpu_percent", p.CPU)
	m.Set("mem_percent", p.Mem)
	m.Set("rss_kb", p.RSS)
	m.Set("vsz_kb", p.VSZ)
	m.Set("state", p.State)
	m.Set("command", p.Command)
	return m
}

func psMaps(procs []psProcess) []any {
	out := make([]any, len(procs))
	for i, p := range procs {
		out[i] = p.Map()
	}
	return out
}

// psColumns is the column set, in order. POSIX names, spelled the same
// by procps and by every BSD ps.
var psColumns = []string{"pid", "ppid", "user", "%cpu", "%mem", "rss", "vsz", "state", "command"}

// psList reads the process table.
func psList(c *exec.Context) ([]psProcess, error) {
	if psUsesLibxo() {
		return psListLibxo(c)
	}
	return psListColumns(c)
}

// psUsesLibxo reports whether this node's `ps` speaks libxo, which is a
// FreeBSD thing rather than a BSD thing: macOS and OpenBSD do not have
// it.
func psUsesLibxo() bool { return runtime.GOOS == "freebsd" }

// psListLibxo parses FreeBSD's own JSON.
//
// The field names are libxo's, which are not the column names: `%cpu`
// arrives as `percent-cpu` and `vsz` as `virtual-size`. They are read
// from the document rather than assumed positionally, which is the
// whole reason for preferring this interface.
func psListLibxo(c *exec.Context) ([]psProcess, error) {
	res, err := c.Run(exec.Command{Argv: []string{"ps", "--libxo=json", "-axwwo",
		strings.Join(psColumns, ",")}})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("ps: %s", firstLine(res.Stderr))
	}
	decoded, err := value.DecodeJSON([]byte(res.Stdout))
	if err != nil {
		return nil, fmt.Errorf("ps --libxo=json returned something that is not JSON: %w", err)
	}
	top, ok := decoded.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("ps --libxo=json returned a %T", decoded)
	}
	info, _ := top.Get("process-information")
	infoMap, ok := info.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("ps --libxo=json has no process-information object")
	}
	rows, _ := infoMap.Get("process")
	list, ok := rows.([]any)
	if !ok {
		return nil, fmt.Errorf("ps --libxo=json has no process list")
	}

	out := make([]psProcess, 0, len(list))
	for _, row := range list {
		m, ok := row.(*value.Map)
		if !ok {
			continue
		}
		// Every value arrives as a string, including the numbers.
		out = append(out, psProcess{
			PID:     psInt(libxoField(m, "pid")),
			PPID:    psInt(libxoField(m, "ppid")),
			User:    libxoField(m, "user"),
			CPU:     psFloat(libxoField(m, "percent-cpu")),
			Mem:     psFloat(libxoField(m, "percent-memory")),
			RSS:     psInt(libxoField(m, "rss")),
			VSZ:     psInt(libxoField(m, "virtual-size")),
			State:   libxoField(m, "state"),
			Command: libxoField(m, "command"),
		})
	}
	sortByPID(out)
	return out, nil
}

func libxoField(m *value.Map, key string) string {
	v, ok := m.Get(key)
	if !ok {
		return ""
	}
	return value.KeyString(v)
}

// psListColumns parses the column output every other unix gives.
//
// The command is everything after the eighth field, because it is the
// only column that holds spaces. `-ww` keeps it from being truncated at
// the terminal width, which is how a long Java command line becomes an
// unmatchable one.
func psListColumns(c *exec.Context) ([]psProcess, error) {
	res, err := c.Run(exec.Command{Argv: psArgv()})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("ps: %s", firstLine(res.Stderr))
	}
	return parsePSColumns(res.Stdout), nil
}

// parsePSColumns is the parsing, separated from the running so that the
// rule that matters can be tested without a process table: the command
// is everything after the eighth field, and it is the only column that
// can hold a space.
func parsePSColumns(stdout string) []psProcess {
	var out []psProcess
	for i, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < len(psColumns) {
			continue
		}
		// A header is present on the BSD spelling and absent on the
		// Linux one, so it is recognised rather than counted out.
		if i == 0 && strings.EqualFold(fields[0], "pid") {
			continue
		}
		// Everything from the ninth field on is the command, rejoined
		// on single spaces: the original spacing inside a command line
		// is not recoverable from a column listing and nothing reads
		// it.
		command := strings.Join(fields[len(psColumns)-1:], " ")
		out = append(out, psProcess{
			PID:     psInt(fields[0]),
			PPID:    psInt(fields[1]),
			User:    fields[2],
			CPU:     psFloat(fields[3]),
			Mem:     psFloat(fields[4]),
			RSS:     psInt(fields[5]),
			VSZ:     psInt(fields[6]),
			State:   fields[7],
			Command: command,
		})
	}
	sortByPID(out)
	return out
}

// psArgv is the command for a platform without libxo.
//
// Linux's ps takes the standard syntax and BSD's takes its own, and the
// difference is not cosmetic: `ps -eo` on a BSD ps is a request for an
// event, and `ps -axo` on procps warns about the missing dash. Each
// gets the spelling its own manual documents.
func psArgv() []string {
	columns := strings.Join(psColumns, ",")
	if runtime.GOOS == "linux" {
		return []string{"ps", "-eww", "--no-headers", "-o", columns}
	}
	return []string{"ps", "-axwwo", columns}
}

func sortByPID(procs []psProcess) {
	sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
}

func psInt(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func psFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// ---- the functions ----

func psProcInfoFn(c *exec.Context, args *value.Map) (any, error) {
	pid := states.Int(args, "pid", 0)
	procs, err := psList(c)
	if err != nil {
		return nil, err
	}
	for _, p := range procs {
		if p.PID == int64(pid) {
			return p.Map(), nil
		}
	}
	return nil, fmt.Errorf("no process %d is running", pid)
}

// psMatch is the shared selection behind pgrep, psaux and pkill.
func psMatch(c *exec.Context, args *value.Map) ([]psProcess, error) {
	pattern := states.Str(args, "pattern", "")
	if pattern == "" {
		return nil, fmt.Errorf("a pattern is needed; an empty one would match every process")
	}
	re, err := regexcompat.Compile(pattern)
	if err != nil {
		return nil, err
	}
	full := states.Bool(args, "full", false)
	user := states.Str(args, "user", "")

	procs, err := psList(c)
	if err != nil {
		return nil, err
	}
	var out []psProcess
	for _, p := range procs {
		if user != "" && p.User != user {
			continue
		}
		subject := p.Name()
		if full {
			subject = p.Command
		}
		if re.MatchString(subject) {
			out = append(out, p)
		}
	}
	return out, nil
}

func psTopFn(c *exec.Context, args *value.Map) (any, error) {
	count := states.Int(args, "num_processes", 5)
	if count <= 0 {
		return nil, fmt.Errorf("num_processes is %d; it has to be at least 1", count)
	}
	by := strings.ToLower(states.Str(args, "by", "cpu"))
	switch by {
	case "cpu", "memory", "mem":
	default:
		return nil, fmt.Errorf("`by` is %q; it is cpu or memory", by)
	}

	procs, err := psList(c)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(procs, func(i, j int) bool {
		if by == "cpu" {
			return procs[i].CPU > procs[j].CPU
		}
		return procs[i].RSS > procs[j].RSS
	})
	if len(procs) > int(count) {
		procs = procs[:count]
	}
	return psMaps(procs), nil
}

func psKillPIDFn(c *exec.Context, args *value.Map) (any, error) {
	pid := states.Int(args, "pid", 0)
	if pid <= 0 {
		return nil, fmt.Errorf("a process ID is needed")
	}
	signal, err := psSignal(states.Str(args, "signal", "TERM"))
	if err != nil {
		return nil, err
	}
	procs, err := psList(c)
	if err != nil {
		return nil, err
	}
	var target *psProcess
	for i := range procs {
		if procs[i].PID == int64(pid) {
			target = &procs[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no process %d is running", pid)
	}
	if c.Test {
		c.Logf("info", "would send %s to %d (%s)", signal, pid, target.Name())
		return true, nil
	}
	return psSignalPID(c, int64(pid), signal)
}

func psPkillFn(c *exec.Context, args *value.Map) (any, error) {
	signal, err := psSignal(states.Str(args, "signal", "TERM"))
	if err != nil {
		return nil, err
	}
	matched, err := psMatch(c, args)
	if err != nil {
		return nil, err
	}
	if len(matched) == 0 {
		// Refused rather than reported as nothing to do. A pattern
		// matching no process is far more often a misspelling than a
		// tidy machine, and `pgrep` is how a tree asks the question
		// without the answer being an action.
		return nil, fmt.Errorf("no process matches %q", states.Str(args, "pattern", ""))
	}

	killed := value.NewMap(len(matched))
	for _, p := range matched {
		if c.Test {
			killed.Set(strconv.FormatInt(p.PID, 10), p.Name())
			continue
		}
		if _, err := psSignalPID(c, p.PID, signal); err != nil {
			return nil, err
		}
		killed.Set(strconv.FormatInt(p.PID, 10), p.Name())
	}
	if c.Test {
		c.Logf("info", "would send %s to %d process(es)", signal, killed.Len())
	}
	return killed, nil
}

// psSignalPID sends one signal through `kill`.
//
// Through the program rather than through os.Process.Signal, because
// the module's whole reading side is the system's own view of the
// process table and mixing the two would mean a pid read from `ps` and
// signalled through a Go handle that was never opened. `kill` is also
// where a permission failure comes back in the words an operator will
// recognise.
func psSignalPID(c *exec.Context, pid int64, signal string) (any, error) {
	res, err := c.Run(exec.Command{Argv: []string{"kill", "-" + signal, strconv.FormatInt(pid, 10)}})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("signalling %d: %s", pid, firstLine(res.Stderr))
	}
	return true, nil
}

// psSignal normalises a signal name.
//
// A name, a number, and a name with the SIG prefix are all spellings an
// existing tree uses, and `kill` accepts the bare name and the number
// on every unix. A signal this does not recognise is refused rather
// than passed through, because `kill -0` and `kill -9` differ by one
// character and one of them is a question.
func psSignal(raw string) (string, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if name == "" {
		return "TERM", nil
	}
	if n, err := strconv.Atoi(name); err == nil {
		if n < 0 || n > 64 {
			return "", fmt.Errorf("signal %d is outside the range a unix has", n)
		}
		return strconv.Itoa(n), nil
	}
	name = strings.TrimPrefix(name, "SIG")
	for _, known := range psSignals {
		if name == known {
			return name, nil
		}
	}
	return "", fmt.Errorf("%q is not a signal this build will send; it sends %s, or a number",
		raw, strings.Join(psSignals, ", "))
}

// psSignals is the set every unix has under the same name. The
// platform-specific ones are left out on purpose: a tree naming SIGINFO
// would work on a BSD and fail on Linux, and the failure would be at
// the moment it was needed.
var psSignals = []string{"HUP", "INT", "QUIT", "KILL", "TERM", "USR1", "USR2", "STOP", "CONT", "0"}
