package target

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A value that contains a colon is reachable: the target is read at every
// colon, as Salt's subdict_match does, and matches if any reading does.
// Only the last colon was tried, so `G@hwaddr_interfaces:eth0:aa:bb:...`
// looked for a path no node has. DIVERGENCE 5.537.
func TestAGrainValueContainingAColonCanBeTargeted(t *testing.T) {
	n := Node{ID: "a", Grains: value.MapOf(
		"hwaddr_interfaces", value.MapOf("eth0", "aa:bb:cc:dd:ee:ff"),
		"fqdn_ip6", []any{"fe80::1"},
		"os", "Ubuntu",
	), Pillar: value.MapOf("mac", "aa:bb")}
	for _, c := range []struct {
		kind Kind
		expr string
		want bool
	}{
		{Grain, "hwaddr_interfaces:eth0:aa:bb:cc:dd:ee:ff", true},
		{Grain, "hwaddr_interfaces:eth0:aa:*", true},
		{Grain, "fqdn_ip6:fe80::1", true},
		{GrainRegex, "hwaddr_interfaces:eth0:aa:bb.*", true},
		{Compound, "G@hwaddr_interfaces:eth0:aa:bb:cc:dd:ee:ff", true},
		{Pillar, "mac:aa:bb", true},
		{PillarRegex, "mac:aa:b+", true},
		// What must still not match.
		{Grain, "hwaddr_interfaces:eth0:aa:bb:cc:dd:ee:00", false},
		{Grain, "hwaddr_interfaces:eth1:aa:bb:cc:dd:ee:ff", false},
		{Grain, "os:Ubuntu", true},
		{Grain, "os:Debian", false},
		{Grain, "fqdn_ip6:fe80::2", false},
	} {
		mustMatch(t, c.kind, c.expr, n, c.want)
	}
	if _, err := Compile(Grain, ":x", nil); err == nil {
		t.Error("an empty grain name was accepted")
	}
	if _, err := Compile(GrainRegex, "os:(", nil); err == nil {
		t.Error("a pattern that does not compile was accepted")
	}
}
