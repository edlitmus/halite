package target

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A glob target is Salt's glob, fnmatch, not Go's path.Match.
//
// Salt matches a glob target with fnmatch, where `[!seq]` is "anything but"
// and `^` is an ordinary character. path.Match has it the other way round: its
// `[!1]` is a set of `!` and `1`, and negation is `[^1]`. So `run 'web[!1]*'`
// reached web1 and skipped web2 -- the nodes the operator meant to leave out,
// and only those -- and `web[^1]*` did what `web[!1]*` should. 5.472 found the
// same reversal in policy patterns and moved them to fnmatch; the target glob
// stayed on path.Match, on SPEC 8's say-so, and the policy's `target:` and a
// bootstrap token's node scope share it.
//
// The same gap makes `*` stop at `/` in a grain value, so `-G 'shell:*sh'`
// matched nothing against `/bin/sh`, where Salt matches it. DIVERGENCE 5.501.
func TestAGlobTargetIsSaltsGlob(t *testing.T) {
	for _, c := range []struct {
		pattern, id string
		want        bool
	}{
		{"web[!1]*", "web1.prod", false},
		{"web[!1]*", "web2.prod", true},
		// `^` is not negation in fnmatch: the set is `^` and `1`.
		{"web[^1]*", "web1.prod", true},
		{"web[^1]*", "web2.prod", false},
		{"web[12].prod", "web2.prod", true},
		{"web[1-2].prod", "web3.prod", false},
		{"[!d]*", "web1.prod", true},
		{"[!d]*", "db1.prod", false},
	} {
		n := Node{ID: c.id}
		mustMatch(t, Glob, c.pattern, n, c.want)
		if got := MatchGlob(c.pattern, c.id); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pattern, c.id, got, c.want)
		}
	}
}

func TestAGrainGlobCrossesASlash(t *testing.T) {
	n := Node{ID: "web1.prod", Grains: value.MapOf("shell", "/bin/sh", "path", "/usr/local/bin")}
	mustMatch(t, Grain, "shell:*sh", n, true)
	mustMatch(t, Grain, "shell:/bin/*", n, true)
	mustMatch(t, Grain, "path:/usr/*", n, true)
	mustMatch(t, Grain, "shell:*zsh", n, false)
}
