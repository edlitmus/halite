package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
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
				opt("dir_mode", signature.String, "", "Mode for directories this state creates."),
				opt("file_mode", signature.String, "", "Mode for the files it writes."),
				opt("user", signature.String, "", "Owner for what it writes."),
				opt("group", signature.String, "", "Group for what it writes."),
				opt("makedirs", signature.Bool, true, "Create the destination if it is absent."),
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
	paths = filterTreePaths(paths, states.Str(args, "include_pat", ""),
		states.Str(args, "exclude_pat", ""))
	if len(paths) == 0 {
		return states.False(fmt.Sprintf(
			"%s lists no files. An empty source is more often a wrong path than an "+
				"empty directory, so this is reported rather than passed.", source)), nil
	}

	plan, err := planTreeCopy(c, dest, source, paths)
	if err != nil {
		return states.False(err.Error()), nil
	}
	if states.Bool(args, "clean", false) {
		extra, err := extraUnderTree(dest, paths)
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
}

type treeCopyFile struct {
	rel   string
	local string
	isNew bool
}

func (p *treeCopyPlan) empty() bool { return len(p.write) == 0 && len(p.remove) == 0 }

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
	return strings.Join(parts, ", ") + " under " + dest + "."
}

// planTreeCopy decides what has to be written, comparing each source file
// with what is already on the node.
func planTreeCopy(c *exec.Context, dest, source string, paths []string) (*treeCopyPlan, error) {
	plan := &treeCopyPlan{}
	for _, rel := range paths {
		uri := strings.TrimSuffix(source, "/") + "/" + rel
		local, err := c.Files.Fetch(c.Env, uri)
		if err != nil {
			return nil, fmt.Errorf("%s could not be fetched: %w", uri, err)
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))

		same, err := sameFileContents(local, target)
		if err != nil {
			return nil, err
		}
		if same {
			continue
		}
		_, statErr := os.Lstat(target)
		plan.write = append(plan.write, treeCopyFile{
			rel: rel, local: local, isNew: os.IsNotExist(statErr),
		})
	}
	return plan, nil
}

// apply writes the plan.
func (p *treeCopyPlan) apply(args *value.Map, dest string) error {
	dirMode := modeOrDefault(states.Str(args, "dir_mode", ""), 0o755)
	fileMode := modeOrDefault(states.Str(args, "file_mode", ""), 0o644)

	if states.Bool(args, "makedirs", true) {
		if err := os.MkdirAll(dest, dirMode); err != nil {
			return err
		}
	}
	for _, f := range p.write {
		target := filepath.Join(dest, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(target), dirMode); err != nil {
			return err
		}
		body, err := os.ReadFile(f.local)
		if err != nil {
			return err
		}
		if err := writeAtomic(target, body, fileMode); err != nil {
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
	return nil
}

// extraUnderTree lists what is under a destination that the source does not
// have, which is what `clean` removes.
func extraUnderTree(dest string, want []string) ([]string, error) {
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
		if !keep[rel] {
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
func filterTreePaths(paths []string, include, exclude string) []string {
	out := make([]string, 0, len(paths))
	for _, rel := range paths {
		if include != "" {
			if ok, _ := filepath.Match(include, rel); !ok {
				continue
			}
		}
		if exclude != "" {
			if ok, _ := filepath.Match(exclude, rel); ok {
				continue
			}
		}
		out = append(out, rel)
	}
	return out
}

// sameFileContents reports whether the destination already holds what
// the source has. A missing destination is not the same.
func sameFileContents(source, target string) (bool, error) {
	want, err := os.ReadFile(source)
	if err != nil {
		return false, err
	}
	have, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, nil
	}
	return string(want) == string(have), nil
}

func modeOrDefault(spec string, def os.FileMode) os.FileMode {
	if spec == "" {
		return def
	}
	m, err := parseMode(spec)
	if err != nil {
		return def
	}
	return m
}
