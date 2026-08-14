package modules

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func init() {
	register("network.system", networkSystem)
}

// networkSystem sets the host's own name.
//
//	web1.example.com:
//	  network.system
//
//	set-hostname:
//	  network.system:
//	    - hostname: web1.example.com
//
// Salt's network.system also writes the RHEL-era /etc/sysconfig/network
// networking switches; halite sets the hostname and nothing else, because
// interface configuration is a stated non-goal (see docs/salt-parity.md)
// and half a state would be worse than none.
//
// The change is applied now *and* recorded, so it survives a reboot: a
// hostname that reverts on the next boot is the failure mode this state
// exists to prevent.
func networkSystem(c *Ctx, id string, args map[string]any) Result {
	wanted := Str(args, "hostname", "")
	if wanted == "" {
		wanted = Str(args, "name", id)
	}
	if wanted == "" {
		return resFail("network.system needs a hostname")
	}
	if runtime.GOOS == "windows" {
		return resFail("network.system is not implemented on Windows")
	}

	current, err := currentHostname()
	if err != nil {
		return resFail("%v", err)
	}
	persisted := persistedHostname()
	if current == wanted && (persisted == "" || persisted == wanted) {
		return resOK(fmt.Sprintf("hostname is %s", wanted))
	}
	if c.Test {
		return resWould(fmt.Sprintf("hostname would change from %q to %s", current, wanted))
	}
	if err := setHostname(wanted); err != nil {
		return resFail("%v", err)
	}
	return resChanged(fmt.Sprintf("hostname set to %s", wanted),
		map[string]string{"hostname": orUnknown(current) + " -> " + wanted})
}

// currentHostname is the name the kernel reports right now.
func currentHostname() (string, error) {
	name, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("read hostname: %w", err)
	}
	return name, nil
}

// persistedHostname is the name the host will have after a reboot, or ""
// when this platform's file does not say. It is checked alongside the
// running name so that a host renamed by hand this boot still gets its
// configuration written.
func persistedHostname() string {
	if runtime.GOOS == "freebsd" {
		return rcConfValue(readFile("/etc/rc.conf"), "hostname")
	}
	return strings.TrimSpace(readFile("/etc/hostname"))
}

// rcConfValue reads one KEY="value" from rc.conf-shaped content.
func rcConfValue(content, key string) string {
	value := ""
	for _, line := range strings.Split(content, "\n") {
		name, rest, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || name != key {
			continue
		}
		// Later assignments win, the way the shell sourcing it would.
		value = strings.Trim(strings.TrimSpace(rest), `"'`)
	}
	return value
}

// setHostname applies the name and records it for the next boot.
func setHostname(name string) error {
	if has("hostnamectl") {
		// systemd writes /etc/hostname itself, so this is both halves.
		if _, err := pkgRun("hostnamectl", "set-hostname", name); err != nil {
			return fmt.Errorf("hostnamectl: %w", err)
		}
		return nil
	}
	if _, err := pkgRun("hostname", name); err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	if runtime.GOOS == "freebsd" {
		if _, err := pkgRun("sysrc", "hostname="+name); err != nil {
			return fmt.Errorf("sysrc: %w", err)
		}
		return nil
	}
	if runtime.GOOS == "darwin" {
		// The running name is set above; these are the two macOS keeps.
		for _, key := range []string{"HostName", "LocalHostName"} {
			if _, err := pkgRun("scutil", "--set", key, name); err != nil {
				return fmt.Errorf("scutil --set %s: %w", key, err)
			}
		}
		return nil
	}
	if err := atomicWrite("/etc/hostname", []byte(name+"\n"), 0o644); err != nil {
		return fmt.Errorf("write /etc/hostname: %w", err)
	}
	return nil
}
