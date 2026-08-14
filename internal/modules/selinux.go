package modules

import (
	"fmt"
	"runtime"
	"strings"
)

func init() {
	register("selinux.mode", selinuxMode)
	register("selinux.boolean", selinuxBoolean)
}

// selinuxConfig is where the mode that survives a reboot is written.
const selinuxConfig = "/etc/selinux/config"

// selinuxMode sets the enforcement mode.
//
//	enforcing:
//	  selinux.mode
//
// The running mode and the configured one are set together, because they
// can differ and a state that changed only one of them would report
// success for a host that reverts on reboot — or that is already
// permissive right now.
//
// enforcing and permissive switch at run time; disabled does not, so that
// change is written to the configuration and reported as needing a reboot
// rather than pretended.
func selinuxMode(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS != "linux" {
		return resFail("selinux is Linux only")
	}
	wanted := strings.ToLower(Str(args, "name", id))
	switch wanted {
	case "enforcing", "permissive", "disabled":
	default:
		return resFail("mode %q is not one of enforcing, permissive, disabled", wanted)
	}
	if !has("getenforce") {
		return resFail("selinux tools not found (install policycoreutils)")
	}

	running := strings.ToLower(selinuxRunningMode())
	configured := selinuxConfiguredMode(readFile(selinuxConfig))
	needRunning := running != wanted && wanted != "disabled" && running != "disabled"
	needConfig := configured != wanted

	if !needRunning && !needConfig {
		return resOK(fmt.Sprintf("selinux is %s", wanted))
	}
	if c.Test {
		return resWould(fmt.Sprintf("selinux would be %s (running %s, configured %s)",
			wanted, orUnknown(running), orUnknown(configured)))
	}

	changes := map[string]string{}
	if needRunning {
		// setenforce speaks 1 and 0; it cannot disable a running kernel.
		flag := "0"
		if wanted == "enforcing" {
			flag = "1"
		}
		if _, err := pkgRun("setenforce", flag); err != nil {
			return resFail("setenforce: %v", err)
		}
		changes["running"] = orUnknown(running) + " -> " + wanted
	}
	if needConfig {
		if err := writeSelinuxMode(selinuxConfig, wanted); err != nil {
			return resFail("%v", err)
		}
		changes["configured"] = orUnknown(configured) + " -> " + wanted
	}
	comment := fmt.Sprintf("selinux set to %s", wanted)
	if wanted == "disabled" || running == "disabled" {
		// Neither direction across disabled takes effect until the kernel
		// is asked again, and saying so beats a green result that lies.
		comment += " (a reboot is needed for the running mode)"
	}
	return resChanged(comment, changes)
}

// selinuxRunningMode is what getenforce reports.
func selinuxRunningMode() string {
	out, ok := pkgQuery("getenforce")
	if !ok {
		return ""
	}
	return strings.TrimSpace(out)
}

// selinuxConfiguredMode reads SELINUX= from the config file.
func selinuxConfiguredMode(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if value, found := strings.CutPrefix(trimmed, "SELINUX="); found {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	return ""
}

// writeSelinuxMode sets SELINUX= in the config, leaving SELINUXTYPE and
// the comments — which explain the very values being set — as they are.
func writeSelinuxMode(path, mode string) error {
	lines := splitLines([]byte(readFile(path)))
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "SELINUX=") {
			lines[i] = "SELINUX=" + mode
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, "SELINUX="+mode)
	}
	if err := atomicWrite(path, joinLines(lines), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// selinuxBoolean sets an SELinux boolean, persistently unless the state
// says otherwise.
//
//	httpd_can_network_connect:
//	  selinux.boolean:
//	    - value: true
func selinuxBoolean(c *Ctx, id string, args map[string]any) Result {
	if runtime.GOOS != "linux" {
		return resFail("selinux is Linux only")
	}
	name := Str(args, "name", id)
	if !has("getsebool") {
		return resFail("selinux tools not found (install policycoreutils)")
	}
	want := Bool(args, "value", true)
	persist := Bool(args, "persist", true)

	out, ok := pkgQuery("getsebool", name)
	if !ok {
		return resFail("getsebool %s: no such boolean", name)
	}
	current, err := parseSebool(out)
	if err != nil {
		return resFail("%v", err)
	}
	if current == want {
		return resOK(fmt.Sprintf("%s is %s", name, seboolWord(want)))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would be %s", name, seboolWord(want)))
	}

	argv := []string{"setsebool"}
	if persist {
		// -P rewrites the policy, which is slow and permanent; without it
		// the change is lost on reboot, so it is the default.
		argv = append(argv, "-P")
	}
	argv = append(argv, name, seboolWord(want))
	if _, err := pkgRun(argv...); err != nil {
		return resFail("setsebool: %v", err)
	}
	return resChanged(fmt.Sprintf("%s set to %s", name, seboolWord(want)),
		map[string]string{name: seboolWord(current) + " -> " + seboolWord(want)})
}

// parseSebool reads getsebool's "name --> on" output.
func parseSebool(out string) (bool, error) {
	_, value, found := strings.Cut(out, "-->")
	if !found {
		return false, fmt.Errorf("cannot read getsebool output %q", strings.TrimSpace(out))
	}
	switch strings.TrimSpace(value) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, fmt.Errorf("cannot read getsebool value %q", strings.TrimSpace(value))
}

// seboolWord is both how getsebool prints a value and how setsebool takes
// one, which is why the state has one word for both.
func seboolWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
