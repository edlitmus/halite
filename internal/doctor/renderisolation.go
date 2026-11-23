package doctor

import (
	"context"
	"fmt"
	"strings"
)

// RenderIsolationState is what a node knows about its render sandbox,
// gathered by the node and judged here. Issue 762, DIVERGENCE 5.701.
type RenderIsolationState struct {
	// Enabled is `render_sandbox`.
	Enabled bool
	// Required is `render_sandbox_require` as written.
	Required []string
	// RequireError is why that list could not be read, when it could not.
	RequireError string
	// Started is whether a child was started to look at. When it was,
	// the fields below are its own report on itself.
	Started    bool
	StartError string
	UID, GID   int
	// UpInterfaces are the interfaces the child sees up; NetworkError is
	// why it could not list them.
	UpInterfaces []string
	NetworkError string
	// Unmet is each required control the child is not under.
	Unmet []string
	// Unenforced is each configured control this machine cannot apply,
	// whether or not it is required.
	Unenforced []string
}

// RenderIsolation reports what the render child is actually under,
// measured by starting one, and fails when a required control is not.
//
// "Effective" in the issue's sense: the uid is the one the child reads
// for itself, and the network is the interfaces it can see, so the line
// is about the process that will render and not about the settings that
// were meant to produce it. Filesystem restriction is reported as not
// built, which it is not, on any platform: the child reads what its
// account can read.
func RenderIsolation(state RenderIsolationState) Check {
	return Check{
		Name:  "render isolation",
		Roles: []string{RoleNode},
		Run: func(context.Context) Result {
			res := Result{Name: "render isolation"}
			required := strings.Join(state.Required, ", ")
			switch {
			case state.RequireError != "":
				res.Status = Fail
				res.Detail = state.RequireError
				res.Remedy = "Correct render_sandbox_require; it takes identity and network. Until then every render is refused."
				return res
			case !state.Enabled && len(state.Required) == 0:
				res.Status = Pass
				res.Detail = "off: templates and YAML are rendered in the node's own process, which is the default"
				return res
			case !state.Enabled:
				res.Status = Fail
				res.Detail = "render_sandbox_require is " + required + " and render_sandbox is off, so every render is refused"
				res.Remedy = "Set render_sandbox: true, or remove render_sandbox_require."
				return res
			case state.StartError != "":
				res.Status = Fail
				res.Detail = "the render child could not be started: " + state.StartError
				res.Remedy = "The node renders nothing in this state. The error names the cause; " +
					"a render_sandbox_user this machine cannot look up is the common one."
				return res
			}

			measured := fmt.Sprintf("the child runs as uid %d gid %d; %s; filesystem: not restricted, the child reads what its account can read",
				state.UID, state.GID, networkSeen(state))
			switch {
			case len(state.Unmet) > 0:
				res.Status = Fail
				res.Detail = "required " + required + " and not in force, so every render is refused: " +
					strings.Join(state.Unmet, "; ") + " (" + measured + ")"
				res.Remedy = "identity needs the node running as root with render_sandbox_user set to an unprivileged account; " +
					"network can be denied on Linux only, and only by a node running as root."
			case len(state.Unenforced) > 0:
				res.Status = Warn
				res.Detail = strings.Join(state.Unenforced, "; ") + " (" + measured + ")"
				res.Remedy = "Run the node as root for the account to be applied, or add identity to render_sandbox_require " +
					"to refuse rendering when it is not."
			default:
				res.Status = Pass
				res.Detail = measured
				if required != "" {
					res.Detail = "required " + required + ", in force: " + measured
				}
			}
			return res
		},
	}
}

func networkSeen(state RenderIsolationState) string {
	switch {
	case state.NetworkError != "":
		return "network: unknown, the child could not list its interfaces (" + state.NetworkError + ")"
	case len(state.UpInterfaces) == 0:
		return "network: no interface up in the child"
	default:
		return "network: the child sees " + strings.Join(state.UpInterfaces, ", ") + " up"
	}
}
