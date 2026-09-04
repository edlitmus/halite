package builtin

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsec"
)

// Ownership on Windows is an owner and an access control list rather
// than a uid and gid pair. Reading the owner has worked since the grains
// went in; setting it was refused, because the module SPEC 15.3 puts it
// in — win_dacl — did not exist. It does now, so a `user:` on a file
// state does what it says.
//
// There is still no such thing as a file's group here. `group:` is
// refused rather than mapped onto something that resembles it: a state
// that asked for a group and silently got an access control entry would
// be a state whose file said one thing and whose host did another.

// addOwnership fills the owner field of file.stats.
//
// No uid or gid: an account here is a SID, and inventing a number for it
// would be worse than the field's absence. The name is the answer an
// administrator on this platform would give.
func addOwnership(m *value.Map, path string, info os.FileInfo) {
	who, err := winsec.Owner(path)
	if err != nil {
		return
	}
	m.Set("user", who)
}

// plannedOwnership reports what the change would be, without making it.
//
// A file that does not exist yet has no owner to compare against, and a
// prediction that claimed to know what it would be would be guessing.
func plannedOwnership(path string, exists bool, wantUser, wantGroup string) (*value.Map, bool, error) {
	if wantGroup != "" {
		return nil, false, groupRefusal(wantGroup)
	}
	if wantUser == "" {
		return nil, false, nil
	}
	if !exists {
		changes := value.NewMap(1)
		changes.Set("user", states.Change("", wantUser))
		return changes, true, nil
	}
	current, err := winsec.Owner(path)
	if err != nil {
		return nil, false, err
	}
	if sameAccount(current, wantUser) {
		return nil, false, nil
	}
	changes := value.NewMap(1)
	changes.Set("user", states.Change(current, wantUser))
	return changes, true, nil
}

func applyOwnership(path, wantUser, wantGroup string) error {
	if wantGroup != "" {
		return groupRefusal(wantGroup)
	}
	if wantUser == "" {
		return nil
	}
	current, err := winsec.Owner(path)
	if err == nil && sameAccount(current, wantUser) {
		return nil
	}
	return winsec.SetOwner(path, wantUser)
}

// groupRefusal says what was asked for and why this platform has no
// answer, rather than naming a module the reader then has to look up.
//
// Windows records a primary group in a security descriptor, but nothing
// on the platform reads it: it exists for POSIX interoperability and
// setting it changes no access. Mapping `group:` onto it would let a
// state pass while granting nobody anything.
func groupRefusal(wantGroup string) error {
	return fmt.Errorf(
		"group %q: a file on Windows has an owner and an access control list, not a group; "+
			"use win_dacl.present to give a group access, or win_dacl.owner to set the owner",
		wantGroup)
}

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
