//go:build unix

package builtin

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// addOwnership fills the uid, gid, user, and group fields of file.stats.
func addOwnership(m *value.Map, path string, info os.FileInfo) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	m.Set("uid", int64(st.Uid))
	m.Set("gid", int64(st.Gid))
	if u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
		m.Set("user", u.Username)
	}
	if g, err := user.LookupGroupId(strconv.FormatUint(uint64(st.Gid), 10)); err == nil {
		m.Set("group", g.Name)
	}
}

// plannedOwnership reports what the ownership change would be, without
// making it. It is what lets test mode predict an ownership change
// honestly instead of guessing.
func plannedOwnership(path string, exists bool, wantUser, wantGroup string) (*value.Map, bool, error) {
	if wantUser == "" && wantGroup == "" {
		return nil, false, nil
	}
	uid, gid, err := resolveOwner(wantUser, wantGroup)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return value.MapOf("ownership", states.Change(nil, ownerLabel(wantUser, wantGroup))), true, nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, false, nil
	}
	changed := (uid >= 0 && int(st.Uid) != uid) || (gid >= 0 && int(st.Gid) != gid)
	if !changed {
		return nil, false, nil
	}
	current := fmt.Sprintf("%d:%d", st.Uid, st.Gid)
	return states.Change(current, ownerLabel(wantUser, wantGroup)), true, nil
}

func ownerLabel(u, g string) string {
	switch {
	case u != "" && g != "":
		return u + ":" + g
	case u != "":
		return u
	default:
		return ":" + g
	}
}

// applyOwnership sets the owner and group of a path.
func applyOwnership(path, wantUser, wantGroup string) error {
	uid, gid, err := resolveOwner(wantUser, wantGroup)
	if err != nil {
		return err
	}
	return os.Lchown(path, uid, gid)
}

// resolveOwner turns names into ids. A -1 means "leave this one alone",
// which is what Lchown expects.
func resolveOwner(wantUser, wantGroup string) (int, int, error) {
	uid, gid := -1, -1
	if wantUser != "" {
		u, err := lookupUser(wantUser)
		if err != nil {
			return 0, 0, err
		}
		n, err := strconv.Atoi(u.Uid)
		if err != nil {
			return 0, 0, fmt.Errorf("user %q has a non-numeric uid %q", wantUser, u.Uid)
		}
		uid = n
	}
	if wantGroup != "" {
		g, err := lookupGroup(wantGroup)
		if err != nil {
			return 0, 0, err
		}
		n, err := strconv.Atoi(g.Gid)
		if err != nil {
			return 0, 0, fmt.Errorf("group %q has a non-numeric gid %q", wantGroup, g.Gid)
		}
		gid = n
	}
	return uid, gid, nil
}

func lookupUser(name string) (*user.User, error) {
	if u, err := user.Lookup(name); err == nil {
		return u, nil
	}
	// A numeric owner is accepted, because a state that manages a file for
	// an account created later in the same run has nothing else to name.
	if _, err := strconv.Atoi(name); err == nil {
		if u, err := user.LookupId(name); err == nil {
			return u, nil
		}
		return &user.User{Uid: name, Gid: name, Username: name}, nil
	}
	return nil, fmt.Errorf("user %q does not exist on this node", name)
}

func lookupGroup(name string) (*user.Group, error) {
	if g, err := user.LookupGroup(name); err == nil {
		return g, nil
	}
	if _, err := strconv.Atoi(name); err == nil {
		if g, err := user.LookupGroupId(name); err == nil {
			return g, nil
		}
		return &user.Group{Gid: name, Name: name}, nil
	}
	return nil, fmt.Errorf("group %q does not exist on this node", name)
}

// legacyHash computes MD5 or SHA-1, which exist only to verify a
// source_hash published by an upstream that offers nothing better. Each
// use is warned about by the caller. SPEC section 25.3.
func legacyHash(data []byte, algorithm string) (string, error) {
	switch algorithm {
	case "md5":
		sum := md5.Sum(data)
		return hex.EncodeToString(sum[:]), nil
	case "sha1":
		sum := sha1.Sum(data)
		return hex.EncodeToString(sum[:]), nil
	}
	return "", fmt.Errorf("unsupported legacy hash %q", algorithm)
}
