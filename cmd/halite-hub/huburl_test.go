package main

import (
	"net/url"
	"strings"
	"testing"
)

// The address an operator command reaches the hub at, for every way of
// writing one. It decided "has a port" by whether the address held a
// colon, and took the port as everything from the last colon of
// `listen`, which is wrong for IPv6 on both sides: `--hub ::1` went out
// as https://::1, `--hub [::1]` as https://[::1] on port 443, and a
// `listen: [::1]` gave the port ":1]". DIVERGENCE 5.587.
func TestHubURLReadsIPv6(t *testing.T) {
	for _, c := range []struct {
		address, listen, want string
	}{
		{"localhost", ":4510", "https://localhost:4510"},
		{"hub.example", "0.0.0.0:8443", "https://hub.example:8443"},
		{"hub.example:9000", ":4510", "https://hub.example:9000"},
		{"192.0.2.7", ":4510", "https://192.0.2.7:4510"},
		{"https://hub.example:1234", ":4510", "https://hub.example:1234"},
		// A listen address with no port is the default port.
		{"localhost", "0.0.0.0", "https://localhost:4510"},
		{"localhost", "", "https://localhost:4510"},
		// IPv6, as a --hub.
		{"::1", ":4510", "https://[::1]:4510"},
		{"[::1]", ":4510", "https://[::1]:4510"},
		{"[::1]:8443", ":4510", "https://[::1]:8443"},
		{"2001:db8::7", "[::]:8443", "https://[2001:db8::7]:8443"},
		{"fe80::1%en0", ":4510", "https://[fe80::1%25en0]:4510"},
		// IPv6, as the listen address.
		{"localhost", "[::1]", "https://localhost:4510"},
		{"localhost", "[::1]:8443", "https://localhost:8443"},
		{"localhost", "[::]:8443", "https://localhost:8443"},
	} {
		got, err := hubURL(c.address, c.listen)
		if err != nil {
			t.Errorf("hubURL(%q, %q): %v", c.address, c.listen, err)
			continue
		}
		if got != c.want {
			t.Errorf("hubURL(%q, %q) = %q, want %q", c.address, c.listen, got, c.want)
		}
		// What the transport is handed has to be a URL it can read.
		if _, err := url.Parse(got); err != nil {
			t.Errorf("hubURL(%q, %q) = %q, which does not parse: %v", c.address, c.listen, got, err)
		}
	}

	for _, address := range []string{"host:", ":8443", "[::1", "not:an:address", "[]"} {
		got, err := hubURL(address, ":4510")
		if err == nil {
			t.Errorf("hubURL(%q) = %q; want it refused", address, got)
			continue
		}
		if !strings.Contains(err.Error(), "--hub") {
			t.Errorf("hubURL(%q): the refusal does not name --hub: %v", address, err)
		}
	}
}

// A --hub that cannot be read is a usage error, before the hub is
// reached. The control is the same command with a readable --hub, which
// in this root fails at the operator certificate instead.
func TestAnUnreadableHubIsAUsageError(t *testing.T) {
	root := t.TempDir()
	res := run(t, "run", "--root", root, "--hub", "not:an:address", "*", "test.ping")
	if res.code != 64 || !strings.Contains(res.stderr, "--hub") {
		t.Errorf("exited %d: %s; want 64 naming --hub", res.code, res.stderr)
	}
}
