package modules

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func init() {
	register("file.symlink", fileSymlink)
	register("file.copy", fileCopy)
}

// fileSymlink ensures a symbolic link points where it should.
//
//	/usr/local/etc/nginx/sites-enabled/site:
//	  file.symlink:
//	    - target: /usr/local/etc/nginx/sites-available/site
//	    - makedirs: true
//
// A link pointing somewhere else is repointed. A real file or directory in
// the way is an error unless `force: true` says to replace it — silently
// deleting something that was not a link is not a thing a config
// management run should do on its own.
func fileSymlink(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	target := Str(args, "target", "")
	if target == "" {
		return resFail("file.symlink needs a target")
	}
	if runtime.GOOS == "windows" {
		// Symlinks on Windows need a privilege most services do not hold,
		// and failing here is clearer than failing inside os.Symlink.
		return resFail("file.symlink is not implemented on Windows")
	}

	current, readErr := os.Readlink(name)
	isLink := readErr == nil
	_, statErr := os.Lstat(name)
	exists := statErr == nil

	switch {
	case isLink && current == target:
		if err := applyEditOwner(name, args); err != nil {
			return resFail("%v", err)
		}
		return resOK(fmt.Sprintf("%s already points at %s", name, target))
	case exists && !isLink && !Bool(args, "force", false):
		return resFail("%s exists and is not a symlink (use force: true to replace it)", name)
	}

	if c.Test {
		if exists {
			return resWould(fmt.Sprintf("%s would be repointed at %s", name, target))
		}
		return resWould(fmt.Sprintf("%s would be linked to %s", name, target))
	}
	if Bool(args, "makedirs", false) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return resFail("mkdir %s: %v", filepath.Dir(name), err)
		}
	}
	if exists {
		if err := os.RemoveAll(name); err != nil {
			return resFail("remove %s: %v", name, err)
		}
	}
	if err := os.Symlink(target, name); err != nil {
		return resFail("symlink %s: %v", name, err)
	}
	if err := applyLinkOwner(name, args); err != nil {
		return resFail("%v", err)
	}
	changes := map[string]string{name: "-> " + target}
	if isLink {
		changes[name] = current + " -> " + target
	}
	return resChanged(fmt.Sprintf("%s points at %s", name, target), changes)
}

// applyLinkOwner sets a link's own ownership, which is not the target's.
func applyLinkOwner(name string, args map[string]any) error {
	userName, groupName := Str(args, "user", ""), Str(args, "group", "")
	if userName == "" && groupName == "" {
		return nil
	}
	uid, gid, err := resolveOwner(userName, groupName)
	if err != nil {
		return err
	}
	if err := lchown(name, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", name, err)
	}
	return nil
}

// fileCopy copies a file already on the host to another path.
//
//	/etc/resolv.conf.orig:
//	  file.copy:
//	    - source: /etc/resolv.conf
//
// The source is a path on the managed host, not in the state tree — that
// is file.managed's `source`. This state is for keeping a copy of
// something the host itself produced.
func fileCopy(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	source := Str(args, "source", "")
	if source == "" {
		return resFail("file.copy needs a source")
	}
	info, err := os.Stat(source)
	if err != nil {
		return resFail("cannot read source %s: %v", source, err)
	}
	if info.IsDir() {
		return resFail("%s is a directory; file.recurse copies trees", source)
	}
	body, err := os.ReadFile(source)
	if err != nil {
		return resFail("read %s: %v", source, err)
	}

	current, readErr := os.ReadFile(name)
	exists := readErr == nil
	if exists && bytes.Equal(current, body) {
		return resOK(fmt.Sprintf("%s is already a copy of %s", name, source))
	}
	if exists && !Bool(args, "force", true) {
		return resFail("%s exists and force is false", name)
	}
	if c.Test {
		if exists {
			return resWould(fmt.Sprintf("%s would be replaced with a copy of %s", name, source))
		}
		return resWould(fmt.Sprintf("%s would be copied from %s", name, source))
	}
	if Bool(args, "makedirs", false) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return resFail("mkdir %s: %v", filepath.Dir(name), err)
		}
	}

	mode := info.Mode().Perm()
	if explicit, err := parseModeArg(args, "mode"); err != nil {
		return resFail("%v", err)
	} else if explicit != 0 {
		mode = explicit
	}
	if err := atomicWrite(name, body, mode); err != nil {
		return resFail("write %s: %v", name, err)
	}
	if Bool(args, "preserve", false) {
		if uid, gid, ok := statOwner(source); ok {
			if err := chown(name, uid, gid); err != nil {
				return resFail("chown %s: %v", name, err)
			}
		}
	}
	if err := applyEditOwner(name, args); err != nil {
		return resFail("%v", err)
	}
	changes := map[string]string{name: "copied from " + source}
	if exists && Bool(args, "show_diff", true) {
		if diff := lineDiff(current, body); diff != "" {
			changes["diff"] = diff
		}
	}
	return resChanged(fmt.Sprintf("%s copied from %s", name, source), changes)
}
