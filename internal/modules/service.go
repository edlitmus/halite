package modules

import (
	"fmt"
	"runtime"
	"strings"
)

func init() {
	register("service.running", serviceRunning)
	register("service.dead", serviceDead)
}

// svcBackend abstracts a platform service manager.
type svcBackend struct {
	name    string
	running func(n string) bool
	start   func(n string) error
	stop    func(n string) error
	restart func(n string) error
	enable  func(n string) error
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
			enable: func(n string) error { return svcExec("sc", "config", n, "start=", "auto") },
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
	name := Str(args, "name", id)
	enable := Bool(args, "enable", false)
	watchTriggered := Bool(args, "__watch_changed", false)

	running := be.running(name)
	changes := map[string]string{}

	if running && !watchTriggered && !enable {
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

	if enable {
		if err := be.enable(name); err != nil {
			return resFail("enable %s: %v", name, err)
		}
		changes["enabled"] = "true"
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
