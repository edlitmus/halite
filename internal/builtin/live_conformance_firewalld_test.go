package builtin

import (
	"os"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `firewalld.present`, through SPEC 11.6's harness against a real
// firewalld.
//
// The zone is this case's own, `halite-conform`, with no interface
// bound to it and only TEST-NET-1 as a source, so nothing it does
// reaches traffic from anywhere real; live_firewalld_test.go says why
// that makes it safe on a host reached over SSH. The harness applies the
// state several times and each apply that changes anything reloads
// firewalld, which discards runtime-only changes on every zone -- that
// is Salt's behaviour and the state's, and it is why this is a lab case
// and not one to run on a node somebody has hand-tuned.
//
// The probe reads the *permanent* configuration, because that is what
// the state manages; the running firewall follows from the reload, which
// TestLiveFirewalldPresentConvergesAndPrunes checks.

const conformanceFirewalldZone = "halite-conform"

func firewalldConformanceCases() []liveCase {
	root := liveRoot()

	run := func(args ...string) (string, int) {
		res, err := root.Run(hexec.Command{Argv: append([]string{"firewall-cmd"}, args...), IgnoreExitCode: true})
		if err != nil {
			return err.Error(), -1
		}
		return strings.TrimSpace(res.Stdout), res.Code
	}
	zoneExists := func() bool {
		out, _ := run("--permanent", "--get-zones")
		for _, z := range strings.Fields(out) {
			if z == conformanceFirewalldZone {
				return true
			}
		}
		return false
	}
	removeZone := func() {
		if zoneExists() {
			run("--permanent", "--delete-zone="+conformanceFirewalldZone)
			run("--reload")
		}
		_ = os.Remove("/etc/firewalld/zones/" + conformanceFirewalldZone + ".xml.old")
	}
	probe := func() (string, error) {
		if !zoneExists() {
			return "no zone " + conformanceFirewalldZone, nil
		}
		var parts []string
		for _, flag := range []string{"--list-services", "--list-ports", "--list-sources", "--list-rich-rules"} {
			out, _ := run("--permanent", "--zone="+conformanceFirewalldZone, flag)
			parts = append(parts, flag+"="+strings.ReplaceAll(out, "\n", " | "))
		}
		return strings.Join(parts, "; "), nil
	}

	return []liveCase{{
		platforms: []string{"linux"},
		needs:     []string{"firewall-cmd"},
		unavailable: func(c *hexec.Context) string {
			res, err := c.Run(hexec.Command{Argv: []string{"firewall-cmd", "--state"}, IgnoreExitCode: true})
			if err != nil || res.Code != 0 {
				return "firewall-cmd is installed and firewalld is not running"
			}
			return ""
		},
		Conformance: states.Conformance{
			Name: "firewalld.present",
			Args: value.MapOf("name", conformanceFirewalldZone,
				"services", []any{"http", "https"},
				"ports", []any{"8080/tcp"},
				"sources", []any{"192.0.2.0/24"},
				// Unquoted on purpose: firewalld lists it back quoted, and
				// the idempotence phase is what shows the state finds it.
				"rich_rules", []any{"rule family=ipv4 source address=192.0.2.0/24 service name=ssh accept"}),
			Probe:   probe,
			Setup:   func() error { removeZone(); return nil },
			Cleanup: removeZone,
		},
	}}
}
