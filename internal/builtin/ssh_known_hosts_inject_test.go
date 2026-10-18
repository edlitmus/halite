package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **A newline cannot add a second trusted host key** (issue 321, section
// 5, the ssh_auth finding's twin). ssh_known_hosts.present wrote name, key
// and enc as given, and a newline in any one of them put a line for
// another host in known_hosts, which ssh then trusts.
func TestSSHKnownHostsRefusesAValueThatAddsALine(t *testing.T) {
	for _, field := range []string{"name", "key", "enc"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "known_hosts")
			args := value.MapOf("name", "web1.example", "key", newAuthBlob, "enc", "ssh-ed25519", "config", path)
			v, _ := args.GetString(field)
			args.Set(field, v.(string)+"\nevil.example ssh-ed25519 AAAAEVIL")
			res := run(t, New(), "ssh_known_hosts.present", args, false)
			if res.Succeeded() {
				t.Errorf("a %s holding a newline succeeded: %s", field, res.Comment)
			}
			if got, _ := os.ReadFile(path); strings.Contains(string(got), "evil.example") {
				t.Errorf("the second host reached the file:\n%s", got)
			}
		})
	}
}
