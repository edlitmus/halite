package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// Salt's host.present takes "a single IP or a list of IP addresses", and
// a tree reaches for the list as soon as it resolves a name: `dnsutil.A`
// returns one. The estate's hostname state writes
// `- ip: {{ ipv4 | default('127.0.1.1', true) }}` where ipv4 came from
// exactly that call, so the list form is the ordinary case rather than
// the exotic one.
func TestHostPresentTakesOneAddressOrSeveral(t *testing.T) {
	withHostsFile := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "hosts")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		old := HostsPath
		HostsPath = path
		t.Cleanup(func() { HostsPath = old })
		return path
	}

	t.Run("one address as a string", func(t *testing.T) {
		path := withHostsFile(t, "127.0.0.1 localhost\n")
		r := New()
		res := run(t, r, "host.present",
			value.MapOf("name", "web1.example", "ip", "10.0.0.1"), false)
		if !res.Succeeded() {
			t.Fatalf("%q", res.Comment)
		}
		body, _ := os.ReadFile(path)
		if !strings.Contains(string(body), "10.0.0.1") || !strings.Contains(string(body), "web1.example") {
			t.Errorf("hosts file = %q", body)
		}
	})

	t.Run("several addresses as a list", func(t *testing.T) {
		path := withHostsFile(t, "127.0.0.1 localhost\n")
		r := New()
		res := run(t, r, "host.present",
			value.MapOf("name", "web1.example", "ip", []any{"10.0.0.1", "10.0.0.2"}), false)
		if !res.Succeeded() {
			t.Fatalf("%q", res.Comment)
		}
		body, _ := os.ReadFile(path)
		for _, want := range []string{"10.0.0.1", "10.0.0.2"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s is missing from the hosts file: %q", want, body)
			}
		}
		// Both entries carry the name, which is what a list of addresses
		// for one host means.
		if n := strings.Count(string(body), "web1.example"); n != 2 {
			t.Errorf("the name appears %d times, want 2:\n%s", n, body)
		}
	})

	t.Run("and it converges on a second run", func(t *testing.T) {
		withHostsFile(t, "127.0.0.1 localhost\n")
		r := New()
		args := value.MapOf("name", "web1.example", "ip", []any{"10.0.0.1", "10.0.0.2"})
		if res := run(t, r, "host.present", args, false); !res.HasChanges() {
			t.Fatal("the first run changed nothing")
		}
		res := run(t, r, "host.present", args, false)
		if res.HasChanges() {
			t.Errorf("the second run reported a change: %q", res.Comment)
		}
	})

	t.Run("a single-element list is still one address", func(t *testing.T) {
		path := withHostsFile(t, "127.0.0.1 localhost\n")
		r := New()
		res := run(t, r, "host.present",
			value.MapOf("name", "web1.example", "ip", []any{"10.0.0.9"}), false)
		if !res.Succeeded() {
			t.Fatalf("%q", res.Comment)
		}
		body, _ := os.ReadFile(path)
		// The separator is the file's own, so this asks for the entry
		// rather than for a particular spelling of the whitespace.
		if !strings.Contains(string(body), "10.0.0.9") || !strings.Contains(string(body), "web1.example") {
			t.Errorf("hosts file = %q", body)
		}
		if n := strings.Count(string(body), "web1.example"); n != 1 {
			t.Errorf("the name appears %d times for one address:\n%s", n, body)
		}
	})
}
