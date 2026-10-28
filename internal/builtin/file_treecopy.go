package builtin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/fnmatch"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerFileRecurse adds `file.recurse`, which copies a subtree of the
// file server onto a node.
//
// Four references in a real estate's tree, and the largest of the state
// functions that were missing from a module this build already had.
// Every other way to place a directory of files means one `file.managed`
// per file, written by hand and kept in step by hand.
func registerFileTreeCopy(r *Registries) {
	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "file", Function: "recurse",
			Doc: "Copy a directory from the file server, creating what is " +
				"missing and replacing what differs.",
			Params: []signature.Param{
				nameParam("The directory on this node. Defaults to the state ID."),
				req("source", signature.String, "The `salt://` directory to copy."),
				opt("clean", signature.Bool, false,
					"Remove files under the destination that the source does not have."),
				opt("exclude_pat", signature.String, "",
					"Skip paths matching this glob, relative to the source."),
				opt("include_pat", signature.String, "",
					"Copy only paths matching this glob, relative to the source."),
				opt("dir_mode", signature.Mode, "", "Mode for directories this state creates."),
				opt("file_mode", signature.Mode, "", "Mode for the files it writes."),
				opt("user", signature.String, "", "Owner for what it writes."),
				opt("group", signature.String, "", "Group for what it writes."),
				opt("makedirs", signature.Bool, true, "Create the destination if it is absent."),
				opt("template", signature.String, "",
					"Render every file through this engine before writing it. Only jinja is supported."),
				opt("context", signature.Map, nil, "Names added to each template, overriding defaults."),
				opt("defaults", signature.Map, nil, "Names added to each template, overridden by context."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: fileRecurse,
	})
}

func fileRecurse(c *exec.Context, args *value.Map) (states.Result, error) {
	dest := states.Str(args, "name", "")
	source := states.Str(args, "source", "")
	if dest == "" || source == "" {
		return states.False("file.recurse needs a destination and a `source` to copy."), nil
	}

	lister, ok := c.Files.(exec.FileLister)
	if !ok || c.Files == nil {
		return states.False("This invocation cannot list a directory on the file " +
			"server, so there is nothing to recurse over. `file.recurse` needs a " +
			"tree, which a node running against a hub or its own roots has."), nil
	}

	paths, err := lister.ListUnder(c.Env, source)
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be listed: %v", source, err)), nil
	}
	// Each is joined to dest and written as root. The fileserver's
	// listers refuse a path that leaves the listing, and this refuses it
	// again, for any lister: a hub is not trusted to say where files go
	// (SPEC 25.6; issue 321, section 5).
	for _, rel := range paths {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return states.False(fmt.Sprintf(
				"%s lists %q, which is not a path inside %s; nothing was written.", source, rel, dest)), nil
		}
	}
	paths = filterTreePaths(paths, states.Str(args, "include_pat", ""),
		states.Str(args, "exclude_pat", ""))
	if len(paths) == 0 {
		return states.False(fmt.Sprintf(
			"%s lists no files. An empty source is more often a wrong path than an "+
				"empty directory, so this is reported rather than passed.", source)), nil
	}

	plan, err := planTreeCopy(c, args, dest, source, paths)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if states.Bool(args, "clean", false) {
		extra, err := extraUnderTree(dest, paths, states.Str(args, "exclude_pat", ""))
		if err != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", dest, err)), nil
		}
		plan.remove = extra
	}

	if plan.empty() {
		return states.True(fmt.Sprintf("%s already matches %s (%d files).",
			dest, source, len(paths))), nil
	}

	changes := plan.changes()
	if c.Test {
		return states.WouldChange(plan.describe(dest, source, true), changes), nil
	}
	if err := plan.apply(args, dest); err != nil {
		return states.False(fmt.Sprintf("%s could not be written: %v", dest, err)), nil
	}
	return states.Changed(plan.describe(dest, source, false), changes), nil
}

// treeCopyPlan is what a run would do, worked out before it does any of
// it so that test mode and the real thing answer from the same decision.
type treeCopyPlan struct {
	write  []treeCopyFile
	remove []string
	// fix is files whose contents match and whose mode or owner does not
	// match file_mode, user or group. See planTreeCopy.
	fix []string
}

type treeCopyFile struct {
	rel string
	// body is what will be written: the source file, rendered if the
	// state names a template engine. It is carried from planning rather
	// than re-read at write time so that the comparison deciding whether
	// to write and the bytes actually written are the same thing.
	body  []byte
	isNew bool
}

func (p *treeCopyPlan) empty() bool {
	return len(p.write) == 0 && len(p.remove) == 0 && len(p.fix) == 0
}

func (p *treeCopyPlan) changes() *value.Map {
	changes := value.NewMap(len(p.write) + len(p.remove))
	for _, f := range p.write {
		was := "differs"
		if f.isNew {
			was = "absent"
		}
		changes.Set(f.rel, states.Change(was, "copied"))
	}
	for _, rel := range p.remove {
		changes.Set(rel, states.Change("present", "removed"))
	}
	for _, rel := range p.fix {
		changes.Set(rel, states.Change("mode or owner differs", "corrected"))
	}
	return changes
}

func (p *treeCopyPlan) describe(dest, source string, would bool) string {
	verb := map[bool]string{true: "would be", false: "were"}[would]
	parts := []string{}
	if n := len(p.write); n > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) %s copied from %s", n, verb, source))
	}
	if n := len(p.remove); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s removed as `clean`", n, verb))
	}
	if n := len(p.fix); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s given the requested mode or owner", n, verb))
	}
	return strings.Join(parts, ", ") + " under " + dest + "."
}

// planTreeCopy decides what has to be written, comparing each source file
// with what is already on the node.
func planTreeCopy(c *exec.Context, args *value.Map, dest, source string, paths []string) (*treeCopyPlan, error) {
	plan := &treeCopyPlan{}
	for _, rel := range paths {
		uri := strings.TrimSuffix(source, "/") + "/" + rel
		local, err := c.Files.Fetch(c.Env, uri)
		if err != nil {
			return nil, fmt.Errorf("%s could not be fetched: %w", uri, err)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))

		body, err := os.ReadFile(local)
		if err != nil {
			return nil, err
		}
		// Rendered before the comparison, not after it. A template
		// compared unrendered differs from its own output on every run,
		// so the state would rewrite the file forever and never converge.
		body, err = renderSourceTemplate(c, args, body, uri)
		if err != nil {
			return nil, fmt.Errorf("the template for %s failed: %w", uri, err)
		}

		same, err := sameContents(body, target)
		if err != nil {
			return nil, err
		}
		if same {
			// **The contents match; the mode and owner may not.** They
			// were applied only to files this run rewrote, so changing
			// `file_mode` on a tree already copied reported "already
			// matches" and left every file as it was (issue 321, M-22).
			fix, err := treeFileNeedsFix(args, target)
			if err != nil {
				return nil, err
			}
			if fix {
				plan.fix = append(plan.fix, rel)
			}
			continue
		}
		_, statErr := os.Lstat(target)
		plan.write = append(plan.write, treeCopyFile{
			rel: rel, body: body, isNew: os.IsNotExist(statErr),
		})
	}
	return plan, nil
}

// treeFileNeedsFix reports whether a file whose contents already match
// differs from the file_mode, user or group the state names. Nothing named
// is nothing to fix.
func treeFileNeedsFix(args *value.Map, target string) (bool, error) {
	info, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	if spec := states.Str(args, "file_mode", ""); spec != "" {
		want, err := modeOrDefault(spec, 0)
		if err != nil {
			return false, err
		}
		if differs, _, _ := plannedMode(target, info, want); differs {
			return true, nil
		}
	}
	_, differs, err := plannedOwnership(target, true, states.Str(args, "user", ""), states.Str(args, "group", ""))
	return differs, err
}

// sameContents compares bytes already in hand with a file on disk.
func sameContents(body []byte, target string) (bool, error) {
	have, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(have, body), nil
}

// apply writes the plan.
func (p *treeCopyPlan) apply(args *value.Map, dest string) error {
	dirMode, err := modeOrDefault(states.Str(args, "dir_mode", ""), 0o755)
	if err != nil {
		return err
	}
	fileMode, err := modeOrDefault(states.Str(args, "file_mode", ""), 0o644)
	if err != nil {
		return err
	}

	// fileperm.MkdirAll sets a directory it creates to dirMode exactly,
	// where os.MkdirAll left it less the umask (as M-31 found in
	// file.directory).
	if states.Bool(args, "makedirs", true) {
		if err := fileperm.MkdirAll(dest, dirMode); err != nil {
			return err
		}
	}
	modeNamed := states.Str(args, "file_mode", "") != ""
	for _, f := range p.write {
		target := filepath.Join(dest, filepath.FromSlash(f.rel))
		if err := fileperm.MkdirAll(filepath.Dir(target), dirMode); err != nil {
			return err
		}
		// An existing file rewritten with no file_mode keeps its own, as
		// file.managed's does; it was forced to 0644.
		mode := fileMode
		if info, err := os.Stat(target); err == nil && !modeNamed {
			mode = info.Mode().Perm()
		}
		if err := writeAtomic(target, f.body, mode); err != nil {
			return err
		}
		if err := applyOwnership(target, states.Str(args, "user", ""),
			states.Str(args, "group", "")); err != nil {
			return err
		}
	}
	for _, rel := range p.remove {
		if err := os.RemoveAll(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	for _, rel := range p.fix {
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if modeNamed {
			if err := applyMode(target, fileMode); err != nil {
				return err
			}
		}
		if err := applyOwnership(target, states.Str(args, "user", ""),
			states.Str(args, "group", "")); err != nil {
			return err
		}
	}
	return nil
}

// extraUnderTree lists what is under a destination that the source does not
// have, which is what `clean` removes.
//
// A file matching exclude_pat is not the state's to remove: Salt's clean
// keeps it, and this deleted it, so `exclude_pat: '*.override'` with
// `clean: True` removed the local override it was written to protect
// (issue 321, M-22).
func extraUnderTree(dest string, want []string, exclude string) ([]string, error) {
	keep := make(map[string]bool, len(want))
	for _, rel := range want {
		keep[filepath.FromSlash(rel)] = true
	}
	var extra []string
	err := filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dest, path)
		if relErr != nil {
			return relErr
		}
		if !keep[rel] && !(exclude != "" && fnmatch.Match(exclude, filepath.ToSlash(rel))) {
			extra = append(extra, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(extra)
	return extra, nil
}

// filterTreePaths applies the include and exclude globs, which are
// matched against the path relative to the source.
//
// With Salt's fnmatch, where `*` crosses `/`, rather than path.Match,
// where it stops: `exclude_pat: '*.bak'` excluded `x.bak` and copied
// `sub/x.bak` (issue 321, M-22). The same correction file_ignore_glob had
// in DIVERGENCE 5.295.
func filterTreePaths(paths []string, include, exclude string) []string {
	out := make([]string, 0, len(paths))
	for _, rel := range paths {
		if include != "" && !fnmatch.Match(include, rel) {
			continue
		}
		if exclude != "" && fnmatch.Match(exclude, rel) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

func modeOrDefault(spec string, def os.FileMode) (os.FileMode, error) {
	if spec == "" {
		return def, nil
	}
	// A mode that does not parse is an error, not the default. This
	// returned the default, so `file_mode: 0600` -- which the YAML parser
	// had made the integer 384, and a String parameter then the text
	// "384" -- quietly became 0644 (issue 321, M-21). The parameters are
	// typed Mode now, which refuses the integer with a message saying to
	// quote it; this is the second guard.
	m, err := parseMode(spec)
	if err != nil {
		return 0, fmt.Errorf("the mode %q is not one: %w", spec, err)
	}
	return m, nil
}
