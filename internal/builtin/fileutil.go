package builtin

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// writeAtomic writes a file the only way a configuration management system
// should: to a temporary file in the *target directory*, fsynced, then
// renamed into place.
//
// The temporary file goes in the target directory rather than in /tmp so
// that the rename is within one filesystem and is therefore atomic; a
// cross-device rename degrades to a copy, and a copy that is interrupted
// leaves a half-written configuration file behind. SPEC section 13.5.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".halite-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	// The mode is set before the rename, so the file is never visible at
	// its final path with the temporary file's restrictive default.
	if mode != 0 {
		if err := os.Chmod(tmpName, mode); err != nil {
			return fmt.Errorf("setting the mode on %s: %w", tmpName, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s into place: %w", tmpName, err)
	}
	// The directory entry itself is synced, so the rename survives a
	// power loss rather than the file existing with no name.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// hashFile digests a file with the named algorithm.
func hashFile(path, algorithm string) (string, error) {
	h, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashBytes digests a buffer.
func hashBytes(b []byte, algorithm string) (string, error) {
	h, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// newHash builds a digest.
//
// MD5 and SHA-1 are deliberately absent here. They exist in halite only to
// verify a source_hash published by an upstream that offers nothing
// better, and that path warns; a state that asks for them as its own
// hash_type is asking for a weaker guarantee than it needs. SPEC section
// 25.3.
func newHash(algorithm string) (hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case "", "sha256":
		return sha256.New(), nil
	case "sha384":
		return sha512.New384(), nil
	case "sha512":
		return sha512.New(), nil
	}
	return nil, fmt.Errorf("unsupported hash type %q; halite offers sha256, sha384, and sha512", algorithm)
}

// parseMode reads a file mode written as a string, which is the only way a
// mode may be written: an unquoted 0644 is read by YAML 1.1 as the decimal
// 420, and the signature layer refuses it before this is ever reached.
func parseMode(s string) (os.FileMode, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty mode")
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not an octal file mode", s)
	}
	return os.FileMode(n), nil
}

// formatMode renders a mode the way an operator writes it.
func formatMode(m os.FileMode) string {
	return fmt.Sprintf("%04o", m.Perm())
}

// unifiedDiff renders a minimal unified diff between two texts.
//
// It backs `--diff` in test mode, which is the difference between "this
// file would change" and "here is what would change about it".
func unifiedDiff(oldText, newText, label string) string {
	if oldText == newText {
		return ""
	}
	oldLines := splitKeepEmpty(oldText)
	newLines := splitKeepEmpty(newText)

	// Trim the common prefix and suffix so the diff shows the change
	// rather than the file.
	start := 0
	for start < len(oldLines) && start < len(newLines) && oldLines[start] == newLines[start] {
		start++
	}
	endOld, endNew := len(oldLines), len(newLines)
	for endOld > start && endNew > start && oldLines[endOld-1] == newLines[endNew-1] {
		endOld--
		endNew--
	}

	const context = 3
	from := max(0, start-context)
	toOld := min(len(oldLines), endOld+context)
	toNew := min(len(newLines), endNew+context)

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s (current)\n+++ %s (managed)\n", label, label)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", from+1, toOld-from, from+1, toNew-from)
	for i := from; i < start; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	for i := start; i < endOld; i++ {
		fmt.Fprintf(&b, "-%s\n", oldLines[i])
	}
	for i := start; i < endNew; i++ {
		fmt.Fprintf(&b, "+%s\n", newLines[i])
	}
	for i := endOld; i < toOld; i++ {
		fmt.Fprintf(&b, " %s\n", oldLines[i])
	}
	return b.String()
}

func splitKeepEmpty(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// A trailing newline produces a final empty element that is not a
	// line; drop it so the diff does not show a phantom change.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
