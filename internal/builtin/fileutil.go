package builtin

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/states"
)

// writeAtomic writes a file the only way a configuration management system
// should: to a temporary file in the *target directory*, fsynced, then
// renamed into place.
//
// The temporary file goes in the target directory rather than in /tmp so
// that the rename is within one filesystem and is therefore atomic; a
// cross-device rename degrades to a copy, and a copy that is interrupted
// leaves a half-written configuration file behind. SPEC section 13.5.
//
// internal/atomicfile holds the idiom, which six packages had a copy of.
//
// **A replaced file keeps its owner and group.** The rename puts a new
// file in place, owned by whoever ran the write, and nothing here used to
// put the old owner back: only file.managed did, after DIVERGENCE 5.253,
// so every other writer -- file.replace, file.line, file.append,
// file.blockreplace, file.comment, file.sed, file.serialize, and the
// modules that rewrite /etc/hosts, sysctl.conf, fstab and the rest --
// turned a `postgres:postgres 0640` file into `root:root 0640` (issue 321,
// M-20). That is the 5.253 outage, waiting in forty other places.
//
// The temporary file is given the owner before the rename, so there is no
// moment at which the file is in place with the wrong one. A caller that
// was asked for a user or group still applies it afterwards and wins. An
// unprivileged run cannot give a file to another account, and that is
// tolerated rather than failed, which is what it got before; as root, which
// is how a node runs, it does not happen. What this does not keep is an
// ACL or an extended attribute: those go with the old inode, as they did.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	was, err := os.Lstat(path)
	if err != nil || !was.Mode().IsRegular() {
		return atomicfile.Write(path, data, mode)
	}
	return atomicfile.WritePrepared(path, data, mode, func(tmp string) error {
		if err := keepOwnership(tmp, was); err != nil && !errors.Is(err, fs.ErrPermission) {
			return err
		}
		return nil
	})
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
	if n > 0o7777 {
		return 0, fmt.Errorf("%q is more than a file mode: the highest is 7777", s)
	}
	// The setuid, setgid and sticky digits are not bits of an
	// os.FileMode's permissions: Go keeps them as flags of their own, and
	// os.Chmod reads the flags and ignores a raw 04000. This returned the
	// raw number, so `mode: '4755'` set 0755, and a file that had setuid
	// compared equal to 0755 because only Perm() was compared (issue 321,
	// M-23). They are translated here, and modeBits compares them.
	m := os.FileMode(n & 0o777)
	if n&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if n&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if n&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m, nil
}

// modeBits is the part of a mode a state manages: the permissions and the
// setuid, setgid and sticky flags, and not the file's type.
func modeBits(m os.FileMode) os.FileMode {
	return m & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

// formatMode renders a mode the way an operator writes it, special digit
// included.
func formatMode(m os.FileMode) string {
	n := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		n |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		n |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		n |= 0o1000
	}
	return fmt.Sprintf("%04o", n)
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

// dirOf is filepath.Dir, named locally so the editing helpers do not each
// need the import.
func dirOf(p string) string { return filepath.Dir(p) }

// withWarnings attaches warnings to a result.
//
// A state that cannot carry out part of what it was asked has to say so
// somewhere. Reporting it as a change is wrong — the change would be
// reported again on every run, and never applied — and dropping it is
// worse. SPEC 11.6 gives a result a Warnings field for exactly this.
func withWarnings(r states.Result, warnings []string) states.Result {
	r.Warnings = append(r.Warnings, warnings...)
	return r
}
