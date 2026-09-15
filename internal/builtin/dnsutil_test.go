package builtin

import (
	"net"
	"os"
	"path/filepath"
	"strings"
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

// Salt's hosts functions on dnsutil are parse_hosts, hosts_append and
// hosts_remove. This shipped one called `hosts_file`, which Salt has
// never had -- an invented name is a function no tree can call, the same
// defect as the lower-case `dnsutil.a` beside it.
func TestDNSUtilHostsFunctionsUseSaltsNames(t *testing.T) {
	r := New()
	for _, name := range []string{"dnsutil.parse_hosts", "dnsutil.hosts_append", "dnsutil.hosts_remove"} {
		if !r.Exec.Has(name) {
			t.Errorf("%s is not registered; it is what a tree calls", name)
		}
	}
	if r.Exec.Has("dnsutil.hosts_file") {
		t.Error("dnsutil.hosts_file is still registered; Salt has no such function")
	}
}

func TestDNSUtilHostsRoundTrip(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "hosts")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	r := New()

	t.Run("parse_hosts reads a mapping of address to names", func(t *testing.T) {
		path := write(t, "# a comment\n127.0.0.1 localhost lh\n10.0.0.1 web1\n")
		out, err := r.Exec.Call(newCtx(false), "dnsutil.parse_hosts",
			value.MapOf("hostsfile", path))
		if err != nil {
			t.Fatal(err)
		}
		m, ok := out.(*value.Map)
		if !ok {
			t.Fatalf("parse_hosts returned %T", out)
		}
		names, _ := m.Get("127.0.0.1")
		list, _ := names.([]any)
		if len(list) != 2 {
			t.Errorf("127.0.0.1 -> %v, want two names", names)
		}
		if _, ok := m.Get("# a comment"); ok {
			t.Error("a comment was read as an address")
		}
	})

	t.Run("hosts_append adds names and skips the ones already there", func(t *testing.T) {
		path := write(t, "10.0.0.1 web1\n")
		if _, err := r.Exec.Call(newCtx(false), "dnsutil.hosts_append",
			value.MapOf("hostsfile", path, "ip_addr", "10.0.0.1", "entries", "web1,web2")); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if n := strings.Count(string(body), "web1"); n != 1 {
			t.Errorf("web1 appears %d times:\n%s", n, body)
		}
		if !strings.Contains(string(body), "web2") {
			t.Errorf("web2 was not added:\n%s", body)
		}

		// A second identical call adds nothing.
		out, err := r.Exec.Call(newCtx(false), "dnsutil.hosts_append",
			value.MapOf("hostsfile", path, "ip_addr", "10.0.0.1", "entries", "web1,web2"))
		if err != nil {
			t.Fatal(err)
		}
		if msg, _ := out.(string); !strings.Contains(msg, "No additional hosts") {
			t.Errorf("a repeated append said %q", msg)
		}
	})

	t.Run("hosts_remove leaves comments and blank lines alone", func(t *testing.T) {
		path := write(t, "# keep me\n\n127.0.0.1 localhost lh\n10.0.0.1 web1\n")
		if _, err := r.Exec.Call(newCtx(false), "dnsutil.hosts_remove",
			value.MapOf("hostsfile", path, "entries", "web1")); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if !strings.Contains(string(body), "# keep me") {
			t.Errorf("the comment was lost:\n%s", body)
		}
		if strings.Contains(string(body), "web1") {
			t.Errorf("web1 survived:\n%s", body)
		}
		// The line held only that name, so the whole line goes.
		if strings.Contains(string(body), "10.0.0.1") {
			t.Errorf("a line with no names left was kept:\n%s", body)
		}
		if !strings.Contains(string(body), "localhost") {
			t.Errorf("an unrelated entry was lost:\n%s", body)
		}
	})

	t.Run("a list is taken as well as Salt's comma-separated string", func(t *testing.T) {
		path := write(t, "10.0.0.1 web1 web2\n")
		if _, err := r.Exec.Call(newCtx(false), "dnsutil.hosts_remove",
			value.MapOf("hostsfile", path, "entries", []any{"web1"})); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if strings.Contains(string(body), "web1") || !strings.Contains(string(body), "web2") {
			t.Errorf("hosts file = %q", body)
		}
	})
}
