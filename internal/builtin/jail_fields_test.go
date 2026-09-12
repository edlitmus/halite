package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// Every field this module reads out of a jail entry is a field `jls`
// actually knows.
//
// # Why this test is written backwards
//
// `TestJailReadsWhatARealJlsPrints` checks that what a real `jls` prints
// parses. It cannot catch the opposite mistake, which is what this
// module had: reading a key that `jls` never emits. A missing key
// unmarshals to the zero value, so the field is quietly empty, the
// parser reports no error, and a fixture written in the module's own
// spelling agrees with it.
//
// That is what happened. `jailInfo` carried a `State` read from
// `e["state"]`, and jail_test.go's fixture supplied `"state": "ACTIVE"`
// -- a value invented by whoever wrote the fixture. `state` is not a
// jail parameter and `jls` has never printed one, so every real host
// reported an empty state and every test passed. DIVERGENCE 5.31 for the
// fourth time.
//
// # It asks the tool rather than a fixture
//
// `jls -h` prints the complete list of parameter names it accepts, and
// the kernel publishes the same namespace under `security.jail.param`.
// Either is a statement by the software itself rather than by this
// project, so the check is derived from one instead of from a table
// somebody maintained by hand.
func TestEveryJailFieldThisModuleReadsIsOneJlsKnows(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skipf("jls publishes the field list and this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("jls") == "" {
		t.Skip("this host has no `jls`")
	}
	known := jlsKnownParameters(t, c)
	if len(known) < 20 {
		t.Fatalf("`jls -h` named only %d parameters, which is not the list this expects to read: %v",
			len(known), known)
	}
	// Exactly the keys parseJls subscripts out of a jail entry.
	for _, field := range []string{"jid", "name", "path", "host.hostname", "osrelease", "dying"} {
		if !known[field] {
			t.Errorf("this module reads %q out of a jail entry and `jls` does not know that "+
				"parameter; a key jls never emits reads back as an empty value on every real host",
				field)
		}
	}
	// And the one that started this, asserted as absent so that nobody
	// reintroduces it from a fixture.
	if known["state"] {
		t.Errorf("`jls` now knows a `state` parameter; this module derives state from `dying` " +
			"because it did not, and that reasoning needs revisiting")
	}
}

// jlsKnownParameters reads the parameter list out of jls's own usage.
//
// `jls -h` exits non-zero and prints the list as whitespace-separated
// words, so the exit code is ignored on purpose: it is a usage message,
// which is the thing being read.
func jlsKnownParameters(t *testing.T, c *exec.Context) map[string]bool {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"jls", "-h"}, IgnoreExitCode: true})
	if err != nil {
		t.Skipf("`jls -h` could not be run: %v", err)
	}
	known := map[string]bool{}
	for _, word := range strings.Fields(res.Stdout + " " + res.Stderr) {
		known[word] = true
	}
	return known
}

// libxo's envelope is the one this build looks for, read from a real
// jls rather than from the comment that describes it.
//
// A host with no jails still prints the container, which is what makes
// this runnable anywhere FreeBSD is without creating anything:
//
//	{"__version": "2", "jail-information": {"jail": []}}
//
// The `__version` is deliberately not asserted. It is libxo's own
// envelope version, it has already moved from 1 to 2 since this module
// was written, and nothing here depends on it -- pinning it would make a
// passing test fail for a change that does not matter.
func TestTheRealJlsEnvelopeIsTheOneThisBuildLooksFor(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skipf("this reads a real jls and this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("jls") == "" {
		t.Skip("this host has no `jls`")
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"jls", "--libxo=json", "-v"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Skipf("`jls --libxo=json -v` could not be run: %v", err)
	}
	if !strings.Contains(res.Stdout, "jail-information") {
		t.Fatalf("a real jls did not print the container this build looks for by name; it said: %s",
			strings.TrimSpace(res.Stdout+res.Stderr))
	}
	jails, err := parseJls(res.Stdout)
	if err != nil {
		t.Fatalf("what a real jls printed did not parse: %v (it said %q)", err, res.Stdout)
	}
	t.Logf("a real jls on this host parsed into %d jail(s)", len(jails))
}

// A jail entry with no `dying` flag is ACTIVE, and one with it is DYING.
//
// This is the unit half of the field defect: it fixes the reported word
// to the parameter that exists, so that a future fixture cannot
// reintroduce an invented one without this failing.
func TestAJailsStateIsDerivedFromTheParameterThatExists(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
		want  string
	}{
		{"a running jail", `{"jid":1,"name":"web","path":"/jails/web","host.hostname":"web","dying":false}`, "ACTIVE"},
		{"one that will not go away", `{"jid":2,"name":"old","path":"/jails/old","host.hostname":"old","dying":true}`, "DYING"},
		{"libxo quoting the flag", `{"jid":3,"name":"q","path":"/jails/q","host.hostname":"q","dying":"true"}`, "DYING"},
		{"no flag at all", `{"jid":4,"name":"n","path":"/jails/n","host.hostname":"n"}`, "ACTIVE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jails, err := parseJls(`{"__version":"2","jail-information":{"jail":[` + tc.entry + `]}}`)
			if err != nil {
				t.Fatalf("parseJls: %v", err)
			}
			if len(jails) != 1 {
				t.Fatalf("expected one jail, got %d", len(jails))
			}
			for _, j := range jails {
				if got := j.state(); got != tc.want {
					t.Errorf("state is %q, want %q", got, tc.want)
				}
			}
		})
	}
}
