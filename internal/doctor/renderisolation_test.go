package doctor

import (
	"context"
	"strings"
	"testing"
)

// Each state the node can be in, judged: off is a pass, a requirement
// that holds is a pass that says so, and every way it can fail to hold
// is a fail naming what the child was seen to be.
func TestRenderIsolationJudgesWhatTheChildIsUnder(t *testing.T) {
	cases := []struct {
		name   string
		state  RenderIsolationState
		status Status
		says   []string
	}{
		{"off and not required", RenderIsolationState{}, Pass, []string{"off"}},
		{"a requirement it cannot read", RenderIsolationState{RequireError: `render_sandbox_require names "fs"`}, Fail, []string{`"fs"`}},
		{"required with the sandbox off", RenderIsolationState{Required: []string{"identity"}}, Fail, []string{"render_sandbox is off", "every render is refused"}},
		{"no child could be started", RenderIsolationState{Enabled: true, Started: true, StartError: "no such account"}, Fail, []string{"no such account"}},
		{
			"required and not in force",
			RenderIsolationState{Enabled: true, Started: true, Required: []string{"network"}, UID: 1001, GID: 1001,
				UpInterfaces: []string{"em0", "lo0"}, Unmet: []string{"network is required to be denied"}},
			Fail, []string{"every render is refused", "uid 1001", "em0, lo0 up"},
		},
		{
			"configured and not applied",
			RenderIsolationState{Enabled: true, Started: true, UID: 1001, Unenforced: []string{"`render_sandbox_user` is set to nobody and cannot be applied"}},
			Warn, []string{"cannot be applied", "uid 1001"},
		},
		{
			"required and in force",
			RenderIsolationState{Enabled: true, Started: true, Required: []string{"identity", "network"}, UID: 65534, GID: 65534},
			Pass, []string{"in force", "uid 65534", "no interface up", "filesystem: not restricted"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := RenderIsolation(c.state).Run(context.Background())
			if res.Status != c.status {
				t.Errorf("status %s, want %s: %s", res.Status, c.status, res.Detail)
			}
			for _, want := range c.says {
				if !strings.Contains(res.Detail, want) {
					t.Errorf("the detail does not say %q: %s", want, res.Detail)
				}
			}
			if res.Status != Pass && res.Remedy == "" {
				t.Errorf("a %s with no remedy line", res.Status)
			}
		})
	}
}
