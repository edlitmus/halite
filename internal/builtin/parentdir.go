package builtin

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The directory a managed file is written into, when it is not there.
//
// A file state that wrote into a directory that did not exist failed in
// writeAtomic, and the error named the temporary file it had tried to
// create beside the target:
//
//	The key could not be written: writing /usr/local/etc/halite/pki/metrics.key:
//	open /usr/local/etc/halite/pki/.metrics.key.3126321956: no such file or directory
//
// The owner met it on a Linux node, where the path was FreeBSD's
// (DIVERGENCE 5.242). Nothing in it says the directory is what is
// missing, and the name it gives is one nobody wrote. Salt says "Parent
// directory not present", and its x509 states take `makedirs` and
// `dir_mode` and pass them to file.managed; halite's x509 states took
// neither. DIVERGENCE 5.246.

// missingParent reports the directory path would be written into, when
// it does not exist. Something other than a directory in its place is
// an error, which makedirs cannot fix either.
func missingParent(path string) (string, bool, error) {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return dir, true, nil
	case err != nil:
		return dir, false, err
	case !info.IsDir():
		return dir, false, fmt.Errorf("%s is not a directory", dir)
	}
	return dir, false, nil
}

// parentNotPresent is the failure for a missing directory and no
// makedirs: Salt's words, then which directory and what to do.
func parentNotPresent(dir string) states.Result {
	return states.False(fmt.Sprintf(
		"Parent directory not present: %s. Create it first, or set makedirs: true.", dir))
}

// dirModeFor is the mode for directories makedirs creates: dir_mode when
// it is given, and otherwise the file's mode with the execute bit added
// to every digit that is not zero -- Salt's rule in file.manage_file, so
// a key's 0600 makes a 0700 directory and a certificate's 0644 a 0755.
func dirModeFor(args *value.Map, fileMode os.FileMode) (os.FileMode, error) {
	if s := states.Str(args, "dir_mode", ""); s != "" {
		return parseMode(s)
	}
	m := fileMode.Perm()
	for _, digit := range []os.FileMode{0o700, 0o070, 0o007} {
		if m&digit != 0 {
			m |= digit & 0o111
		}
	}
	return m, nil
}

// makeParents creates dir and every missing directory above it, each
// with mode and owned as user and group say, as Salt's makedirs does.
// Each level goes through fileperm, so a private mode is private on
// Windows too.
func makeParents(dir string, mode os.FileMode, user, group string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := fileperm.MkdirAll(missing[i], mode); err != nil {
			return err
		}
		if err := applyOwnership(missing[i], user, group); err != nil {
			return fmt.Errorf("%s was created but its ownership could not be set: %w", missing[i], err)
		}
	}
	return nil
}

// prepareParent is what a file state calls before writing path. It
// returns a result to hand back when the write cannot go ahead, or nil;
// and, when makedirs will create the directory, a clause for the state's
// comment saying so. In test mode it creates nothing.
func prepareParent(test bool, args *value.Map, path string, fileMode os.FileMode) (*states.Result, string) {
	dir, missing, err := missingParent(path)
	if err != nil {
		r := states.False(fmt.Sprintf("%s cannot be written: %v.", path, err))
		return &r, ""
	}
	if !missing {
		return nil, ""
	}
	if !states.Bool(args, "makedirs", false) {
		r := parentNotPresent(dir)
		return &r, ""
	}
	mode, err := dirModeFor(args, fileMode)
	if err != nil {
		r := states.False(fmt.Sprintf("The dir_mode for %s is invalid: %v", path, err))
		return &r, ""
	}
	if test {
		return nil, fmt.Sprintf(" %s would be created, mode %04o.", dir, mode)
	}
	if err := makeParents(dir, mode, states.Str(args, "user", ""), states.Str(args, "group", "")); err != nil {
		r := states.False(fmt.Sprintf("The directory %s could not be created: %v", dir, err))
		return &r, ""
	}
	return nil, fmt.Sprintf(" %s was created, mode %04o.", dir, mode)
}
