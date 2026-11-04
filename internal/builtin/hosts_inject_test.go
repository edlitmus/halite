package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **A hosts entry cannot add a line, a name or a comment the tree did not
// write** (issue 321, the M-45 class, item A12). host.present and
// dnsutil.hosts_append wrote the address and names as given, so a newline
// in any of them put another mapping in /etc/hosts -- the file every
// resolver on the machine reads first, which makes it a redirect for any
// name. A space or a tab splits one name into two, and a `#` turns the
// rest of the line into a comment, so those end a field the same way a
// newline ends the line.
//
// Each case runs for real and under --test, because a refusal that only
// the real run gave would let a dry run report a change it would not make.
func TestAHostsEntryThatEndsItsLineOrFieldIsRefused(t *testing.T) {
	const original = "127.0.0.1\tlocalhost\n"
	injections := []string{
		"\n10.6.6.6 evil.example",
		"\r\n10.6.6.6 evil.example",
		" evil.example",
		"\tevil.example",
		"#evil.example",
		"\x00evil.example",
		"\u0085evil.example",
	}
	withHosts := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "hosts")
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		old := HostsPath
		HostsPath = path
		t.Cleanup(func() { HostsPath = old })
		return path
	}
	unchanged := func(t *testing.T, path, what string) {
		t.Helper()
		if got, _ := os.ReadFile(path); string(got) != original {
			t.Errorf("%s changed the file:\n%s", what, got)
		}
	}

	for _, inj := range injections {
		for _, test := range []bool{false, true} {
			for _, field := range []string{"name", "names", "ip", "ip list"} {
				path := withHosts(t)
				args := value.MapOf("name", "web1.example", "ip", "10.0.0.1")
				switch field {
				case "name":
					args.Set("name", "web1.example"+inj)
				case "names":
					args.Set("names", []any{"web1.example", "web2.example" + inj})
				case "ip":
					args.Set("ip", "10.0.0.1"+inj)
				case "ip list":
					args.Set("ip", []any{"10.0.0.2", "10.0.0.1" + inj})
				}
				res := run(t, New(), "host.present", args, test)
				what := "host.present " + field + " " + strings.TrimSpace(inj)
				if res.Result == nil || *res.Result {
					t.Errorf("%s (test=%v) was not refused: %s", what, test, res.Comment)
				}
				unchanged(t, path, what)
			}

			for _, field := range []string{"ip_addr", "entries", "entries list"} {
				path := filepath.Join(t.TempDir(), "hosts")
				if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
					t.Fatal(err)
				}
				args := value.MapOf("hostsfile", path, "ip_addr", "10.0.0.1", "entries", "web1.example")
				switch field {
				case "ip_addr":
					args.Set("ip_addr", "10.0.0.1"+inj)
				case "entries":
					args.Set("entries", "web1.example,web2.example"+inj)
				case "entries list":
					args.Set("entries", []any{"web1.example", "web2.example" + inj})
				}
				c := newCtx(test)
				_, err := New().Exec.Call(c, "dnsutil.hosts_append", args)
				what := "dnsutil.hosts_append " + field + " " + strings.TrimSpace(inj)
				if err == nil {
					t.Errorf("%s (test=%v) was not refused", what, test)
				}
				unchanged(t, path, what)
			}
		}
	}

	// The ordinary entry still goes in, by both routes.
	path := withHosts(t)
	if res := run(t, New(), "host.present", value.MapOf("name", "web1.example", "ip", "fe80::1%eth0"), false); !res.Succeeded() {
		t.Fatalf("an ordinary entry was refused: %s", res.Comment)
	}
	if got, _ := os.ReadFile(path); string(got) != original+"fe80::1%eth0\tweb1.example\n" {
		t.Errorf("host.present wrote %q", got)
	}
	if _, err := New().Exec.Call(newCtx(false), "dnsutil.hosts_append",
		value.MapOf("hostsfile", path, "ip_addr", "10.0.0.9", "entries", "a.example, b.example")); err != nil {
		t.Fatalf("an ordinary hosts_append was refused: %v", err)
	}
	if got, _ := os.ReadFile(path); !strings.HasSuffix(string(got), "10.0.0.9\ta.example b.example\n") {
		t.Errorf("hosts_append wrote %q", got)
	}
}
