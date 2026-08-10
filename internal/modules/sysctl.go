package modules

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func init() {
	register("sysctl.present", sysctlPresent)
}

func defaultSysctlConf() string {
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd":
		return "/etc/sysctl.conf"
	case "linux":
		return "/etc/sysctl.d/99-halite.conf"
	}
	return "" // darwin: runtime only
}

// sysctlPresent ensures a sysctl is set at runtime and persisted.
//
//	kern.ipc.somaxconn:
//	  sysctl.present:
//	    - value: 1024
func sysctlPresent(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS == "windows" {
		return resFail("sysctl is not supported on Windows")
	}
	name := Str(args, "name", id)
	value := Str(args, "value", "")
	if value == "" {
		return resFail("sysctl.present requires a value")
	}
	persist := Bool(args, "persist", true)
	config := Str(args, "config", defaultSysctlConf())

	out, errOut, rc, err := run("sysctl", "-n", name)
	if err != nil || rc != 0 {
		return resFail("unknown sysctl %s: %s", name, strings.TrimSpace(errOut))
	}
	current := strings.TrimSpace(out)

	needRuntime := !sysctlValuesEqual(current, value)
	needPersist := false
	if persist && config != "" {
		needPersist = !confHasSetting(config, name, value)
	}

	if !needRuntime && !needPersist {
		return resOK(fmt.Sprintf("%s = %s (runtime and %s)", name, value, config))
	}
	if c.Test {
		var what []string
		if needRuntime {
			what = append(what, fmt.Sprintf("runtime %s -> %s", current, value))
		}
		if needPersist {
			what = append(what, "persist in "+config)
		}
		return resWould(fmt.Sprintf("%s would be updated: %s", name, strings.Join(what, "; ")))
	}

	changes := map[string]string{}
	if needRuntime {
		var argv []string
		if runtime.GOOS == "linux" {
			argv = []string{"sysctl", "-w", name + "=" + value}
		} else {
			argv = []string{"sysctl", name + "=" + value}
		}
		if err := svcExec(argv...); err != nil {
			return resFail("set %s: %v", name, err)
		}
		changes["runtime"] = current + " -> " + value
	}
	if needPersist {
		if err := confSetSetting(config, name, value); err != nil {
			return resFail("persist %s in %s: %v", name, config, err)
		}
		changes["config"] = config
	}
	return resChanged(fmt.Sprintf("sysctl %s updated", name), changes)
}

// sysctlValuesEqual compares sysctl values ignoring whitespace layout:
// `sysctl -n` prints multi-value keys tab-separated while SLS files
// conventionally separate them with spaces.
func sysctlValuesEqual(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// confHasSetting reports whether config already contains name=value.
func confHasSetting(config, name, value string) bool {
	b, err := os.ReadFile(config)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := splitConfLine(l)
		if ok && k == name && sysctlValuesEqual(v, value) {
			return true
		}
	}
	return false
}

// confSetSetting replaces or appends name=value in config.
func confSetSetting(config, name, value string) error {
	var lines []string
	if b, err := os.ReadFile(config); err == nil {
		lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	}
	replaced := false
	for i, l := range lines {
		if k, _, ok := splitConfLine(l); ok && k == name {
			lines[i] = name + "=" + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, name+"="+value)
	}
	return atomicWrite(config, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func splitConfLine(l string) (k, v string, ok bool) {
	l = strings.TrimSpace(l)
	if l == "" || strings.HasPrefix(l, "#") {
		return "", "", false
	}
	k, v, ok = strings.Cut(l, "=")
	return strings.TrimSpace(k), strings.TrimSpace(v), ok
}
