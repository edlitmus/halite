package modules

import (
	"fmt"
	"strings"
)

func init() {
	register("cmd.run", cmdRun)
	register("cmd.wait", cmdWait)
}

// cmdRun runs a shell command. The universal gates (creates, unless,
// onlyif) apply to all states and are evaluated by the engine before this
// function is called.
//
//	rebuild_cache:
//	  cmd.run:
//	    - name: make cache
//	    - cwd: /srv/app
//	    - creates: /srv/app/.cache
func cmdRun(c *Ctx, id string, args map[string]any) Result {
	command := Str(args, "name", id)
	cwd := Str(args, "cwd", "")
	env := List(args, "env")

	if c.Test {
		return resWould(fmt.Sprintf("command %q would be run", command))
	}

	stdout, stderr, rc, err := shellRun(command, cwd, env)
	changes := map[string]string{"rc": fmt.Sprintf("%d", rc)}
	if s := strings.TrimSpace(stdout); s != "" {
		changes["stdout"] = s
	}
	if s := strings.TrimSpace(stderr); s != "" {
		changes["stderr"] = s
	}
	if err != nil {
		return resFail("command failed to start: %v", err)
	}
	if rc != 0 {
		return Result{Ok: false, Changed: true, Comment: fmt.Sprintf("command %q exited %d", command, rc), Changes: changes}
	}
	return resChanged(fmt.Sprintf("command %q run", command), changes)
}

// cmdWait behaves like cmd.run but only fires when a watched state changed.
func cmdWait(c *Ctx, id string, args map[string]any) Result {
	if !Bool(args, "__watch_changed", false) {
		return resOK("not triggered (no watched changes)")
	}
	return cmdRun(c, id, args)
}
