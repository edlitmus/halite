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
				sls.TemplateData{Grains: c.Grains, Pillar: c.Pillar})
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
		if wantMode != 0 {
			perm = wantMode
		}
		if err := os.WriteFile(name, desired, perm); err != nil {
			return resFail("write %s: %v", name, err)
		}
		if !exists {
			changes["content"] = fmt.Sprintf("created (%d bytes)", len(desired))
		} else {
			changes["content"] = fmt.Sprintf("updated (%d -> %d bytes)", len(current), len(desired))
		}
	}
	if needMode {
		if err := os.Chmod(name, wantMode); err != nil {
			return resFail("chmod %s: %v", name, err)
		}
		changes["mode"] = mode
	}
	if needOwner {
		if err := chown(name, wantUID, wantGID); err != nil {
			return resFail("chown %s: %v", name, err)
		}
		changes["owner"] = fmt.Sprintf("uid=%d gid=%d", wantUID, wantGID)
	}
	return resChanged(fmt.Sprintf("file %s updated", name), changes)
}

func fileDirectory(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	mode := Str(args, "mode", "")
	wantUID, wantGID, ownerErr := resolveOwner(Str(args, "user", ""), Str(args, "group", ""))
	if ownerErr != nil && !c.Test {
		return resFail("%v", ownerErr)
	}
	ownerPending := ownerErr != nil

	st, statErr := os.Stat(name)
	exists := statErr == nil && st.IsDir()
	needOwner := ownerPending || (exists && ownerDrift(name, wantUID, wantGID))

	if exists && !needOwner {
		return resOK(fmt.Sprintf("directory %s exists", name))
	}
	if c.Test {
		if !exists {
			return resWould(fmt.Sprintf("directory %s would be created", name))
		}
		return resWould(fmt.Sprintf("directory %s ownership would be updated", name))
	}
	changes := map[string]string{}
	if !exists {
		perm := os.FileMode(0o755)
		if mode != "" && runtime.GOOS != "windows" {
			if n, err := strconv.ParseUint(mode, 8, 32); err == nil {
				perm = os.FileMode(n)
			}
		}
		if err := os.MkdirAll(name, perm); err != nil {
			return resFail("mkdir %s: %v", name, err)
		}
		changes["directory"] = "created"
	}
	if wantUID >= 0 || wantGID >= 0 {
		if !exists || needOwner {
			if err := chown(name, wantUID, wantGID); err != nil {
				return resFail("chown %s: %v", name, err)
			}
			changes["owner"] = fmt.Sprintf("uid=%d gid=%d", wantUID, wantGID)
		}
	}
	return resChanged(fmt.Sprintf("directory %s updated", name), changes)
}

func fileAbsent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	if _, err := os.Stat(name); os.IsNotExist(err) {
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
