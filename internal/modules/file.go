package modules

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/edlitmus/halite/internal/sls"
)

func init() {
	register("file.managed", fileManaged)
	register("file.directory", fileDirectory)
	register("file.absent", fileAbsent)
}

// atomicWrite replaces path via a temp file in the same directory: the data
// is written, the requested mode applied, and the file fsynced before the
// rename, so a crash never leaves a truncated file and a tightened mode is
// in force before the content is reachable.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(e error) error {
		tmp.Close()
		os.Remove(name)
		return e
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// FollowSymlinksArg is the argument that lets a state change the mode or
// ownership of what a symlink points at.
const FollowSymlinksArg = "follow_symlinks"

// setMode applies a mode, refusing to do it through a symlink.
func setMode(path string, mode os.FileMode, follow bool) error {
	if err := refuseSymlink(path, follow); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// setOwner applies ownership, refusing to do it through a symlink.
func setOwner(path string, uid, gid int, follow bool) error {
	if err := refuseSymlink(path, follow); err != nil {
		return err
	}
	return chown(path, uid, gid)
}

// refuseSymlink stops a mode or ownership change from landing on whatever
// a link points at. Writing *content* is safe without this — the write
// goes to a temp file and the rename replaces the link, leaving its
// target alone — but chmod and chown follow, so a path an unprivileged
// user can pre-create is otherwise a way to have a root state widen or
// take ownership of any file on the host.
//
// A state that means it says follow_symlinks: true.
func refuseSymlink(path string, follow bool) error {
	if follow {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		target = "its target"
	}
	return fmt.Errorf("%s is a symlink to %s: refusing to change the mode or owner of a link's target (set %s: true to allow it)",
		path, target, FollowSymlinksArg)
}

// resolveOwner turns user/group names (or numeric IDs) into uid/gid.
// Returns -1 for unspecified fields.
func resolveOwner(userName, groupName string) (uid, gid int, err error) {
	uid, gid = -1, -1
	if runtime.GOOS == "windows" {
		return
	}
	if userName != "" {
		if n, e := strconv.Atoi(userName); e == nil {
			uid = n
		} else {
			u, e := user.Lookup(userName)
			if e != nil {
				return uid, gid, fmt.Errorf("user %q: %v", userName, e)
			}
			uid, _ = strconv.Atoi(u.Uid)
		}
	}
	if groupName != "" {
		if n, e := strconv.Atoi(groupName); e == nil {
			gid = n
		} else {
			g, e := user.LookupGroup(groupName)
			if e != nil {
				return uid, gid, fmt.Errorf("group %q: %v", groupName, e)
			}
			gid, _ = strconv.Atoi(g.Gid)
		}
	}
	return
}

func ownerDrift(path string, wantUID, wantGID int) bool {
	if wantUID < 0 && wantGID < 0 {
		return false
	}
	cuid, cgid, ok := statOwner(path)
	if !ok {
		return false
	}
	return (wantUID >= 0 && cuid != wantUID) || (wantGID >= 0 && cgid != wantGID)
}

// fileManaged ensures a file exists with the given contents/source, mode,
// and ownership.
//
//	/usr/local/etc/app.conf:
//	  file.managed:
//	    - source: files/app.conf
//	    - mode: "0640"
//	    - user: www
//	    - group: www
//	    - makedirs: true
func fileManaged(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	contents := Str(args, "contents", "")
	source := Str(args, "source", "")
	mode := Str(args, "mode", "")
	makedirs := Bool(args, "makedirs", false)
	showDiff := Bool(args, "show_diff", true)
	follow := Bool(args, FollowSymlinksArg, false)

	wantUID, wantGID, ownerErr := resolveOwner(Str(args, "user", ""), Str(args, "group", ""))
	if ownerErr != nil && !c.Test {
		return resFail("%v", ownerErr)
	}
	// In test mode a referenced user/group may not exist yet (it would be
	// created by an earlier state); defer resolution instead of failing.
	ownerPending := ownerErr != nil

	var desired []byte
	haveDesired := false
	switch {
	case contents != "":
		desired = []byte(contents)
		if !bytes.HasSuffix(desired, []byte("\n")) {
			desired = append(desired, '\n')
		}
		haveDesired = true
	case source != "":
		src := source
		if !filepath.IsAbs(src) && c.BaseDir != "" {
			src = filepath.Join(c.BaseDir, src)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return resFail("cannot read source %s: %v", src, err)
		}
		if tpl := Str(args, "template", ""); tpl == "true" || tpl == "go" {
			rendered, err := sls.Render(filepath.Base(src), string(b),
				sls.TemplateData{Grains: c.Grains, Pillar: c.Pillar, Mine: c.Mine})
			if err != nil {
				return resFail("render source %s: %v", src, err)
			}
			b = []byte(rendered)
		}
		desired = b
		haveDesired = true
	}

	current, readErr := os.ReadFile(name)
	exists := readErr == nil

	needContent := haveDesired && (!exists || !bytes.Equal(current, desired))
	needCreate := !exists && !haveDesired

	var wantMode os.FileMode
	needMode := false
	if mode != "" && runtime.GOOS != "windows" {
		n, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			return resFail("invalid mode %q: %v", mode, err)
		}
		wantMode = os.FileMode(n)
		if exists {
			if st, err := os.Stat(name); err == nil && st.Mode().Perm() != wantMode {
				needMode = true
			}
		} else {
			needMode = true
		}
	}

	needOwner := ownerPending ||
		(!exists && (wantUID >= 0 || wantGID >= 0)) ||
		(exists && ownerDrift(name, wantUID, wantGID))

	if !needContent && !needCreate && !needMode && !needOwner {
		return resOK(fmt.Sprintf("file %s is in the correct state", name))
	}

	changes := map[string]string{}
	if needContent && showDiff {
		changes["diff"] = lineDiff(current, desired)
	}

	if c.Test {
		if needContent || needCreate {
			changes["content"] = "would be updated"
		}
		if needMode {
			changes["mode"] = "would be set to " + mode
		}
		if needOwner {
			if ownerPending {
				changes["owner"] = "would be set (owner not yet present)"
			} else {
				changes["owner"] = "would be updated"
			}
		}
		return Result{Ok: true, Changed: true, Comment: fmt.Sprintf("file %s would be updated", name), Changes: changes}
	}

	if makedirs {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return resFail("makedirs: %v", err)
		}
	}
	if needContent || needCreate {
		if !haveDesired {
			desired = []byte{}
		}
		perm := os.FileMode(0o644)
		switch {
		case wantMode != 0:
			perm = wantMode
		case exists:
			// No mode requested: the atomic rename must not reset an
			// existing file's permissions to the default.
			if st, err := os.Stat(name); err == nil {
				perm = st.Mode().Perm()
			}
		}
		if err := atomicWrite(name, desired, perm); err != nil {
			return resFail("write %s: %v", name, err)
		}
		// The rename resets ownership to this process; put back an owner
		// the caller pinned but that was not otherwise drifting.
		if !needOwner && (wantUID >= 0 || wantGID >= 0) {
			if err := setOwner(name, wantUID, wantGID, follow); err != nil {
				return resFail("chown %s: %v", name, err)
			}
		}
		if !exists {
			changes["content"] = fmt.Sprintf("created (%d bytes)", len(desired))
		} else {
			changes["content"] = fmt.Sprintf("updated (%d -> %d bytes)", len(current), len(desired))
		}
	}
	if needMode {
		if err := setMode(name, wantMode, follow); err != nil {
			return resFail("%v", err)
		}
		changes["mode"] = mode
	}
	if needOwner {
		if err := setOwner(name, wantUID, wantGID, follow); err != nil {
			return resFail("%v", err)
		}
		changes["owner"] = fmt.Sprintf("uid=%d gid=%d", wantUID, wantGID)
	}
	return resChanged(fmt.Sprintf("file %s updated", name), changes)
}

func fileDirectory(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	mode := Str(args, "mode", "")
	follow := Bool(args, FollowSymlinksArg, false)
	wantUID, wantGID, ownerErr := resolveOwner(Str(args, "user", ""), Str(args, "group", ""))
	if ownerErr != nil && !c.Test {
		return resFail("%v", ownerErr)
	}
	ownerPending := ownerErr != nil

	var wantMode os.FileMode
	haveMode := false
	if mode != "" && runtime.GOOS != "windows" {
		n, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			return resFail("invalid mode %q: %v", mode, err)
		}
		wantMode = os.FileMode(n)
		haveMode = true
	}

	st, statErr := os.Stat(name)
	exists := statErr == nil && st.IsDir()
	needMode := haveMode && exists && st.Mode().Perm() != wantMode
	needOwner := ownerPending || (exists && ownerDrift(name, wantUID, wantGID))

	if exists && !needMode && !needOwner {
		return resOK(fmt.Sprintf("directory %s exists", name))
	}
	if c.Test {
		if !exists {
			return resWould(fmt.Sprintf("directory %s would be created", name))
		}
		if needMode {
			return resWould(fmt.Sprintf("directory %s mode would be set to %s", name, mode))
		}
		return resWould(fmt.Sprintf("directory %s ownership would be updated", name))
	}
	changes := map[string]string{}
	if !exists {
		perm := os.FileMode(0o755)
		if haveMode {
			perm = wantMode
		}
		if err := os.MkdirAll(name, perm); err != nil {
			return resFail("mkdir %s: %v", name, err)
		}
		if haveMode {
			// MkdirAll's mode is filtered by the umask; assert the real one.
			if err := setMode(name, wantMode, follow); err != nil {
				return resFail("%v", err)
			}
		}
		changes["directory"] = "created"
	}
	if needMode {
		if err := setMode(name, wantMode, follow); err != nil {
			return resFail("%v", err)
		}
		changes["mode"] = mode
	}
	if wantUID >= 0 || wantGID >= 0 {
		if !exists || needOwner {
			if err := setOwner(name, wantUID, wantGID, follow); err != nil {
				return resFail("%v", err)
			}
			changes["owner"] = fmt.Sprintf("uid=%d gid=%d", wantUID, wantGID)
		}
	}
	return resChanged(fmt.Sprintf("directory %s updated", name), changes)
}

func fileAbsent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	// Lstat, not Stat: a dangling symlink is still an entry to remove.
	if _, err := os.Lstat(name); os.IsNotExist(err) {
		return resOK(fmt.Sprintf("%s is already absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would be removed", name))
	}
	if err := os.RemoveAll(name); err != nil {
		return resFail("remove %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("%s removed", name), map[string]string{"removed": name})
}
