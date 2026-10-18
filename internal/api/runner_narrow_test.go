package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// **A runner called through the API carries the operator's scope** (issue
// 321, M-56). The hub runs every API call as this service's certificate,
// so without the narrowing an operator scoped to web* reached through
// the API whatever the service's role reaches. The API sends the targets
// of the operator's own rules granting the runner; a rule with none sends
// an empty narrowing, which reaches no node.
func TestARunnerThroughTheAPICarriesTheOperatorsScope(t *testing.T) {
	for _, c := range []struct {
		name, rule string
		want       []string
	}{
		{"targeted", "    - target: 'web*'\n      runners: ['cache.*']\n", []string{"web*"}},
		{"untargeted", "    - runners: ['cache.*']\n", []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, hub := executeLab(t, "roles:\n  operator:\n"+c.rule+"bindings:\n  - principal: 'local:ed'\n    roles: ['operator']\n")
			hub.runnerReturn = `{}`
			out := l.login(t, "ed", "hunter2")
			res, body := l.post(t, PathRun, `{"client":"runner","fun":"cache.grains"}`, out.Token)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("the runner call answered %d: %s", res.StatusCode, body)
			}
			if len(hub.seen) == 0 {
				t.Fatal("nothing reached the hub")
			}
			var sent transport.RunnerRequest
			if err := json.Unmarshal([]byte(hub.seen[len(hub.seen)-1].body), &sent); err != nil {
				t.Fatal(err)
			}
			if sent.Narrow == nil {
				t.Fatalf("the call reached the hub with no narrowing: %s", hub.seen[len(hub.seen)-1].body)
			}
			if got := *sent.Narrow; len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
				t.Errorf("the narrowing sent is %q, want %q", got, c.want)
			}
		})
	}
}
