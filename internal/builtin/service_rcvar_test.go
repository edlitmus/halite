package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// What `service <name> rcvar` printed on beastie (FreeBSD 15.1-RELEASE-p5),
// captured 2026-10-16. The last line of each is followed by a blank one,
// as service(8) prints it.
const (
	rcvarIPFW = `# ipfw : Firewall, traffic shaper, packet scheduler, in-kernel NAT
#
firewall_enable="YES"
#   (default: "")

`
	rcvarRwho = `# rwhod : System status server
#
rwhod_enable="NO"
#   (default: "")

`
	rcvarSSHD = `# sshd : Secure Shell Daemon
#
sshd_enable="YES"
#   (default: "")

`
	// A script with no rcvar: comments only, exit 0.
	rcvarLdconfig = `# ldconfig : Configure the shared library cache
#

`
	// A script that does not exist: exit 1, on stderr.
	rcvarMissing = `nosuchsvc does not exist in /etc/rc.d or the local startup
directories (/usr/local/etc/rc.d), or is not executable
`
)

// **Enable and disable write the variable the script names** (issue 321,
// M-43). `ipfw` reads `firewall_enable` and `rwho` reads `rwhod_enable`;
// the provider used to write `ipfw_enable` and `rwho_enable`, which rc
// never reads. `sshd` is the ordinary case, kept so a fix that only
// handled the exceptions would be seen to break the rule.
func TestFreeBSDEnableWritesTheScriptsRCVar(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{"ipfw", rcvarIPFW, "firewall_enable"},
		{"rwho", rcvarRwho, "rwhod_enable"},
		{"sshd", rcvarSSHD, "sshd_enable"},
	}
	for _, tc := range cases {
		for _, op := range []struct {
			value string
			fn    func(freebsdRCProvider, *exec.Context, string) error
		}{
			{"YES", freebsdRCProvider.Enable},
			{"NO", freebsdRCProvider.Disable},
		} {
			c := newCtx(false)
			rec := &exec.RecordingRunner{Responses: map[string]exec.Result{
				"service " + tc.name + " rcvar": {Stdout: tc.out},
			}}
			c.Runner = rec
			if err := op.fn(freebsdRCProvider{}, c, tc.name); err != nil {
				t.Fatalf("%s %s: %v", tc.name, op.value, err)
			}
			want := []string{"service " + tc.name + " rcvar", "sysrc " + tc.want + "=" + op.value}
			if got := rec.RanCommands(); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s %s ran %q\nwant %q", tc.name, op.value, got, want)
			}
		}
	}
}

// A script with no rcvar and a script that does not exist are both
// refused, and neither reaches sysrc.
func TestFreeBSDEnableRefusesWithoutAnRCVar(t *testing.T) {
	cases := []struct {
		name string
		res  exec.Result
		want string
	}{
		{"ldconfig", exec.Result{Stdout: rcvarLdconfig}, "declares no rcvar"},
		{"nosuchsvc", exec.Result{Code: 1, Stderr: rcvarMissing}, "does not exist in /etc/rc.d"},
	}
	for _, tc := range cases {
		c := newCtx(false)
		rec := &exec.RecordingRunner{Responses: map[string]exec.Result{"service " + tc.name + " rcvar": tc.res}}
		c.Runner = rec
		err := freebsdRCProvider{}.Enable(c, tc.name)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error saying %q", tc.name, err, tc.want)
		}
		for _, ran := range rec.RanCommands() {
			if strings.HasPrefix(ran, "sysrc") {
				t.Errorf("%s: ran %q", tc.name, ran)
			}
		}
	}
}
