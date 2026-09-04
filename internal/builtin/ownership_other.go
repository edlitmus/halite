//go:build !unix && !windows

package builtin

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/value"
)

// Ownership on Windows is an ACL rather than a uid and gid pair, and the
// win_dacl module of SPEC section 15.3 owns it. Until that ships, a state
// that asks for ownership here is refused rather than silently ignored.

func addOwnership(m *value.Map, info os.FileInfo) {}

func plannedOwnership(path string, exists bool, wantUser, wantGroup string) (*value.Map, bool, error) {
	if wantUser == "" && wantGroup == "" {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("user and group are managed by win_dacl on this platform, which is not in this build")
}

func applyOwnership(path, wantUser, wantGroup string) error {
	if wantUser == "" && wantGroup == "" {
		return nil
	}
	return fmt.Errorf("user and group are managed by win_dacl on this platform, which is not in this build")
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
