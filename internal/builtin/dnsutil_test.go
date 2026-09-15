package builtin

import (
	"net"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The functions carry Salt's capitals because they are DNS record types.
// This shipped as `dnsutil.a`, which no tree written for Salt can reach:
// an estate's own hostname state writes `salt['dnsutil.A'](id)` and got
// "no such execution module or function".
func TestDNSUtilUsesSaltsNames(t *testing.T) {
	r := New()
	for _, name := range []string{"dnsutil.A", "dnsutil.AAAA"} {
		if !r.Exec.Has(name) {
			t.Errorf("%s is not registered; it is what a tree calls", name)
		}
	}
	if r.Exec.Has("dnsutil.a") {
		t.Error("dnsutil.a is still registered; Salt has no such function and no tree calls it")
	}
}

// A is an A record and AAAA is an AAAA record, so each answers with one
// address family. The function these replaced returned whatever the
// resolver had, so a tree asking for an A record could be given an IPv6
// address and use it where it wanted an IPv4 one.
func TestDNSUtilSeparatesTheAddressFamilies(t *testing.T) {
	// localhost is the one name every machine resolves, and it is the
	// only one a test may depend on: anything else makes this a test of
	// the network.
	v4, v6 := lookupAddressFamily("localhost", true), lookupAddressFamily("localhost", false)

	for _, addr := range v4 {
		ip := net.ParseIP(addr.(string))
		if ip == nil || ip.To4() == nil {
			t.Errorf("A returned %v, which is not an IPv4 address", addr)
		}
	}
	for _, addr := range v6 {
		ip := net.ParseIP(addr.(string))
		if ip == nil || ip.To4() != nil {
			t.Errorf("AAAA returned %v, which is not an IPv6 address", addr)
		}
	}
	if len(v4)+len(v6) == 0 {
		t.Skip("this machine does not resolve localhost at all")
	}
}

// A name that does not resolve is an empty list, not an error and not a
// string. Salt returns the string "Unable to resolve <host>" from a
// function it documents as always returning a list; a tree that indexes
// the result would take the first character of that sentence.
func TestDNSUtilReturnsAListWhenNothingResolves(t *testing.T) {
	r := New()
	out, err := r.Exec.Call(newCtx(false), "dnsutil.A",
		value.MapOf("host", "no-such-host.invalid"))
	if err != nil {
		t.Fatalf("it returned an error rather than an empty list: %v", err)
	}
	list, ok := out.([]any)
	if !ok {
		t.Fatalf("it returned %T, and a tree indexes a list", out)
	}
	if len(list) != 0 {
		t.Errorf("a name that does not resolve gave %v", list)
	}
}
