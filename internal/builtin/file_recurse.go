package builtin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `file.directory` may enforce ownership and mode over everything under
// the directory rather than the directory alone. Salt spells it as a
// list naming what to enforce:
//
//	- recurse:
//	    - user
//	    - mode
//
// with `dir_mode` for the directories it finds and `file_mode` for the
// files. Naming `mode` without saying which mode to give a file is the
// mistake the two options exist to prevent, so a directory mode is never
// applied to a file here.

// recurseFlags is what a `recurse` list asked for.
type recurseFlags struct {
	user        bool
	group       bool
	mode        bool
	ignoreFiles bool
	ignoreDirs  bool
	any         bool
}

func parseRecurse(args *value.Map) (recurseFlags, error) {
	var f recurseFlags
	v, ok := args.Get("recurse")
	if !ok || v == nil {
		return f, nil
	}
	items, ok := v.([]any)
	if !ok {
		return f, fmt.Errorf("recurse must hold a list of what to enforce, found %s", value.TypeName(v))
	}
	for _, item := range items {
		switch strings.ToLower(value.KeyString(item)) {
		case "user":
			f.user, f.any = true, true
		case "group":
			f.group, f.any = true, true
		case "mode":
			f.mode, f.any = true, true
		case "silent":
			// Salt uses this to suppress the per-path changes. They are
			// summarised here in any case, so it asks for nothing extra.
			f.any = true
		case "ignore_files":
			f.ignoreFiles, f.any = true, true
		case "ignore_dirs":
			f.ignoreDirs, f.any = true, true
		default:
			return f, fmt.Errorf("recurse does not take %q; it takes user, group, mode, silent, "+
				"ignore_files, and ignore_dirs", value.KeyString(item))
		}
	}
	if f.ignoreFiles && f.ignoreDirs {
		return f, fmt.Errorf("recurse names both ignore_files and ignore_dirs, which leaves nothing to act on")
	}
	return f, nil
}

// recurseTarget is one path the walk would change.
type recurseTarget struct {
	path string
	// mode is the mode to set, or zero to leave it alone.
	mode     os.FileMode
	setMode  bool
	setOwner bool
	from     string
	to       string
}

// planRecurse walks the directory and returns what would change, and a
// warning for each path it will not change for what the path is: a socket,
// a FIFO or a device in a directory another account can write.
//
// Each change is checked here by the same code that will make it, so that
// test mode refuses a planted hard link with the words the run would use,
// and does not report as a change a path the run would skip.
func planRecurse(c *exec.Context, root string, f recurseFlags, args *value.Map) ([]recurseTarget, []string, error) {
	if !f.any {
		return nil, nil, nil
	}
	wantUser, wantGroup := states.Str(args, "user", ""), states.Str(args, "group", "")
	if !f.user {
		wantUser = ""
	}
	if !f.group {
		wantGroup = ""
	}

	var dirMode, fileMode os.FileMode
	var haveDirMode, haveFileMode bool
	if f.mode {
		// A recursed directory takes dir_mode, or the mode the state
		// gave the directory itself when there is no dir_mode. Salt
		// reads it the same way, and the alternative is a state that
		// says `mode` and `recurse: [mode]` and enforces neither.
		dirSpec := states.Str(args, "dir_mode", "")
		if dirSpec == "" {
			dirSpec = states.Str(args, "mode", "")
		}
		if dirSpec != "" {
			m, err := parseMode(dirSpec)
			if err != nil {
				return nil, nil, fmt.Errorf("dir_mode: %w", err)
			}
			dirMode, haveDirMode = m, true
		}
		if s := states.Str(args, "file_mode", ""); s != "" {
			m, err := parseMode(s)
			if err != nil {
				return nil, nil, fmt.Errorf("file_mode: %w", err)
			}
			fileMode, haveFileMode = m, true
		}
		if !haveDirMode && !haveFileMode {
			return nil, nil, fmt.Errorf("recurse names mode, but neither dir_mode nor file_mode says what mode to set")
		}
	}

	maxDepth := int(states.Int(args, "max_depth", -1))
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

	var out []recurseTarget
	var skipped []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if maxDepth >= 0 {
			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
			if depth > maxDepth {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		// A symlink is left alone: following one would take the walk
		// outside the directory being managed, and chmod through one
		// changes the target rather than the link.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && f.ignoreDirs {
			return nil
		}
		if !d.IsDir() && f.ignoreFiles {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		t := recurseTarget{path: path}
		if f.mode {
			want, have := fileMode, haveFileMode
			if d.IsDir() {
				want, have = dirMode, haveDirMode
			}
			// Asked of the platform rather than compared as a number,
			// for the reason plannedMode gives: a mode is no answer at
			// all on Windows, and comparing one there put every path
			// under the directory in the plan on every run.
			if have {
				differs, _, _ := plannedMode(path, info, want)
				if differs {
					t.mode, t.setMode = want, true
					t.from, t.to = formatMode(info.Mode()), formatMode(want)
				}
			}
		}
		if wantUser != "" || wantGroup != "" {
			_, differs, err := plannedOwnership(path, true, wantUser, wantGroup)
			if err != nil {
				return err
			}
			t.setOwner = differs
		}
		if !t.setMode && !t.setOwner {
			return nil
		}
		if err := checkRecurseTarget(t, wantUser, wantGroup); err != nil {
			if why, ok := skippedEntry(err); ok {
				skipped = append(skipped, why)
				return nil
			}
			return err
		}
		out = append(out, t)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, skipped, nil
}

// applyRecurse makes the planned changes, inside dir.
//
// **Through an os.Root, not by path** (issue 321, M-29). The paths were
// found by the plan and changed afterwards with os.Chmod and os.Lchown by
// full path. Between the two, a user who owns a file in the tree could
// swap it for a symbolic link to /etc/shadow, and os.Chmod, which follows
// links, set the mode on the target as root; or swap a parent directory
// for a link, which redirected even Lchown, because Lchown protects only
// the last component. An os.Root resolves every component inside dir and
// refuses to leave it. A path that is a link by the time it is reached is
// skipped, as the plan skipped links.
//
// That was written as "the worst a swap can do is redirect a change to
// another path in the tree", and it was not true: a **hard link** is not
// followed, so the root does not see it leave, and a file in the tree can
// be a second name for /etc/spwd.db. changeEntryIn (through
// applyModeIn and applyOwnershipIn) makes the change on an opened, checked
// descriptor and refuses a hard link in a directory another account can
// write. DIVERGENCE 5.558.
func applyRecurse(dir string, targets []recurseTarget, args *value.Map, f recurseFlags) error {
	wantUser, wantGroup := states.Str(args, "user", ""), states.Str(args, "group", "")
	if !f.user {
		wantUser = ""
	}
	if !f.group {
		wantGroup = ""
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, t := range targets {
		rel, err := filepath.Rel(dir, t.path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("%s is not under %s", t.path, dir)
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if t.setMode {
			if err := applyModeIn(root, dir, rel, t.mode); err != nil {
				if _, ok := skippedEntry(err); ok {
					continue
				}
				return err
			}
		}
		if t.setOwner {
			if err := applyOwnershipIn(root, rel, wantUser, wantGroup); err != nil {
				if _, ok := skippedEntry(err); ok {
					continue
				}
				return err
			}
		}
	}
	return nil
}

// recurseChanges summarises the walk for the result.
//
// A recursive state can touch thousands of paths, and a job return
// carrying one entry each is a return nobody reads and a returner nobody
// thanks. The first few are named and the rest counted.
const recurseChangesShown = 10

func recurseChanges(targets []recurseTarget) *value.Map {
	m := value.NewMap(len(targets) + 1)
	shown := targets
	if len(shown) > recurseChangesShown {
		shown = shown[:recurseChangesShown]
	}
	for _, t := range shown {
		switch {
		case t.setMode && t.setOwner:
			m.Set(t.path, states.Change(t.from+", ownership", t.to+", ownership"))
		case t.setMode:
			m.Set(t.path, states.Change(t.from, t.to))
		default:
			m.Set(t.path, states.Change("ownership", "ownership"))
		}
	}
	if len(targets) > len(shown) {
		m.Set("recurse", states.Change(
			fmt.Sprintf("%d paths", len(targets)),
			fmt.Sprintf("%d paths, %d of them named above", len(targets), len(shown))))
	}
	return m
}
