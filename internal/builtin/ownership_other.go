//go:build !unix && !windows

package builtin

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

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

// keepOwnership is a no-op where there is no Unix ownership to keep.
func keepOwnership(string, os.FileInfo) error { return nil }

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

// applyOwnershipIn is applyOwnership for a name inside an os.Root. There
// is no root-relative form of what applyOwnership does here, so the name
// is checked against the root first and then set by path. That leaves the
// window between the two that the unix version closes; it is recorded in
// DIVERGENCE 5.290 rather than claimed closed.
func applyOwnershipIn(root *os.Root, name, wantUser, wantGroup string) error {
	if _, err := root.Lstat(name); err != nil {
		return err
	}
	return applyOwnership(filepath.Join(root.Name(), name), wantUser, wantGroup)
}
