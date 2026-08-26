// Package version carries the build identity that every binary reports and
// that the `haliteversion` grain and template variable expose.
//
// Values are set at link time with -X. The defaults are what a `go build`
// without the Makefile produces, so a developer build is identifiable as
// one rather than claiming to be a release.
package version

import (
	"runtime/debug"

	"github.com/edlitmus/halite/internal/fips"
)

// Version is the release version, set at link time.
var Version = "0.0.0-dev"

// Commit is the source revision, set at link time or read from the build
// info that -buildvcs=true embeds.
var Commit = ""

// SaltCompat is the Salt version this build claims compatibility with. It
// backs the `saltversion` template variable and grain, so that the
// `{% if saltversion >= '3006' %}` guards in an existing tree evaluate the
// way the tree's author intended. See SPEC section 10.2.7.
const SaltCompat = "3007.1"

// FIPS reports whether this is a FIPS 140-3 artifact of SPEC 27.4.
//
// Read from the module at runtime rather than stamped at link time. The
// -X flag it used to be recorded what the builder intended, which is a
// different fact from what the process is doing and is the one that can
// be wrong: a plain build carrying -X FIPS=true claimed a certified
// module it did not have.
func FIPS() bool { return fips.Artifact() }

func init() {
	if Commit != "" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			Commit = s.Value
			return
		}
	}
}

// String renders the version for `<binary> version`.
func String() string {
	s := Version
	if Commit != "" {
		if len(Commit) > 12 {
			s += "+" + Commit[:12]
		} else {
			s += "+" + Commit
		}
	}
	if fips.Artifact() {
		s += " (fips " + fips.Module() + ")"
	} else if fips.Enabled() {
		// FIPS mode without a certified module underneath it. Said out
		// loud, because it is the case somebody reports as a FIPS
		// deployment when it is not one.
		s += " (fips mode, in-tree crypto)"
	}
	return s
}

// Full is what `<binary> version` prints: the identity and, when FIPS is
// involved at all, what the module is doing.
func Full(binary string) string {
	s := binary + " " + String()
	if fips.Enabled() {
		s += "\n" + fips.Status()
	}
	return s
}
