package grains

import (
	"reflect"
	"testing"
)

// The grains compared against a real Salt, value by value.
//
// Every grain in this file was wrong, and every one of them was covered
// by a test that passed. TestCoreGrainsArePresent asserts that a key
// exists; the Windows test asserts that `systemd` is a mapping. Neither
// looks at a value, so `grains['systemd']['version']` reported the
// string "systemd" on every Linux host for as long as the grain has
// existed, and nothing noticed.
//
// What found them was running Salt 3007.1 and this build on one host
// and comparing every grain the two have in common: 17 disagreed, and
// 12 of those were defects here. The five that were not are the node's
// own id, `$PATH` as two processes inherited it, the version the
// compatibility shim claims, and an ordering Salt does not fix. The
// `systemd` grain is a thirteenth, found by reading the dump before the
// comparison was written.
//
// That is the same lesson as DIVERGENCE 5.31 -- a fixture written from
// what the author meant proves nothing about the tool -- one level up,
// applied to the reference implementation rather than to a command.
//
// The rules asserted below were read out of Salt's own
// `salt/grains/core.py` rather than remembered, and each one is a rule
// a tree depends on. See DIVERGENCE 5.61.

func TestOsFingerSpellsUbuntuWithItsFullRelease(t *testing.T) {
	// Salt uses the full release for Ubuntu, Pop and NixOS, whose
	// releases only mean something with both halves, and the major
	// alone everywhere else. The name is the short `os` for the
	// families that have one and `osfullname` otherwise.
	for _, c := range []struct {
		osName, osFullName, release, want string
	}{
		{"Ubuntu", "Ubuntu", "22.04", "Ubuntu-22.04"},
		{"Ubuntu", "Ubuntu", "24.04", "Ubuntu-24.04"},
		{"Debian", "Debian GNU/Linux", "12", "Debian-12"},
		{"CentOS", "CentOS Linux", "7.9.2009", "CentOS Linux-7"},
		{"Pop", "Pop!_OS", "22.04", "Pop!_OS-22.04"},
		{"AlmaLinux", "AlmaLinux", "9.4", "AlmaLinux-9"},
	} {
		if got := osFinger(c.osName, c.osFullName, c.release); got != c.want {
			t.Errorf("osFinger(%q, %q, %q) = %q, want %q",
				c.osName, c.osFullName, c.release, got, c.want)
		}
	}
}

func TestOsMajorReleaseIsANumber(t *testing.T) {
	// `{% if grains['osmajorrelease'] >= 22 %}` is the whole purpose of
	// the grain, and against the string this used to return that is
	// either an error or an ordering in which "9" beats "22".
	if got := majorRelease("22.04"); got != int64(22) {
		t.Errorf("majorRelease(\"22.04\") = %#v, want int64(22)", got)
	}
	if got := majorRelease("9.4"); got != int64(9) {
		t.Errorf("majorRelease(\"9.4\") = %#v, want int64(9)", got)
	}
	// A release with no leading number has no major, and "" is the
	// absence Salt reports by leaving the grain unset. Zero would be a
	// number that compares.
	if got := majorRelease("rolling"); got != "" {
		t.Errorf("majorRelease(\"rolling\") = %#v, want \"\"", got)
	}
}

func TestLocaleSplitsTheLanguageFromTheCodeset(t *testing.T) {
	for _, c := range []struct{ lang, language, encoding string }{
		{"C.UTF-8", "C", "UTF-8"},
		{"en_US.UTF-8", "en_US", "UTF-8"},
		{"C", "C", "UTF-8"},
		{"", "", "UTF-8"},
		{"de_DE.ISO8859-15@euro", "de_DE@euro", "ISO8859-15"},
	} {
		language, encoding := splitLocale(c.lang)
		if language != c.language || encoding != c.encoding {
			t.Errorf("splitLocale(%q) = (%q, %q), want (%q, %q)",
				c.lang, language, encoding, c.language, c.encoding)
		}
	}
}

func TestSortIPsOrdersNumericallyAndDeduplicates(t *testing.T) {
	// String ordering would put 10.0.0.1 before 9.0.0.1, and a tree
	// that reads grains['ipv4'][0] is reading a position.
	got := sortIPs([]any{"10.0.0.1", "9.0.0.1", "127.0.0.1", "10.0.0.1"})
	want := []any{"9.0.0.1", "10.0.0.1", "127.0.0.1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortIPs = %v, want %v", got, want)
	}
}
