package fips

import (
	"strings"
	"testing"
)

// The facts have to agree with each other in whichever mode the test
// binary was built and run. Each of these was a way to report a FIPS
// deployment that is not one.
func TestTheReportedFactsAreConsistent(t *testing.T) {
	if Module() == inTree {
		t.Errorf("the in-tree crypto is being reported as a certified module (%q)", Module())
	}
	if Artifact() && !Enabled() {
		t.Error("a certified module is in use and FIPS mode is off")
	}
	if Artifact() && Module() == "" {
		t.Error("this is a FIPS artifact with no module version")
	}
	if Enforced() && !Enabled() {
		t.Error("non-approved algorithms are rejected but FIPS mode is off")
	}
	if Restricted() != Enabled() {
		t.Error("the restrictions are not keyed on FIPS mode")
	}
}

// Status is what an operator and an assessor read, so it must never
// claim a certified module that is not there.
func TestStatusNeverClaimsAModuleItDoesNotHave(t *testing.T) {
	s := Status()
	switch {
	case !Enabled():
		if s != "fips mode off" {
			t.Errorf("FIPS mode is off and the status is %q", s)
		}
	case Artifact():
		if !strings.Contains(s, "module "+Module()) {
			t.Errorf("a FIPS artifact does not name its module: %q", s)
		}
		if !strings.Contains(s, "self-tests passed") {
			t.Errorf("a FIPS artifact does not report its self-tests: %q", s)
		}
	default:
		// FIPS mode without a certified module. This is the case that
		// gets reported as a FIPS deployment when it is not one, so the
		// status has to say so rather than stay quiet.
		if !strings.Contains(s, "not a certified module") &&
			!strings.Contains(s, "not reportable") {
			t.Errorf("FIPS mode without a certified module reads as %q", s)
		}
	}
}

// CertifiedModule is what the Makefile passes to GOFIPS140 and what
// fips-verify greps for. A build that shipped one and checked the other
// would pass its own gate while shipping something else.
func TestTheCertifiedModuleConstantIsAVersion(t *testing.T) {
	if !strings.HasPrefix(CertifiedModule, "v") {
		t.Errorf("CertifiedModule is %q, which is not a version", CertifiedModule)
	}
	if Artifact() && Module() != CertifiedModule {
		t.Errorf("this artifact carries module %q, not the %q the build asks for",
			Module(), CertifiedModule)
	}
}
