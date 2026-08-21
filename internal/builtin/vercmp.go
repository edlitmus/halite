package builtin

import (
	"fmt"
	"strings"
)

// Version comparison, required by SPEC section 15.2: "`version_cmp`
// implements Debian and RPM version comparison directly, including epochs
// and tildes".
//
// Doing it directly rather than shelling out to `dpkg --compare-versions`
// matters twice. It works on a node that has neither tool, which is every
// node when the hub is deciding whether an upgrade is needed. And it is
// one process rather than one per comparison, which a `pkg.latest` over a
// few hundred packages notices.
//
// Both algorithms are transcriptions of the originals, and both have a
// rule that catches people out. In Debian and in RPM a `~` sorts *before*
// everything, including the empty string, which is how `1.0~rc1` comes
// before `1.0`. Getting that backwards installs a release candidate over
// a release.

// CompareDebian compares two Debian version strings, returning -1, 0, or
// 1. It is `dpkg --compare-versions` without dpkg.
func CompareDebian(a, b string) int {
	ae, av, ar := splitDebian(a)
	be, bv, br := splitDebian(b)

	if c := compareInts(ae, be); c != 0 {
		return c
	}
	if c := debianPart(av, bv); c != 0 {
		return c
	}
	return debianPart(ar, br)
}

// splitDebian breaks [epoch:]upstream[-revision] apart. An absent epoch
// is zero and an absent revision is empty, which is what makes `1.0` and
// `0:1.0-` compare equal.
func splitDebian(v string) (epoch int, upstream, revision string) {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ':'); i >= 0 {
		if n, ok := atoiSafe(v[:i]); ok {
			epoch = n
			v = v[i+1:]
		}
	}
	// The revision is what follows the *last* hyphen, so an upstream
	// version containing hyphens keeps them.
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// debianPart compares one part of a Debian version with dpkg's algorithm:
// alternating runs of non-digits, compared with dpkg's own character
// order, and runs of digits, compared numerically.
func debianPart(a, b string) int {
	for len(a) > 0 || len(b) > 0 {
		// The non-digit run.
		i, j := 0, 0
		for i < len(a) && !isDigitByte(a[i]) {
			i++
		}
		for j < len(b) && !isDigitByte(b[j]) {
			j++
		}
		if c := debianAlpha(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]

		// The digit run.
		i, j = 0, 0
		for i < len(a) && isDigitByte(a[i]) {
			i++
		}
		for j < len(b) && isDigitByte(b[j]) {
			j++
		}
		if c := compareNumeric(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]
	}
	return 0
}

// debianOrder is dpkg's character ordering. A tilde sorts before
// everything, letters sort in their own order before every other
// non-letter, and the end of the string sorts as zero.
func debianOrder(c byte) int {
	switch {
	case c == '~':
		return -1
	case isAlphaByte(c):
		return int(c)
	}
	return int(c) + 256
}

func debianAlpha(a, b string) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var ac, bc int
		if i < len(a) {
			ac = debianOrder(a[i])
		}
		if i < len(b) {
			bc = debianOrder(b[i])
		}
		if ac != bc {
			if ac < bc {
				return -1
			}
			return 1
		}
	}
	return 0
}

// CompareRPM compares two RPM version strings, returning -1, 0, or 1. It
// is rpmvercmp, plus the epoch and release handling around it.
func CompareRPM(a, b string) int {
	ae, av, ar := splitRPM(a)
	be, bv, br := splitRPM(b)

	if c := compareInts(ae, be); c != 0 {
		return c
	}
	if c := rpmvercmp(av, bv); c != 0 {
		return c
	}
	// An absent release compares equal to any release, which is how
	// `1.0` and `1.0-1` are the same package to rpm.
	if ar == "" || br == "" {
		return 0
	}
	return rpmvercmp(ar, br)
}

func splitRPM(v string) (epoch int, version, release string) {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ':'); i >= 0 {
		if n, ok := atoiSafe(v[:i]); ok {
			epoch = n
			v = v[i+1:]
		}
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// rpmvercmp is a transcription of rpm's own rpmvercmp.c.
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	for len(a) > 0 || len(b) > 0 {
		// Everything that is not alphanumeric, a tilde, or a caret is a
		// separator and is skipped on both sides.
		for len(a) > 0 && !isVerByte(a[0]) {
			a = a[1:]
		}
		for len(b) > 0 && !isVerByte(b[0]) {
			b = b[1:]
		}

		// A tilde sorts before everything, including nothing at all.
		if strings.HasPrefix(a, "~") || strings.HasPrefix(b, "~") {
			if !strings.HasPrefix(a, "~") {
				return 1
			}
			if !strings.HasPrefix(b, "~") {
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}

		// A caret sorts after nothing but before anything else, which is
		// how a post-release snapshot sits between two releases.
		if strings.HasPrefix(a, "^") || strings.HasPrefix(b, "^") {
			switch {
			case a == "":
				return -1
			case b == "":
				return 1
			case !strings.HasPrefix(a, "^"):
				return 1
			case !strings.HasPrefix(b, "^"):
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}

		if len(a) == 0 || len(b) == 0 {
			break
		}

		// A run of digits or a run of letters, whichever comes first.
		var i, j int
		numeric := isDigitByte(a[0])
		if numeric {
			for i < len(a) && isDigitByte(a[i]) {
				i++
			}
			for j < len(b) && isDigitByte(b[j]) {
				j++
			}
		} else {
			for i < len(a) && isAlphaByte(a[i]) {
				i++
			}
			for j < len(b) && isAlphaByte(b[j]) {
				j++
			}
		}
		// One side has digits where the other has letters: digits win,
		// because 10 is a later version than "beta".
		if j == 0 {
			if numeric {
				return 1
			}
			return -1
		}

		segA, segB := a[:i], b[:j]
		if numeric {
			if c := compareNumeric(segA, segB); c != 0 {
				return c
			}
		} else {
			if segA != segB {
				if segA < segB {
					return -1
				}
				return 1
			}
		}
		a, b = a[i:], b[j:]
	}

	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return -1
	}
	return 1
}

// compareNumeric compares two digit runs by value, which is why 10 beats
// 9 and why 007 equals 7.
func compareNumeric(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func atoiSafe(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if !isDigitByte(s[i]) {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }
func isAlphaByte(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isVerByte(c byte) bool {
	return isDigitByte(c) || isAlphaByte(c) || c == '~' || c == '^'
}

// versionScheme picks the comparison a node's packages use.
func versionScheme(name, osFamily string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		switch strings.ToLower(osFamily) {
		case "debian":
			return "debian", nil
		case "redhat", "rhel", "suse":
			return "rpm", nil
		case "freebsd":
			return "freebsd", nil
		}
		// An unknown family gets the RPM algorithm, which is the more
		// forgiving of the two and the one Salt falls back to.
		return "rpm", nil
	case "debian", "deb", "dpkg", "apt":
		return "debian", nil
	case "rpm", "redhat", "yum", "dnf", "zypper":
		return "rpm", nil
	case "freebsd", "pkgng", "pkg":
		return "freebsd", nil
	}
	return "", fmt.Errorf("unknown version comparison scheme %q; halite compares debian, rpm, and freebsd versions", name)
}
