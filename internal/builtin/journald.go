package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/varlink"
)

// JournaldVarlinkSocket is systemd-journald's own varlink endpoint. A
// variable so a test can point it elsewhere.
var JournaldVarlinkSocket = "/run/systemd/journal/io.systemd.journal"

// registerJournald installs the `journald` module of SPEC 15.3's Common
// Linux row.
//
// # Reads come back structured; nothing parses the human `journalctl`
//
// SPEC 15.3's journald row asks for the journal "over a socket rather
// than by parsing `journalctl` output". The journal has no read
// interface without cgo — its varlink socket does rotate, flush and
// sync and nothing else, and the file format needs LZ4/XZ/ZSTD
// decompressors this build has no dependency for. So a *query* runs
// `journalctl -o json`, which is the same call `jail` makes to
// `jls --libxo=json` (DIVERGENCE 5.32): a documented, stable, one-
// object-per-line serialisation, not the aligned columns of `-o short`
// that SPEC's sentence is actually about. `field_values` and `fields`
// read `-F` and `-N`, which emit one bare value per line and have no
// structure to misread.
//
// # The control verbs *do* go over the socket
//
// `rotate`, `flush` and `sync` are `io.systemd.Journal.Rotate`,
// `FlushToVar` and `Synchronize` on the varlink socket, with a
// `journalctl --rotate` / `--flush` / `--sync` fallback for when the
// socket cannot be reached — the same shape the systemd `service`
// provider uses for D-Bus (DIVERGENCE 5.39). Which path was taken is
// reported.
//
// # There is no `journald` state
//
// SPEC 15.5 names none, the way it names none for `pam`. A log query
// has nothing to converge, and journald's *settings* live in
// journald.conf, which `file.managed` already manages. `vacuum` is an
// action a schedule runs, not a target a state holds.
func registerJournald(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "query",
				Doc: "Read journal entries matching a set of filters, newest last, as structured records.",
				Params: []signature.Param{
					opt("match", signature.List, nil,
						"Field matches such as `_SYSTEMD_UNIT=sshd.service`. Repeats of one field are OR; different fields are AND."),
					opt("unit", signature.String, "", "Shorthand for a `_SYSTEMD_UNIT` match, like `journalctl -u`."),
					opt("priority", signature.String, "", "A priority or range: `err`, `0..3`, `warning..err`."),
					opt("since", signature.String, "", "Only entries at or after this time (a systemd time spec or an absolute timestamp)."),
					opt("until", signature.String, "", "Only entries at or before this time."),
					opt("boot", signature.String, "", "A boot id or offset (`0` is the current boot). Empty spans all boots."),
					opt("grep", signature.String, "", "A pattern the MESSAGE field must match."),
					opt("kernel", signature.Bool, false, "Only kernel messages."),
					opt("lines", signature.Int, int64(100), "At most this many entries. Zero means no limit."),
					opt("reverse", signature.Bool, false, "Return newest first."),
					opt("after_cursor", signature.String, "",
						"Only entries after this `__CURSOR`. The result's `cursor` field feeds the next call, for polling."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: journaldQueryFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "fields",
				Doc:        "Return every field name present in the journal.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return journaldLines(c, []string{"journalctl", "-q", "--no-pager", "-N"})
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "field_values",
				Doc: "Return every distinct value a field takes in the journal.",
				Params: []signature.Param{
					req("field", signature.String, "The field, such as _SYSTEMD_UNIT or PRIORITY."),
				},
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				field := strings.TrimSpace(states.Str(args, "field", ""))
				if field == "" || !journaldValidField(field) {
					return nil, fmt.Errorf("%q is not a journal field name", field)
				}
				return journaldLines(c, []string{"journalctl", "-q", "--no-pager", "-F", field})
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "list_boots",
				Doc:        "Return the recorded boots, oldest first, each with its id and first and last entry time.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: journaldListBootsFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "disk_usage",
				Doc:        "Return how much space the journal files take on disk.",
				TestMode:   signature.TestNotApplicable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: journaldDiskUsageFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "rotate",
				Doc:        "Close the active journal files and start new ones. Over the varlink socket, or `journalctl --rotate`.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return journaldControl(c, "Rotate", "io.systemd.Journal.Rotate", "--rotate", "rotated")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "flush",
				Doc:        "Move the runtime journal in /run into the persistent one in /var. Over the varlink socket, or `journalctl --flush`.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return journaldControl(c, "FlushToVar", "io.systemd.Journal.FlushToVar", "--flush", "flushed to /var")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "sync",
				Doc:        "Flush journal buffers to disk and wait for it. Over the varlink socket, or `journalctl --sync`.",
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return journaldControl(c, "Synchronize", "io.systemd.Journal.Synchronize", "--sync", "synced")
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "journald", Function: "vacuum",
				Doc: "Delete archived journal files to bring the journal under a size, an age, or a file count.",
				Params: []signature.Param{
					opt("size", signature.String, "", "Keep the journal under this size, such as 500M or 2G."),
					opt("time", signature.String, "", "Delete entries older than this, such as 2weeks or 30d."),
					opt("files", signature.Int, int64(0), "Keep at most this many journal files per identifier."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: journaldVacuumFn,
		},
	)
}

// ---- reading ----

func journaldToolPresent(c *exec.Context) error {
	if c.Which("journalctl") == "" {
		return errors.New(
			"this node has no `journalctl`; the journal is part of systemd, so a node without it either " +
				"predates systemd or logs another way")
	}
	return nil
}

// journaldQueryArgv builds the `journalctl -o json` command, a pure
// function of its options so the test pins it without a journal.
func journaldQueryArgv(o journaldQueryOpts) ([]string, error) {
	argv := []string{"journalctl", "-q", "--no-pager", "-o", "json"}
	if o.Reverse {
		argv = append(argv, "-r")
	}
	if o.Kernel {
		argv = append(argv, "-k")
	}
	if o.Unit != "" {
		argv = append(argv, "-u", o.Unit)
	}
	if o.Priority != "" {
		argv = append(argv, "-p", o.Priority)
	}
	if o.Since != "" {
		argv = append(argv, "--since", o.Since)
	}
	if o.Until != "" {
		argv = append(argv, "--until", o.Until)
	}
	if o.Boot != "" {
		argv = append(argv, "-b", o.Boot)
	}
	if o.Grep != "" {
		argv = append(argv, "-g", o.Grep)
	}
	if o.AfterCursor != "" {
		argv = append(argv, "--after-cursor", o.AfterCursor)
	}
	switch {
	case o.Lines < 0:
		return nil, fmt.Errorf("lines is %d; it is a count, or zero for no limit", o.Lines)
	case o.Lines == 0:
		argv = append(argv, "--lines=all")
	default:
		argv = append(argv, "-n", strconv.FormatInt(o.Lines, 10))
	}
	// Matches go last: journalctl reads everything after its options as
	// `FIELD=value` terms.
	for _, m := range o.Match {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !journaldValidMatch(m) {
			return nil, fmt.Errorf("%q is not a journal match; it is `FIELD=value`, or `+` on its own to OR the next group", m)
		}
		argv = append(argv, m)
	}
	return argv, nil
}

type journaldQueryOpts struct {
	Match       []string
	Unit        string
	Priority    string
	Since       string
	Until       string
	Boot        string
	Grep        string
	Kernel      bool
	Lines       int64
	Reverse     bool
	AfterCursor string
}

// journaldValidField accepts a journal field name: uppercase letters,
// digits and underscore, optionally leading underscores for the trusted
// fields. Anything else is a caller mistake, not a field.
func journaldValidField(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func journaldValidMatch(s string) bool {
	if s == "+" {
		return true
	}
	field, _, ok := strings.Cut(s, "=")
	return ok && journaldValidField(field)
}

func journaldQueryFn(c *exec.Context, args *value.Map) (any, error) {
	if err := journaldToolPresent(c); err != nil {
		return nil, err
	}
	opts := journaldQueryOpts{
		Match:       states.Strings(args, "match"),
		Unit:        strings.TrimSpace(states.Str(args, "unit", "")),
		Priority:    strings.TrimSpace(states.Str(args, "priority", "")),
		Since:       strings.TrimSpace(states.Str(args, "since", "")),
		Until:       strings.TrimSpace(states.Str(args, "until", "")),
		Boot:        strings.TrimSpace(states.Str(args, "boot", "")),
		Grep:        strings.TrimSpace(states.Str(args, "grep", "")),
		Kernel:      states.Bool(args, "kernel", false),
		Lines:       states.Int(args, "lines", 100),
		Reverse:     states.Bool(args, "reverse", false),
		AfterCursor: strings.TrimSpace(states.Str(args, "after_cursor", "")),
	}
	argv, err := journaldQueryArgv(opts)
	if err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("journalctl could not be run: %w", err)
	}
	// journalctl exits 1 for "no entries matched", which is an answer,
	// not a failure. Anything else with no output is a real error.
	if res.Code != 0 && strings.TrimSpace(res.Stdout) == "" && strings.Contains(res.Stderr, "Failed") {
		return nil, fmt.Errorf("journalctl exited %d: %s", res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	entries, cursor, err := journaldParseJSONStream(res.Stdout)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(3)
	out.Set("entries", entries)
	out.Set("count", int64(len(entries)))
	out.Set("cursor", nilIfEmpty(cursor))
	return out, nil
}

// journaldParseJSONStream reads journalctl's `-o json`: one JSON object
// per line. The last object's `__CURSOR` is returned separately, so a
// caller polling for new entries has the token for the next `query`.
func journaldParseJSONStream(out string) ([]any, string, error) {
	var (
		entries []any
		cursor  string
	)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, "", fmt.Errorf("a journal entry did not parse as JSON: %w", err)
		}
		if cur, ok := m["__CURSOR"].(string); ok {
			cursor = cur
		}
		entries = append(entries, value.FromJSON(m))
	}
	return entries, cursor, nil
}

// journaldLines runs a command whose output is one bare value per line
// and returns them as a list, dropping blanks.
func journaldLines(c *exec.Context, argv []string) (any, error) {
	if err := journaldToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 && strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("`%s` exited %d: %s",
			strings.Join(argv, " "), res.Code, strings.TrimSpace(firstLine(res.Stderr)))
	}
	var out []any
	for _, line := range strings.Split(res.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func journaldListBootsFn(c *exec.Context, args *value.Map) (any, error) {
	if err := journaldToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"journalctl", "-q", "--no-pager", "--list-boots", "-o", "json"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, fmt.Errorf("journalctl could not be run: %w", err)
	}
	text := strings.TrimSpace(res.Stdout)
	if text == "" {
		return []any{}, nil
	}
	var boots []any
	if err := json.Unmarshal([]byte(text), &boots); err != nil {
		return nil, fmt.Errorf("`journalctl --list-boots -o json` did not parse: %w", err)
	}
	out := make([]any, len(boots))
	for i, b := range boots {
		out[i] = value.FromJSON(b)
	}
	return out, nil
}

func journaldDiskUsageFn(c *exec.Context, args *value.Map) (any, error) {
	if err := journaldToolPresent(c); err != nil {
		return nil, err
	}
	res, err := c.Run(exec.Command{Argv: []string{"journalctl", "-q", "--no-pager", "--disk-usage"}, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("journalctl could not be run: %w", err)
	}
	// `Archived and active journals take up 82.3M in the file system.`
	text := strings.TrimSpace(res.Stdout)
	out := value.NewMap(2)
	out.Set("text", text)
	if bytes, ok := journaldParseUsage(text); ok {
		out.Set("bytes", bytes)
	} else {
		out.Set("bytes", nil)
	}
	return out, nil
}

// journaldParseUsage pulls the byte count out of journalctl's one
// sentence. systemd's sizes are binary (1024-based) and always carry a
// unit.
func journaldParseUsage(text string) (int64, bool) {
	const marker = "take up "
	i := strings.Index(text, marker)
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(text[i+len(marker):])
	word, _, _ := strings.Cut(rest, " ")
	if word == "" {
		return 0, false
	}
	unit := word[len(word)-1]
	digits := word
	var mult float64 = 1
	switch unit {
	case 'B':
		digits = word[:len(word)-1]
	case 'K':
		digits, mult = word[:len(word)-1], 1<<10
	case 'M':
		digits, mult = word[:len(word)-1], 1<<20
	case 'G':
		digits, mult = word[:len(word)-1], 1<<30
	case 'T':
		digits, mult = word[:len(word)-1], 1<<40
	default:
		if unit < '0' || unit > '9' {
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, false
	}
	return int64(f * mult), true
}

// ---- control ----

// journaldControl runs one of journald's control verbs over the varlink
// socket, falling back to journalctl when the socket is unreachable.
func journaldControl(c *exec.Context, name, method, journalctlFlag, past string) (any, error) {
	if err := journaldToolPresent(c); err != nil {
		return nil, err
	}
	if c.Test {
		out := value.NewMap(3)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf("the journal would be %s. Nothing was changed: this was a test run.", past))
		out.Set("via", "varlink or journalctl "+journalctlFlag)
		return out, nil
	}

	ctx := c.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	via := "varlink"
	_, verr := varlink.Call(ctx, JournaldVarlinkSocket, method, nil)
	if verr != nil {
		var vErr *varlink.Error
		if errors.As(verr, &vErr) {
			// The service answered and refused. That is a real failure —
			// falling back to journalctl would just get the same refusal
			// with fewer words.
			return nil, fmt.Errorf("journald refused %s over varlink: %w", name, vErr)
		}
		// The socket was not reachable (older systemd, a namespace
		// without it). journalctl's flag does the same job.
		c.Logf("debug", "journald varlink %s unreachable (%v); using journalctl %s", method, verr, journalctlFlag)
		via = "journalctl " + journalctlFlag
		res, err := c.Run(exec.Command{Argv: []string{"journalctl", journalctlFlag}, IgnoreExitCode: true})
		if err != nil {
			return nil, fmt.Errorf("journalctl %s could not be run: %w", journalctlFlag, err)
		}
		if res.Code != 0 {
			return nil, fmt.Errorf("`journalctl %s` exited %d: %s",
				journalctlFlag, res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
		}
	}
	out := value.NewMap(3)
	out.Set("changed", true)
	out.Set("comment", fmt.Sprintf("the journal was %s.", past))
	out.Set("via", via)
	return out, nil
}

func journaldVacuumFn(c *exec.Context, args *value.Map) (any, error) {
	if err := journaldToolPresent(c); err != nil {
		return nil, err
	}
	size := strings.TrimSpace(states.Str(args, "size", ""))
	age := strings.TrimSpace(states.Str(args, "time", ""))
	files := states.Int(args, "files", 0)
	argv := []string{"journalctl", "-q", "--no-pager"}
	if size != "" {
		argv = append(argv, "--vacuum-size="+size)
	}
	if age != "" {
		argv = append(argv, "--vacuum-time="+age)
	}
	if files > 0 {
		argv = append(argv, "--vacuum-files="+strconv.FormatInt(files, 10))
	}
	if len(argv) == 3 {
		return nil, errors.New("vacuum needs at least one of size, time or files")
	}

	if c.Test {
		out := value.NewMap(3)
		out.Set("changed", true)
		out.Set("comment", "the journal would be vacuumed. Nothing was changed: this was a test run.")
		out.Set("command", exec.Command{Argv: argv}.String())
		return out, nil
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("journalctl could not be run: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`%s` exited %d: %s",
			exec.Command{Argv: argv}.String(), res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	// journalctl prints the deletions to stderr and ends with a
	// `Vacuuming done, freed <N> of archived journals ...` line.
	report := strings.TrimSpace(res.Stderr)
	if report == "" {
		report = strings.TrimSpace(res.Stdout)
	}
	out := value.NewMap(2)
	out.Set("changed", report != "" && !strings.Contains(report, "freed 0B"))
	out.Set("output", report)
	return out, nil
}
