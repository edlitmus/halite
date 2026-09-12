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

// atPlatforms are the platforms this module has been checked against.
//
// `at`, `atq` and `atrm` on this project's own FreeBSD host are one
// setuid binary with four hardlinks, dispatching on argv[0] -- and its
// embedded strings ("cannot generate job number", the `/var/at/jobs/`
// job directory, the `.SEQ` sequence file, "Job %ld will be executed
// using /bin/sh") are the fingerprints of the classic BSD-licensed `at`
// package that Linux distributions ship too, under the same three names.
// That is why this is one code path for both rather than a table with
// two branches: it is the same program, not a lookalike, and the two
// branches would be identical. Other platforms are not claimed, because
// nobody has run this against one.
var atPlatforms = []string{"linux", "freebsd"}

// registerAt installs the `at` module of SPEC 15.2's core execution
// modules and its matching SPEC 15.5 state: a one-shot job queue,
// scheduled once and run once, next to `cron`'s recurring one.
//
// # What is reused from `cron`, and what is not
//
// `cron.go` is the closest neighbour, and one idea is taken from it
// deliberately: a state that wants to find its own entry again on the
// next run writes an identifying comment where it can read it back,
// rather than matching on the command text, which a tree may
// legitimately want to schedule more than once. `at.present` and
// `at.absent` do the same thing cron does -- write `#HALITE_AT_
// IDENTIFIER: <id>` as the first line of the job -- so the same trick
// that lets `cron.present` recognise its own line lets `at.present`
// recognise its own job.
//
// Nothing else is shared. A crontab is one file, rewritten whole, with
// entries cron itself never reorders; an at job is a queue of
// independent one-shot files, each already committed to disk the moment
// `at` accepts it, addressed by a job number the tool hands back rather
// than a line this module wrote. `parseCrontab`, `renderCrontab` and
// `readCrontab`/`writeCrontab` all assume the one-file-rewritten-whole
// shape and none of them fit here, so `at` gets its own small reader
// (`atListJobs`, `atShowScript`) built against what `atq` and `at -c`
// actually print rather than adapted from code that solves a different
// problem.
//
// # `atq`'s job number is the last column, not a fixed one
//
// `atq`'s row format, read out of the real binary's own strings rather
// than guessed (see the comment on `atParseQueueList`), packs the date,
// the owner and the queue letter together with no reliable separator
// between the last two. The job number is the one field this module
// trusts: it is always the last whitespace-separated token on the row,
// confirmed against the format string embedded in `/usr/bin/atq`
// (FreeBSD-at-15.1) and against this exact mistake having been made once
// already while probing this host by hand. Everything before it is kept
// as `raw` for a human to read and not parsed further, rather than
// inventing a column split this module cannot verify.
//
// # This was written without ever running `at` as this project's own
// # account
//
// `at`, `atq` and `atrm` all refuse a non-root caller on this host --
// "you do not have permission to use this program" -- because neither
// /etc/at.allow nor /etc/at.deny exists here, and this implementation's
// documented default with both absent is that only the superuser may use
// it. Root was not available while this was written, so the row format
// above is derived from the compiled binary's own format strings
// (`strings -a /usr/bin/atq`) rather than from a captured real run. The
// live test in live_at_test.go, run as root, is where that derivation is
// checked against the tool actually running rather than against its
// strings table.
//
// # Privileges says root because that is this tool's own default
//
// Not because the module asks for it. An operator who creates
// /etc/at.allow naming other accounts changes what this needs without
// this module changing at all; `Privileges` records what a fresh install
// enforces, the same as `cron`'s "the target account, or root" records
// what crontab(1) enforces.
func registerAt(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "at", Function: "at",
				Doc: "Schedule a shell command to run once, at a given time.",
				Params: []signature.Param{
					req("timespec", signature.String,
						`When to run it, in at(1)'s own grammar, such as "now + 5 minutes" or "15:00 tomorrow".`),
					req("cmd", signature.String, "The shell command or script to run."),
					opt("queue", signature.String, "", "Which queue letter to use; the tool's own default if empty."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  atPlatforms,
				Section:    "15.2",
			},
			Fn: atAtFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "at", Function: "atq",
				Doc: "Return the jobs waiting in the at queue.",
				Params: []signature.Param{
					opt("queue", signature.String, "", "Limit to one queue letter."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: atPlatforms,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				jobs, err := atListJobs(c, states.Str(args, "queue", ""))
				if err != nil {
					return nil, err
				}
				out := make([]any, len(jobs))
				for i, j := range jobs {
					out[i] = value.MapOf("job", j.Job, "raw", j.Raw)
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "at", Function: "atc",
				Doc: "Return the shell script queued as a job, the way `at -c` prints it.",
				Params: []signature.Param{
					req("job", signature.Int, "The job number."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: atPlatforms,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				job := states.Int(args, "job", 0)
				if job <= 0 {
					return nil, errors.New("a positive job number is required")
				}
				return atShowScript(c, job)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "at", Function: "atrm",
				Doc: "Remove a job from the at queue.",
				Params: []signature.Param{
					req("job", signature.Int, "The job number to remove."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  atPlatforms,
				Section:    "15.2",
			},
			Fn: atAtrmFn,
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "at", Function: "present",
				Doc: "Ensure a one-shot job is queued at a given time.",
				Params: []signature.Param{
					nameParam("The command to run. Defaults to the state ID."),
					req("timespec", signature.String,
						`When to run it, in at(1)'s own grammar, such as "now + 5 minutes" or "15:00 tomorrow".`),
					opt("queue", signature.String, "", "Which queue letter to use."),
					opt("identifier", signature.String, "",
						"The identifying marker written into the job's script; defaults to the command."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  atPlatforms,
				Section:    "15.5",
			},
			Fn: atPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "at", Function: "absent",
				Doc: "Ensure no queued job carries a given command or identifier.",
				Params: []signature.Param{
					nameParam("The command. Defaults to the state ID."),
					opt("identifier", signature.String, "", "The identifying marker; defaults to the command."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  atPlatforms,
				Section:    "15.5",
			},
			Fn: atAbsent,
		},
	)
}

// ---- the platform table ----
//
// One case for both platforms rather than two identical ones spelled out
// separately, so that a third platform this module has not been checked
// against is refused rather than silently given a guess -- the same
// discipline `quotaSetArgv` and `swapOnArgv` apply to a genuine
// difference; here there is no difference to keep separate, only a
// claim to keep bounded.

func atCheckPlatform(goos string) error {
	switch goos {
	case "linux", "freebsd":
		return nil
	}
	return fmt.Errorf("this build does not know the at family's spelling on %s", goos)
}

// atScheduleArgv is the command that queues a job. The time spec is
// at(1)'s own grammar and is passed through as separate words, the way
// it would be typed on a command line, because reinterpreting it here
// would mean maintaining a second copy of at's own time parser.
func atScheduleArgv(goos, queue, timespec string) ([]string, error) {
	if err := atCheckPlatform(goos); err != nil {
		return nil, err
	}
	fields := strings.Fields(timespec)
	if len(fields) == 0 {
		return nil, errors.New(`a time spec is required, in at(1)'s own grammar, such as "now + 5 minutes"`)
	}
	argv := []string{"at"}
	if queue != "" {
		argv = append(argv, "-q", queue)
	}
	return append(argv, fields...), nil
}

// atQueueListArgv is `at -l`, which the usage text embedded in the real
// binary documents as atq's own equivalent -- `at -l -q queuename` or
// `at -l [job ...]` -- so this is asked for through `at` rather than
// through a second binary name, and behaves the same under either.
func atQueueListArgv(goos, queue string) ([]string, error) {
	if err := atCheckPlatform(goos); err != nil {
		return nil, err
	}
	argv := []string{"at", "-l"}
	if queue != "" {
		argv = append(argv, "-q", queue)
	}
	return argv, nil
}

// atRemoveArgv is `atrm`.
func atRemoveArgv(goos string, job int64) ([]string, error) {
	if err := atCheckPlatform(goos); err != nil {
		return nil, err
	}
	return []string{"atrm", strconv.FormatInt(job, 10)}, nil
}

// atShowArgv is `at -c`, the only way to read a job's script back: there
// is no separate `atc` binary on this host, only the usage text's own
// `at -c job [job ...]`.
func atShowArgv(goos string, job int64) ([]string, error) {
	if err := atCheckPlatform(goos); err != nil {
		return nil, err
	}
	return []string{"at", "-c", strconv.FormatInt(job, 10)}, nil
}

// ---- reading ----

// atJobLine is one row of `atq`'s listing. Only the job number is
// trusted structure; see the doc comment on registerAt and on
// atParseQueueList for why the rest is kept as text rather than split
// into columns this module cannot verify.
type atJobLine struct {
	Job int64
	Raw string
}

// atParseQueueList reads atq's listing.
//
// The row format, extracted with `strings -a` from the real
// /usr/bin/atq on this host (FreeBSD-at-15.1, FreeBSD 15.1-RELEASE-p3)
// rather than guessed, is the printf call:
//
//	%s\t%-16s%c%s\t%ld
//
// date, a tab, the owner left-padded to 16 columns immediately followed
// by the one-character queue letter and an optional suffix with no
// separator, a tab, and the job number. Splitting the owner from the
// queue letter needs the padding width, which is fragile the moment an
// owner name is longer than 16 characters; splitting the job number off
// needs nothing but "it is the last field on the row", which is true
// whatever the owner's name is and whatever whitespace-normalising
// happened to the rest of the line in transit. So that is the only
// structure this reads: the trailing token is parsed as the job number,
// and everything before it is kept verbatim as `Raw` for a caller that
// wants to show a person which job is which, not to compare against.
//
// The header line ("Date ... Owner ... Queue ... Job#") is recognised by
// its first word and skipped; an empty queue is an empty slice and not
// an error, the same as an unconfigured filesystem is an empty quota
// report and not an error.
func atParseQueueList(out string) ([]atJobLine, error) {
	var jobs []atJobLine
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "Date") {
			continue
		}
		fields := strings.Fields(trimmed)
		last := fields[len(fields)-1]
		job, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("atq's row %q does not end in a job number; %q is not one", trimmed, last)
		}
		jobs = append(jobs, atJobLine{Job: job, Raw: trimmed})
	}
	return jobs, nil
}

func atListJobs(c *exec.Context, queue string) ([]atJobLine, error) {
	argv, err := atQueueListArgv(runtime.GOOS, queue)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return atParseQueueList(res.Stdout)
}

func atShowScript(c *exec.Context, job int64) (string, error) {
	argv, err := atShowArgv(runtime.GOOS, job)
	if err != nil {
		return "", err
	}
	if c.Which(argv[0]) == "" {
		return "", fmt.Errorf("this node has no `%s`", argv[0])
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("`%s` exited %d: %s", exec.Command{Argv: argv}.String(), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

func atJobQueued(jobs []atJobLine, job int64) bool {
	for _, j := range jobs {
		if j.Job == job {
			return true
		}
	}
	return false
}

// atIdentifierPrefix is the marker `at.present` writes as the first line
// of a job's script, mirroring the trick cron.go's managed entries use
// -- see the doc comment on registerAt for what is and is not shared
// between the two.
const atIdentifierPrefix = "#HALITE_AT_IDENTIFIER:"

func atScriptWithIdentifier(identifier, cmd string) string {
	return atIdentifierPrefix + " " + identifier + "\n" + cmd + "\n"
}

// atFindByIdentifier looks for a queued job whose script starts with this
// module's own marker for the given identifier.
//
// A job that answers `at -c` with an error between the listing and the
// read is skipped rather than failing the whole search: a one-shot job
// firing in that exact window is what "one-shot" means, not a fault in
// either tool.
func atFindByIdentifier(c *exec.Context, identifier string) (int64, bool, error) {
	jobs, err := atListJobs(c, "")
	if err != nil {
		return 0, false, err
	}
	marker := atIdentifierPrefix + " " + identifier
	for _, j := range jobs {
		script, err := atShowScript(c, j.Job)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(script, "\n") {
			if strings.TrimSpace(line) == marker {
				return j.Job, true, nil
			}
		}
	}
	return 0, false, nil
}

// ---- mutating ----

// atParseScheduledJobNumber reads the job number out of at's own
// confirmation, "Job %ld will be executed using /bin/sh" -- the format
// string `strings -a /usr/bin/at` shows this build printing on stderr.
func atParseScheduledJobNumber(text string) (int64, bool) {
	const prefix = "Job "
	i := strings.Index(text, prefix)
	if i < 0 {
		return 0, false
	}
	rest := text[i+len(prefix):]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	job, err := strconv.ParseInt(rest[:digits], 10, 64)
	if err != nil {
		return 0, false
	}
	return job, true
}

func atRunSchedule(c *exec.Context, argv []string, script string) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: argv, Stdin: script, IgnoreExitCode: true})
	if err != nil {
		return nil, fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	job, ok := atParseScheduledJobNumber(res.Stderr + res.Stdout)
	if res.Code != 0 || !ok {
		return nil, fmt.Errorf("`%s` did not confirm a scheduled job (exit %d): %s",
			exec.Command{Argv: argv}.String(), res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	out := value.NewMap(3)
	out.Set("changed", true)
	out.Set("job", job)
	out.Set("comment", fmt.Sprintf("job %d was scheduled.", job))
	return out, nil
}

func atAtFn(c *exec.Context, args *value.Map) (any, error) {
	timespec := strings.TrimSpace(states.Str(args, "timespec", ""))
	cmd := states.Str(args, "cmd", "")
	queue := strings.TrimSpace(states.Str(args, "queue", ""))
	if cmd == "" {
		return nil, errors.New("a command must be given")
	}
	argv, err := atScheduleArgv(runtime.GOOS, queue, timespec)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}
	if c.Test {
		out := value.NewMap(2)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf(
			"a job would be scheduled at %q. Nothing was changed: this was a test run.", timespec))
		return out, nil
	}
	return atRunSchedule(c, argv, cmd+"\n")
}

func atAtrmFn(c *exec.Context, args *value.Map) (any, error) {
	job := states.Int(args, "job", 0)
	if job <= 0 {
		return nil, errors.New("a positive job number is required")
	}
	argv, err := atRemoveArgv(runtime.GOOS, job)
	if err != nil {
		return nil, err
	}
	if c.Which(argv[0]) == "" {
		return nil, fmt.Errorf("this node has no `%s`", argv[0])
	}

	// A read that fails here costs only the "already gone" answer, the
	// same as `quotaCurrent`'s failure does: atrm on a job that is
	// already gone fails on its own, loudly, which is an acceptable
	// fallback and not a silent one.
	if jobs, jerr := atListJobs(c, ""); jerr == nil && !atJobQueued(jobs, job) {
		out := value.NewMap(2)
		out.Set("changed", false)
		out.Set("comment", fmt.Sprintf("job %d is not queued.", job))
		return out, nil
	}

	if c.Test {
		out := value.NewMap(2)
		out.Set("changed", true)
		out.Set("comment", fmt.Sprintf("job %d would be removed. Nothing was changed: this was a test run.", job))
		return out, nil
	}
	if err := atRun(c, argv); err != nil {
		return nil, err
	}
	out := value.NewMap(2)
	out.Set("changed", true)
	out.Set("comment", fmt.Sprintf("job %d was removed.", job))
	return out, nil
}

func atRun(c *exec.Context, argv []string) error {
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

// ---- states ----

func atPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	cmd := states.Str(args, "name", "")
	timespec := strings.TrimSpace(states.Str(args, "timespec", ""))
	queue := strings.TrimSpace(states.Str(args, "queue", ""))
	identifier := states.Str(args, "identifier", "")
	if identifier == "" {
		identifier = cmd
	}
	if cmd == "" {
		return states.False("This state needs a command to run."), nil
	}
	if timespec == "" {
		return states.False("This state needs a time spec."), nil
	}

	job, found, err := atFindByIdentifier(c, identifier)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	if found {
		return states.True(fmt.Sprintf("A job for %s is already queued as job %d.", identifier, job)), nil
	}

	changes := value.MapOf(identifier, states.Change(nil, timespec))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("A job for %s would be queued at %s.", identifier, timespec), changes), nil
	}

	argv, err := atScheduleArgv(runtime.GOOS, queue, timespec)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	if c.Which(argv[0]) == "" {
		return states.False(fmt.Sprintf("this node has no `%s`", argv[0])), nil
	}
	result, err := atRunSchedule(c, argv, atScriptWithIdentifier(identifier, cmd))
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	scheduled, _ := result.GetString("job")
	return states.Changed(
		fmt.Sprintf("A job for %s was queued as job %v at %s.", identifier, scheduled, timespec), changes), nil
}

func atAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	identifier := states.Str(args, "identifier", "")
	if identifier == "" {
		identifier = states.Str(args, "name", "")
	}
	if identifier == "" {
		return states.False("This state needs a command or an identifier."), nil
	}

	job, found, err := atFindByIdentifier(c, identifier)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	if !found {
		return states.True(fmt.Sprintf("No queued job carries the identifier %s.", identifier)), nil
	}

	changes := value.MapOf(identifier, states.Change(job, nil))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("Job %d for %s would be removed.", job, identifier), changes), nil
	}

	argv, err := atRemoveArgv(runtime.GOOS, job)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	if err := atRun(c, argv); err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	return states.Changed(fmt.Sprintf("Job %d for %s was removed.", job, identifier), changes), nil
}
