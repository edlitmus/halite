package modules

import (
	"fmt"
	"os"
	"strings"
)

func init() {
	register("cmd.run", cmdRun)
	register("cmd.wait", cmdWait)
}

// cmdRun runs a shell command, gated by creates / unless / onlyif.
//
//	rebuild_cache:
//	  cmd.run:
//	    - name: make cache
//	    - cwd: /srv/app
//	    - unless: test -f /srv/app/.cache
func cmdRun(c *Ctx, id string, args map[string]any) Result {
	command := Str(args, "name", id)
	cwd := Str(args, "cwd", "")
	creates := Str(args, "creates", "")
	unless := Str(args, "unless", "")
	onlyif := Str(args, "onlyif", "")
	env := List(args, "env")

	if creates != "" {
		if _, err := os.Stat(creates); err == nil {
			return resOK(fmt.Sprintf("%s exists, command not run", creates))
		}
	}
	if unless != "" {
		if _, _, rc, _ := shellRun(unless, cwd, env); rc == 0 {
			return resOK("unless condition met, command not run")
		}
	}
	if onlyif != "" {
		if _, _, rc, _ := shellRun(onlyif, cwd, env); rc != 0 {
			return resOK("onlyif condition not met, command not run")
		}
	}
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
