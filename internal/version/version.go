// Package version carries the build identity that every binary reports and
// that the `haliteversion` grain and template variable expose.
//
// Values are set at link time with -X. The defaults are what a `go build`
// without the Makefile produces, so a developer build is identifiable as
// one rather than claiming to be a release.
package version

import "runtime/debug"

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

// FIPS reports whether this is a FIPS 140-3 artifact, set at link time by
// the parallel build described in SPEC section 27.4.
var FIPS = "false"

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
	if FIPS == "true" {
		s += " (fips)"
	}
	return s
}
