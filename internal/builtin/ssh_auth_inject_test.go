package builtin

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **A newline cannot add a second authorized key** (issue 321, section
// 5). ssh_auth.present wrote `comment` and `options` as given, so a comment
// of "x\nssh-ed25519 <key> attacker" put a second key in the file, one
// that grants a login. shadow already refused the same thing; ssh did not.
// A value that can only have come through a template, or a pillar someone
// else controls, is enough.
func TestSSHAuthRefusesAValueThatAddsALine(t *testing.T) {
	const evil = "AAAAC3NzaC1lZDI1NTE5AAAAIGV2aWxrZXl0aGF0bXVzdG5vdGFwcGVhcg"
	for _, c := range []struct {
		name string
		args *value.Map
	}{
		{"comment", value.MapOf("name", newAuthBlob, "enc", "ssh-ed25519",
			"comment", "x\nssh-ed25519 "+evil+" attacker")},
		{"options", value.MapOf("name", newAuthBlob, "enc", "ssh-ed25519",
			"options", []any{"no-pty\nssh-ed25519 " + evil + " attacker"})},
		{"carriage return", value.MapOf("name", newAuthBlob, "enc", "ssh-ed25519",
			"comment", "x\rssh-ed25519 "+evil)},
		{"enc", value.MapOf("name", newAuthBlob, "enc", "ssh-ed25519 "+evil+" attacker\nssh-ed25519")},
		{"name", value.MapOf("name", newAuthBlob+"\nssh-ed25519 "+evil, "enc", "ssh-ed25519")},
	} {
		t.Run(c.name, func(t *testing.T) {
			keys := filepath.Join(t.TempDir(), "authorized_keys")
			writeFileForTest(t, keys, otherAuthKey+"\n", 0o600)
			c.args.Set("config", keys)
			res := run(t, New(), "ssh_auth.present", c.args, false)
			if res.Succeeded() {
				t.Errorf("a %s holding a newline succeeded: %s", c.name, res.Comment)
			}
			if got := readFile(t, keys); strings.Contains(got, evil) {
				t.Errorf("the second key reached the file:\n%s", got)
			}
		})
	}
}
