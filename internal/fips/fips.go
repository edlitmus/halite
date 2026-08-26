// Package fips reports what the Go Cryptographic Module is doing in this
// process, and decides where halite applies the restrictions SPEC 27.4
// names.
//
// The facts come from crypto/fips140 at runtime rather than from a link
// time -X flag. A flag records what the builder meant; this records what
// the process is actually doing, and the two are exactly the pair an
// assessment needs to not have to trust.
package fips

import "crypto/fips140"

// CertifiedModule is the module version SPEC 27.4 names, and the value
// GOFIPS140 is set to for the parallel artifact set.
const CertifiedModule = "v1.0.0"

// inTree is what crypto/fips140 reports when the process is using the
// toolchain's own crypto rather than a frozen, certified module. A build
// that was never given GOFIPS140 says this even when FIPS mode is turned
// on at runtime, which is the distinction this package exists to keep.
const inTree = "latest"

// Module is the version of the Go Cryptographic Module in use, or an
// empty string when this process is not using a certified one — or when
// the toolchain that built it cannot say.
//
// crypto/fips140.Version arrived in Go 1.26 and SPEC 4.1 sets the floor
// at 1.25, so on a 1.25 toolchain the version is simply not reportable.
// Reported as unknown rather than as absent: the two are different
// facts, and calling an unreportable module "not certified" would make
// a correct FIPS build look like a broken one.
func Module() string {
	if v := moduleVersion(); v != inTree && v != "" {
		return v
	}
	return ""
}

// Reportable tells whether this toolchain can name the module at all.
func Reportable() bool { return moduleVersion() != "" }

// Artifact reports whether this binary was built against a certified
// module — that is, whether it is one of the `-fips` artifacts.
//
// Not the same question as Enabled. A binary built without GOFIPS140 and
// run with GODEBUG=fips140=on reports FIPS mode and is not a FIPS
// artifact: its cryptography is the toolchain's, which is not what was
// certified. Anything that claims a FIPS build to an auditor has to ask
// this one.
func Artifact() bool { return Module() != "" }

// Enabled reports whether the module is in FIPS mode.
func Enabled() bool { return fips140.Enabled() }

// Enforced reports whether the module rejects non-approved algorithms
// outright, which is GODEBUG=fips140=only rather than =on.
//
// SPEC 27.4 has the service unit set `on`, and `on` does not enforce:
// it routes approved algorithms through the module and leaves the rest
// reachable. The restrictions the specification describes are applied by
// this build rather than assumed from the setting; see DIVERGENCE 1.10.
func Enforced() bool { return fips140.Enforced() }

// Restricted reports whether halite applies its own FIPS restrictions:
// no Ed25519, no SHA-1, and so no TOTP.
//
// Keyed on FIPS mode rather than on the artifact, because an operator
// who turned FIPS mode on asked for those restrictions whichever binary
// they turned it on in.
func Restricted() bool { return Enabled() }

// Status is the one-line summary `<binary> version` prints.
//
// The self-test is reported as passed rather than queried: the module
// runs its power-on self-tests during package initialisation and panics
// if any of them fails, so a process that is alive to answer the
// question has already passed them. Saying so is honest; offering a
// status that could only ever read "passed" would not be.
func Status() string {
	if !Enabled() {
		return "fips mode off"
	}
	s := "fips mode on"
	switch {
	case Module() != "":
		s += ", module " + Module() + ", self-tests passed"
	case !Reportable():
		s += ", self-tests passed, module version not reportable by this toolchain " +
			"(crypto/fips140.Version needs go1.26)"
	default:
		s += ", in-tree crypto (not a certified module)"
	}
	if Enforced() {
		s += ", non-approved algorithms rejected"
	}
	return s
}
