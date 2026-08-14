package modules

import (
	"fmt"
	"runtime"
	"strings"
)

func init() {
	register("service.running", serviceRunning)
	register("service.dead", serviceDead)
	register("service.enabled", serviceEnabled)
	register("service.disabled", serviceDisabled)
}

// svcBackend abstracts a platform service manager.
type svcBackend struct {
	name    string
	running func(n string) bool
	start   func(n string) error
	stop    func(n string) error
	restart func(n string) error
	enable  func(n string) error
	disable func(n string) error
	// enabled probes boot-time enablement; an error means the backend
	// cannot know, not that the service is disabled.
	enabled func(n string) (bool, error)
}

// ServiceRunning reports whether a service is up right now. Beacons use it
// to watch services without going through a state.
func ServiceRunning(name string) (bool, error) {
	backend, err := detectSvcBackend()
	if err != nil {
		return false, err
	}
	return backend.running(name), nil
}

func svcExec(argv ...string) error {
	_, errOut, rc, err := run(argv[0], argv[1:]...)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("%s exited %d: %s", strings.Join(argv, " "), rc, strings.TrimSpace(errOut))
	}
	return nil
}

// detectSvcBackend picks the service manager for the current platform:
// FreeBSD rc.d, systemd, launchd, or the Windows service control manager.
func detectSvcBackend() (*svcBackend, error) {
	switch runtime.GOOS {
	case "freebsd":
		return &svcBackend{
			name: "rc.d",
			running: func(n string) bool {
				_, _, rc, err := run("service", n, "onestatus")
				return err == nil && rc == 0
			},
			start:   func(n string) error { return svcExec("service", n, "onestart") },
			stop:    func(n string) error { return svcExec("service", n, "onestop") },
			restart: func(n string) error { return svcExec("service", n, "onerestart") },
			enable:  func(n string) error { return svcExec("sysrc", n+"_enable=YES") },
			disable: func(n string) error { return svcExec("sysrc", n+"_enable=NO") },
			enabled: func(n string) (bool, error) {
				out, _, rc, err := run("sysrc", "-n", n+"_enable")
				if err != nil {
					return false, err
				}
				// A non-zero exit means the rc.conf variable is unset.
				return rc == 0 && strings.EqualFold(strings.TrimSpace(out), "YES"), nil
			},
		}, nil
	case "darwin":
		return &svcBackend{
			name: "launchd",
			running: func(n string) bool {
				_, _, rc, err := run("launchctl", "list", n)
				return err == nil && rc == 0
			},
			start: func(n string) error { return svcExec("launchctl", "start", n) },
			stop:  func(n string) error { return svcExec("launchctl", "stop", n) },
			restart: func(n string) error {
				_ = svcExec("launchctl", "stop", n)
				return svcExec("launchctl", "start", n)
			},
			enable: func(n string) error {
				return fmt.Errorf("enable not yet implemented for launchd (load a plist instead)")
			},
			disable: func(n string) error {
				return fmt.Errorf("disable not yet implemented for launchd (unload the plist instead)")
			},
			enabled: func(n string) (bool, error) {
				return false, fmt.Errorf("launchd cannot report enablement")
			},
		}, nil
	case "windows":
		return &svcBackend{
			name: "scm",
			running: func(n string) bool {
				out, _, rc, err := run("sc", "query", n)
				return err == nil && rc == 0 && strings.Contains(out, "RUNNING")
			},
			start: func(n string) error { return svcExec("sc", "start", n) },
			stop:  func(n string) error { return svcExec("sc", "stop", n) },
			restart: func(n string) error {
				_ = svcExec("sc", "stop", n)
				return svcExec("sc", "start", n)
			},
			enable:  func(n string) error { return svcExec("sc", "config", n, "start=", "auto") },
			disable: func(n string) error { return svcExec("sc", "config", n, "start=", "disabled") },
			enabled: func(n string) (bool, error) {
				out, errOut, rc, err := run("sc", "qc", n)
				if err != nil {
					return false, err
				}
				if rc != 0 {
					return false, fmt.Errorf("sc qc %s exited %d: %s", n, rc, strings.TrimSpace(errOut))
				}
				return strings.Contains(out, "AUTO_START"), nil
			},
		}, nil
	default:
		if has("systemctl") {
			return &svcBackend{
				name: "systemd",
				running: func(n string) bool {
					_, _, rc, err := run("systemctl", "is-active", "--quiet", n)
					return err == nil && rc == 0
				},
				start:   func(n string) error { return svcExec("systemctl", "start", n) },
				stop:    func(n string) error { return svcExec("systemctl", "stop", n) },
				restart: func(n string) error { return svcExec("systemctl", "restart", n) },
				enable:  func(n string) error { return svcExec("systemctl", "enable", n) },
				disable: func(n string) error { return svcExec("systemctl", "disable", n) },
				enabled: func(n string) (bool, error) {
					// Disabled, static, and masked units all exit non-zero.
					_, _, rc, err := run("systemctl", "is-enabled", "--quiet", n)
					if err != nil {
						return false, err
					}
					return rc == 0, nil
				},
			}, nil
		}
		if has("service") {
			return &svcBackend{
				name: "sysvinit",
				running: func(n string) bool {
					_, _, rc, err := run("service", n, "status")
					return err == nil && rc == 0
				},
				start:   func(n string) error { return svcExec("service", n, "start") },
				stop:    func(n string) error { return svcExec("service", n, "stop") },
				restart: func(n string) error { return svcExec("service", n, "restart") },
				enable:  func(n string) error { return fmt.Errorf("enable not supported on sysvinit backend") },
				disable: func(n string) error { return fmt.Errorf("disable not supported on sysvinit backend") },
				enabled: func(n string) (bool, error) {
					return false, fmt.Errorf("sysvinit backend cannot report enablement")
				},
			}, nil
		}
		return nil, fmt.Errorf("no supported service manager found")
	}
}

// serviceRunning ensures a service is running (and optionally enabled).
// If a watched state changed, the service is restarted.
func serviceRunning(c *Ctx, id string, args map[string]any) Result {
	be, err := detectSvcBackend()
	if err != nil {
		return resFail("%v", err)
	}
	return applyServiceRunning(be, c, id, args)
}

// applyServiceRunning is serviceRunning with the backend injected, so tests
// can drive it without a real service manager.
func applyServiceRunning(be *svcBackend, c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	enable := Bool(args, "enable", false)
	watchTriggered := Bool(args, "__watch_changed", false)

	running := be.running(name)
	changes := map[string]string{}

	// Only enable when the service is provably not enabled; a backend that
	// cannot probe keeps the old always-enable behavior, but reports no
	// change for a call that may have been redundant.
	needEnable := false
	enableKnown := false
	if enable {
		isEnabled, err := be.enabled(name)
		enableKnown = err == nil
		needEnable = !enableKnown || !isEnabled
	}

	if running && !watchTriggered && !needEnable {
		return resOK(fmt.Sprintf("service %s is running", name))
	}
	if c.Test {
		switch {
		case !running:
			return resWould(fmt.Sprintf("service %s would be started", name))
		case watchTriggered:
			return resWould(fmt.Sprintf("service %s would be restarted (watch)", name))
		default:
			return resWould(fmt.Sprintf("service %s would be enabled", name))
		}
	}

	if needEnable {
		if err := be.enable(name); err != nil {
			return resFail("enable %s: %v", name, err)
		}
		if enableKnown {
			changes["enabled"] = "true"
		}
	}
	switch {
	case !running:
		if err := be.start(name); err != nil {
			return resFail("start %s: %v", name, err)
		}
		changes["started"] = "true"
	case watchTriggered:
		if err := be.restart(name); err != nil {
			return resFail("restart %s: %v", name, err)
		}
		changes["restarted"] = "true (watch)"
	}
	if len(changes) == 0 {
		return resOK(fmt.Sprintf("service %s is running", name))
	}
	return resChanged(fmt.Sprintf("service %s updated via %s", name, be.name), changes)
}

func serviceDead(c *Ctx, id string, args map[string]any) Result {
	be, err := detectSvcBackend()
	if err != nil {
		return resFail("%v", err)
	}
	name := Str(args, "name", id)
	if !be.running(name) {
		return resOK(fmt.Sprintf("service %s is not running", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("service %s would be stopped", name))
	}
	if err := be.stop(name); err != nil {
		return resFail("stop %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("service %s stopped", name), map[string]string{"stopped": "true"})
}

// serviceEnabled ensures a service starts at boot, without starting it
// now. Salt keeps this separate from service.running for the case a
// service.running's `enable: true` cannot express: a service that should
// come up on the next boot but must not be started by this run.
//
//	pf:
//	  service.enabled
func serviceEnabled(c *Ctx, id string, args map[string]any) Result {
	be, err := detectSvcBackend()
	if err != nil {
		return resFail("%v", err)
	}
	return applyServiceEnablement(be, c, id, args, true)
}

// serviceDisabled stops a service starting at boot, without stopping it
// now.
func serviceDisabled(c *Ctx, id string, args map[string]any) Result {
	be, err := detectSvcBackend()
	if err != nil {
		return resFail("%v", err)
	}
	return applyServiceEnablement(be, c, id, args, false)
}

// applyServiceEnablement is the body of both, with the backend injected so
// tests can drive it without a real service manager.
func applyServiceEnablement(be *svcBackend, c *Ctx, id string, args map[string]any, want bool) Result {
	name := Str(args, "name", id)

	// A backend that cannot report enablement fails the state rather than
	// acting blindly: without a probe, every run would report a change, and
	// this state exists precisely to be idempotent about boot config.
	isEnabled, err := be.enabled(name)
	if err != nil {
		return resFail("%s: %v", be.name, err)
	}
	if isEnabled == want {
		return resOK(fmt.Sprintf("service %s is %s at boot", name, enablementWord(want)))
	}
	if c.Test {
		return resWould(fmt.Sprintf("service %s would be %s at boot", name, enablementWord(want)))
	}

	apply := be.enable
	if !want {
		apply = be.disable
	}
	if err := apply(name); err != nil {
		return resFail("%s %s: %v", be.name, name, err)
	}
	return resChanged(fmt.Sprintf("service %s is now %s at boot", name, enablementWord(want)),
		map[string]string{name: enablementWord(want)})
}

func enablementWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
