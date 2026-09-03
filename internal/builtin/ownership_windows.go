package builtin

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsec"
)

// Ownership on Windows is an access control list rather than a uid and
// gid pair, and the win_dacl module of SPEC section 15.3 owns setting
// it. Reading it is a different matter: a file here does have an owner,
// and this reported that it had none — `file.stats` returned no `user`
// field and `file.get_user` answered "has no owner this platform
// reports" for every file on the platform.
//
// So the owner is read, and only the setting is refused.

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

func plannedOwnership(path string, exists bool, wantUser, wantGroup string) (*value.Map, bool, error) {
	if wantUser == "" && wantGroup == "" {
		return nil, false, nil
	}
	return nil, false, ownershipRefusal(wantUser, wantGroup)
}

func applyOwnership(path, wantUser, wantGroup string) error {
	if wantUser == "" && wantGroup == "" {
		return nil
	}
	return ownershipRefusal(wantUser, wantGroup)
}

// ownershipRefusal says what was asked for and why it cannot be done,
// rather than naming a module the reader then has to look up. Taking
// ownership of a file needs a privilege the node may not hold, and there
// is no such thing as a file's group here at all.
func ownershipRefusal(wantUser, wantGroup string) error {
	if wantGroup != "" && wantUser == "" {
		return fmt.Errorf(
			"group %q: a file on Windows has an owner and an access control list, not a group; "+
				"SPEC 15.3 puts this in win_dacl, which this build does not have", wantGroup)
	}
	return fmt.Errorf(
		"user %q: setting a file's owner on Windows needs the privilege to take ownership, "+
			"which this build does not ask for; SPEC 15.3 puts this in win_dacl, "+
			"which this build does not have", wantUser)
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
