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

// planRecurse walks the directory and returns what would change.
func planRecurse(c *exec.Context, root string, f recurseFlags, args *value.Map) ([]recurseTarget, error) {
	if !f.any {
		return nil, nil
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
				return nil, fmt.Errorf("dir_mode: %w", err)
			}
			dirMode, haveDirMode = m, true
		}
		if s := states.Str(args, "file_mode", ""); s != "" {
			m, err := parseMode(s)
			if err != nil {
				return nil, fmt.Errorf("file_mode: %w", err)
			}
			fileMode, haveFileMode = m, true
		}
		if !haveDirMode && !haveFileMode {
			return nil, fmt.Errorf("recurse names mode, but neither dir_mode nor file_mode says what mode to set")
		}
	}

	maxDepth := int(states.Int(args, "max_depth", -1))
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

	var out []recurseTarget
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
			if have && info.Mode().Perm() != want.Perm() {
				t.mode, t.setMode = want, true
				t.from, t.to = formatMode(info.Mode()), formatMode(want)
			}
		}
		if wantUser != "" || wantGroup != "" {
			_, differs, err := plannedOwnership(path, true, wantUser, wantGroup)
			if err != nil {
				return err
			}
			t.setOwner = differs
		}
		if t.setMode || t.setOwner {
			out = append(out, t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// applyRecurse makes the planned changes.
func applyRecurse(targets []recurseTarget, args *value.Map, f recurseFlags) error {
	wantUser, wantGroup := states.Str(args, "user", ""), states.Str(args, "group", "")
	if !f.user {
		wantUser = ""
	}
	if !f.group {
		wantGroup = ""
	}
	for _, t := range targets {
		if t.setMode {
			if err := os.Chmod(t.path, t.mode); err != nil {
				return err
			}
		}
		if t.setOwner {
			if err := applyOwnership(t.path, wantUser, wantGroup); err != nil {
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
