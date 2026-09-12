package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The six functions SPEC 15.2 names for `file` that this build did not
// have: `patch`, `sed`, `seek_read`, `seek_write`, `list_backups` and
// `restore_backup`.
//
// # `patch` runs the system patch
//
// SPEC says so in as many words -- "`patch` uses the system `patch`
// binary" -- and it is the right call for the same reason `ps` shells
// out: a unified-diff applier that has to agree with GNU patch about
// fuzz, offsets, reversed hunks and backups is a large program to write
// and an unbounded one to keep right.
//
// Two things this does not do, both because the tool decides them and
// neither is portable to override: it does not stop `patch` from
// leaving its own `<name>.orig` beside a file it changed, which the BSD
// patch does and GNU's does not, and it does not interpret a hunk that
// failed. What it does do is discard the reject file, because a `.rej`
// dropped beside a managed file on every run is litter a tree cannot
// clear.
//
// The one thing added on top is that `dry_run` is offered and used:
// `patch --dry-run` is what makes this function usable in test mode
// without a second implementation, and SPEC 11.6's test-mode contract is
// otherwise unsatisfiable for a patch.
//
// # `sed` is not sed
//
// Salt's `file.sed` shells out to `sed -i`. This one does the same work
// in Go, against a regular expression this project's own engine
// compiled and with the atomic write the rest of the module uses, so
// nothing runs an external editor over a file as root.
//
// It does not delegate to `file.replace`, which is the obvious thing and
// is wrong for one reason: `limit` is a *per-line* filter and
// `file.replace` works on the whole file. Passing it through would
// either ignore it, which replaces more than the caller asked and is the
// accept-but-do-nothing defect this project keeps finding in its own
// settings, or quietly mean something else.
//
// # The backup cache
//
// `list_backups` and `restore_backup` need somewhere to list. Salt keeps
// timestamped copies under its node's cache, and a `backup:` value in a
// state is what puts them there. This build had a `backup` argument that
// wrote `<path><suffix>` beside the file, which is useful and is not
// something either function can enumerate.
//
// Both now exist: `backup: node`, and Salt's own value beside it, keeps
// a timestamped copy under `<cache_dir>/file_backup/`, and any other
// value keeps the suffix behaviour exactly as it was.

// fileBackupDirName is where the cache lives under `cache_dir`.
const fileBackupDirName = "file_backup"

// backupTimeFormat orders lexically as well as chronologically, which is
// what lets `list_backups` sort by name and be sorting by time. Colons
// are not in it: a Windows path cannot hold one, and this module runs
// there.
const backupTimeFormat = "2006-01-02T15-04-05.000000000"

func registerFilePatch(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "patch",
				Doc: "Apply a unified diff through the system `patch`. In test mode it " +
					"runs `--dry-run`, so a run that would not apply is reported rather " +
					"than attempted.",
				Params: []signature.Param{
					req("name", signature.Path, "The file or directory to patch."),
					req("source", signature.Path, "The patch file."),
					opt("options", signature.String, "", "Extra options for `patch`."),
					opt("dry_run", signature.Bool, false, "Check without applying."),
					opt("strip", signature.Int, int64(-1), "Path components to strip, as -p."),
				},
				Returns:  "a mapping of the patch's result and its output",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: filePatchFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "sed",
				Doc: "Replace the first match of `before` with `after` on each eligible " +
					"line. Salt's spelling of `file.replace`, which is what actually " +
					"does the work: the regular expression is this build's own engine " +
					"and the write is atomic, so nothing runs an editor over a file.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					req("before", signature.String, "A regular expression."),
					req("after", signature.String, "The replacement."),
					opt("limit", signature.String, "", "Only lines matching this expression are eligible."),
					opt("backup", signature.String, "", "Keep a copy with this suffix before writing."),
					opt("flags", signature.List, nil, "Any of `i`, `m`, `s`, and `g`."),
				},
				Returns:  "a mapping of what changed",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: fileSedFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "seek_read",
				Doc: "Read a number of bytes from an offset, without reading the file.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					req("size", signature.Int, "How many bytes to read."),
					opt("offset", signature.Int, int64(0), "Where to start."),
				},
				Returns:  "the bytes read, as a string",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: fileSeekReadFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "seek_write",
				Doc: "Write bytes at an offset, leaving the rest of the file alone. " +
					"This is the one write in the module that is not atomic, and it " +
					"cannot be: replacing the file would be a different operation.",
				Params: []signature.Param{
					req("path", signature.Path, "The file."),
					req("data", signature.String, "What to write."),
					opt("offset", signature.Int, int64(0), "Where to start."),
				},
				Returns:  "the number of bytes written",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: fileSeekWriteFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "list_backups",
				Doc: "The copies kept for a path, newest first. A state writes them " +
					"with `backup: node`.",
				Params: []signature.Param{
					req("path", signature.Path, "The file the backups are of."),
					opt("limit", signature.Int, int64(0), "How many to return; 0 is all."),
				},
				Returns:  "a list of mappings, newest first",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: fileListBackupsFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "file", Function: "restore_backup",
				Doc: "Put a kept copy back. The current contents are themselves kept " +
					"first, so a restore is undoable.",
				Params: []signature.Param{
					req("path", signature.Path, "The file to restore."),
					req("backup_id", signature.String, "The `id` from `list_backups`."),
				},
				Returns:  "a mapping of what was restored",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "15.2",
			},
			Fn: fileRestoreBackupFn,
		},
	)
}

// ---- patch ----

func filePatchFn(c *exec.Context, args *value.Map) (any, error) {
	target := states.Str(args, "name", "")
	source := states.Str(args, "source", "")
	if target == "" || source == "" {
		return nil, fmt.Errorf("file.patch needs a name and a source")
	}
	if _, err := os.Stat(source); err != nil {
		return nil, fmt.Errorf("the patch %s: %w", source, err)
	}

	extra := strings.Fields(states.Str(args, "options", ""))

	// `--batch --forward`, always, and this is the interesting part of
	// the function.
	//
	// A patch applied twice is what a state run does on its second pass,
	// and `patch` left to itself handles that by *asking*: "Reversed (or
	// previously applied) patch detected! Assume -R? [y]". With no
	// terminal it takes its own default, answers yes, and reverses the
	// change -- exiting 0, so nothing downstream notices. Measured on
	// FreeBSD patch 2.0-12u11, and GNU patch behaves the same way.
	// Silently undoing the thing a tree just asked for is the worst
	// available outcome, so the question is never asked: `--forward`
	// ignores a patch that is already applied and reports it, and
	// `--batch` refuses every other prompt.
	//
	// A caller who really means to reverse one says so with `-R`, and
	// then `--forward` would refuse their own request, so it is left
	// off.
	// `-r -` discards the reject file. Without it, a re-run that
	// ignores an already-applied patch drops a `<name>.rej` beside a
	// file a tree is managing, every time.
	argv := []string{"patch", "--batch", "-r", "-"}
	if !hasReverseOption(extra) {
		argv = append(argv, "--forward")
	}
	if strip := states.Int(args, "strip", -1); strip >= 0 {
		argv = append(argv, fmt.Sprintf("-p%d", strip))
	}
	// A dry run in test mode, so that SPEC 11.6's contract is met by
	// the tool itself rather than by a second implementation of what a
	// patch would do.
	dry := states.Bool(args, "dry_run", false) || c.Test
	if dry {
		argv = append(argv, "--dry-run")
	}
	argv = append(argv, extra...)
	argv = append(argv, filepath.Clean(target), filepath.Clean(source))

	// The exit code is the answer here rather than a failure: `patch`
	// exits 1 for "already applied" under `--forward`, and its own
	// output says which of several things happened. Letting the runner
	// turn that into a generic error would replace the only useful
	// message with "exited 1".
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(4)
	out.Set("applied", res.Code == 0 && !dry)
	out.Set("dry_run", dry)
	out.Set("retcode", int64(res.Code))
	out.Set("output", strings.TrimRight(res.Stdout+res.Stderr, "\n"))
	if res.Code != 0 {
		// `patch` says why on its own output, and its words are better
		// than any summary here: "Ignoring previously applied (or
		// reversed) patch" is the message an operator needs, and it is
		// a different thing from a hunk that would not apply.
		said := collapse(res.Stdout + "\n" + res.Stderr)
		if said == "" {
			said = fmt.Sprintf("exited %d and said nothing", res.Code)
		}
		return out, fmt.Errorf("patch %s: %s", target, said)
	}
	return out, nil
}

// collapse puts a tool's several lines onto one.
//
// All of them, rather than the first or the last: `patch` opens with a
// commentary on what it thinks the file is, ends with "done", and puts
// its verdict -- "Ignoring previously applied (or reversed) patch" --
// in the middle. Picking a line means picking the wrong one.
func collapse(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	out := strings.Join(kept, "; ")
	if len(out) > 400 {
		return out[:400] + "..."
	}
	return out
}

// hasReverseOption reports whether the caller asked for a reversal.
func hasReverseOption(options []string) bool {
	for _, o := range options {
		if o == "-R" || o == "--reverse" || (strings.HasPrefix(o, "-") && !strings.HasPrefix(o, "--") &&
			strings.ContainsRune(o, 'R')) {
			return true
		}
	}
	return false
}

// ---- sed ----

func fileSedFn(c *exec.Context, args *value.Map) (any, error) {
	path := filepath.Clean(states.Str(args, "path", ""))
	before := states.Str(args, "before", "")
	if before == "" {
		return nil, fmt.Errorf("file.sed needs `before`")
	}
	after := states.Str(args, "after", "")
	flags := sedFlags(args)

	// Done here rather than delegated to `file.replace`, for one
	// reason: `limit` is a *per-line* filter and `file.replace` works on
	// the whole file. Passing it through as anything else would either
	// ignore it -- which replaces more than the caller asked, and is the
	// accept-but-do-nothing defect this project keeps finding in its own
	// settings -- or quietly mean something different.
	pattern, err := regexcompat.CompileWithFlags(before, flags.ignoreCase, false, false)
	if err != nil {
		return nil, err
	}
	var eligible *regexp.Regexp
	if limit := states.Str(args, "limit", ""); limit != "" {
		eligible, err = regexcompat.CompileWithFlags(limit, flags.ignoreCase, false, false)
		if err != nil {
			return nil, fmt.Errorf("limit: %w", err)
		}
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	lines := strings.SplitAfter(string(original), "\n")
	changed := 0
	for i, line := range lines {
		body, ending := line, ""
		if strings.HasSuffix(body, "\n") {
			body, ending = body[:len(body)-1], "\n"
		}
		if eligible != nil && !eligible.MatchString(body) {
			continue
		}
		// sed replaces once per line unless told otherwise, which is
		// what the `g` flag means and why it is not the default here.
		var out string
		if flags.count == 0 {
			out = pattern.ReplaceAllString(body, after)
		} else {
			out = replaceFirst(pattern, body, after)
		}
		if out == body {
			continue
		}
		changed++
		lines[i] = out + ending
	}

	result := value.NewMap(3)
	result.Set("path", path)
	result.Set("lines_changed", int64(changed))
	if changed == 0 {
		result.Set("changed", false)
		return result, nil
	}
	result.Set("changed", true)
	if c.Test {
		c.Logf("info", "would change %d line(s) in %s", changed, path)
		return result, nil
	}

	if suffix := states.Str(args, "backup", ""); suffix != "" {
		if err := writeAtomic(path+suffix, original, info.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("the backup of %s: %w", path, err)
		}
		result.Set("backup", path+suffix)
	}
	if err := writeAtomic(path, []byte(strings.Join(lines, "")), info.Mode().Perm()); err != nil {
		return nil, err
	}
	return result, nil
}

// sedFlagSet is Salt's flag string, read.
type sedFlagSet struct {
	ignoreCase bool
	// count is 0 for every match on a line and 1 for the first only,
	// which is sed's own default.
	count int64
}

func sedFlags(args *value.Map) sedFlagSet {
	out := sedFlagSet{count: 1}
	for _, f := range states.Strings(args, "flags") {
		switch strings.ToLower(strings.TrimSpace(f)) {
		case "i":
			out.ignoreCase = true
		case "g":
			out.count = 0
		}
	}
	return out
}

// replaceFirst replaces one match, which is what sed does without `g`.
func replaceFirst(re *regexp.Regexp, line, with string) string {
	loc := re.FindStringSubmatchIndex(line)
	if loc == nil {
		return line
	}
	return line[:loc[0]] + string(re.ExpandString(nil, with, line, loc)) + line[loc[1]:]
}

// ---- seek ----

func fileSeekReadFn(c *exec.Context, args *value.Map) (any, error) {
	path := filepath.Clean(states.Str(args, "path", ""))
	size := states.Int(args, "size", 0)
	if size <= 0 {
		return nil, fmt.Errorf("file.seek_read needs a size above zero")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, size)
	n, err := f.ReadAt(buf, states.Int(args, "offset", 0))
	// A short read at the end of the file is the answer rather than an
	// error: asking for 4 KiB from 100 bytes before the end means "give
	// me what is there".
	if n == 0 && err != nil {
		return nil, err
	}
	return string(buf[:n]), nil
}

func fileSeekWriteFn(c *exec.Context, args *value.Map) (any, error) {
	path := filepath.Clean(states.Str(args, "path", ""))
	data := states.Str(args, "data", "")
	offset := states.Int(args, "offset", 0)
	if c.Test {
		c.Logf("info", "would write %d bytes to %s at offset %d", len(data), path, offset)
		return int64(len(data)), nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := f.WriteAt([]byte(data), offset)
	if err != nil {
		return nil, err
	}
	// Written through, because the caller is editing a file in place and
	// a partial write that survives only in the page cache is the worst
	// version of this operation.
	if err := f.Sync(); err != nil {
		return nil, err
	}
	return int64(n), nil
}

// ---- the backup cache ----

// fileBackupDir is where copies of one path are kept.
//
// The file's own absolute path becomes a directory under the cache, so
// two files with the same basename do not share a history. The
// separator is kept rather than flattened, which means the tree under
// the cache mirrors the filesystem and an operator can find a backup
// with `ls`.
func fileBackupDir(c *exec.Context, path string) (string, error) {
	root := cacheDirOf(c)
	if root == "" {
		return "", fmt.Errorf("this node has no cache_dir, so it keeps no backups")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	// The volume name on Windows ends in a colon, which cannot be a
	// directory name on the same platform.
	abs = strings.Replace(abs, filepath.VolumeName(abs), "", 1)
	return filepath.Join(root, fileBackupDirName, filepath.FromSlash(abs)), nil
}

func cacheDirOf(c *exec.Context) string {
	if c.Config == nil {
		return ""
	}
	v, ok := c.Config.Get("cache_dir")
	if !ok {
		return ""
	}
	return value.KeyString(v)
}

// backupNow is the clock, replaceable so that a test can hold it still
// and exercise the collision path below deliberately.
var backupNow = time.Now

// keepBackup copies the current contents into the cache and answers with
// the identifier `list_backups` will report.
//
// # A timestamp is not a unique name
//
// `time.Now` is only as fine as the platform's clock, and on Windows
// that is about half a millisecond. Two backups taken inside one tick
// get the same name, and the second silently replaces the first --
// which is the whole point of a backup, lost. CI's Windows leg found
// exactly that: three keeps in a row produced two files.
//
// This is the *fourth* time this shape has been recorded here. The
// webhook returner's spool and the relay's spool both named files by a
// timestamp on the same reasoning, both lost returns for it (DIVERGENCE
// 4.9), and the concurrent-writer scenario in the chaos layer exists
// because of them. Knowing about a defect class is evidently not the
// same as not writing it again, so the name is now made unique rather
// than assumed to be: a collision takes a suffix, which keeps the
// lexical order chronological and leaves the older copy where it is.
func keepBackup(c *exec.Context, path string, contents []byte, mode os.FileMode) (string, error) {
	dir, err := fileBackupDir(c, path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	stamp := backupNow().UTC().Format(backupTimeFormat)
	for attempt := 1; ; attempt++ {
		id := stamp
		if attempt > 1 {
			id = fmt.Sprintf("%s-%d", stamp, attempt)
		}
		full := filepath.Join(dir, id)
		// O_EXCL is what makes this a claim rather than a hope: two
		// processes keeping a backup of the same file in the same tick
		// cannot both win.
		f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if os.IsExist(err) {
			if attempt > 1000 {
				return "", fmt.Errorf("a thousand backups of %s share one timestamp", path)
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(contents); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return id, nil
	}
}

func fileListBackupsFn(c *exec.Context, args *value.Map) (any, error) {
	path := states.Str(args, "path", "")
	dir, err := fileBackupDir(c, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		// No backups is an empty list rather than an error: "has this
		// ever been backed up" is a question a template asks.
		return []any{}, nil
	}
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// Newest first, which the format makes a reverse sort by name.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if limit := states.Int(args, "limit", 0); limit > 0 && int64(len(names)) > limit {
		names = names[:limit]
	}

	out := make([]any, 0, len(names))
	for _, name := range names {
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		m := value.NewMap(4)
		m.Set("id", name)
		m.Set("path", full)
		m.Set("size", info.Size())
		if when, err := time.Parse(backupTimeFormat, name); err == nil {
			m.Set("time", when.Format(time.RFC3339Nano))
		}
		out = append(out, m)
	}
	return out, nil
}

func fileRestoreBackupFn(c *exec.Context, args *value.Map) (any, error) {
	path := filepath.Clean(states.Str(args, "path", ""))
	id := states.Str(args, "backup_id", "")
	if id == "" {
		return nil, fmt.Errorf("file.restore_backup needs the `id` from file.list_backups")
	}
	// The identifier names a file in the cache and nothing else. A
	// separator in it would name a file outside, which is the traversal
	// this module exists to refuse.
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return nil, fmt.Errorf("%q is not a backup identifier", id)
	}
	dir, err := fileBackupDir(c, path)
	if err != nil {
		return nil, err
	}
	from := filepath.Join(dir, id)
	contents, err := os.ReadFile(from)
	if err != nil {
		return nil, fmt.Errorf("the backup %s of %s: %w", id, path, err)
	}

	out := value.NewMap(3)
	out.Set("restored", from)
	out.Set("path", path)
	if c.Test {
		c.Logf("info", "would restore %s from %s", path, from)
		return out, nil
	}

	// The current contents are kept before they are replaced, so that a
	// restore of the wrong backup is itself undoable. Doing it the
	// other way round is how somebody loses the only copy of the thing
	// they were trying to keep.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if current, err := os.ReadFile(path); err == nil {
			kept, err := keepBackup(c, path, current, mode)
			if err != nil {
				return nil, fmt.Errorf("keeping the current contents of %s: %w", path, err)
			}
			out.Set("kept", kept)
		}
	}
	if err := writeAtomic(path, contents, mode); err != nil {
		return nil, err
	}
	return out, nil
}
