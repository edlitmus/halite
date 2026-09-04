package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// serviceProvider is one init system. `service` is virtual in the same way
// `pkg` is: the SLS file says `service.running` and the right init system
// runs. SPEC section 15.2.
type serviceProvider interface {
	Name() string
	Available(c *exec.Context) bool
	Status(c *exec.Context, name string) (running bool, err error)
	Enabled(c *exec.Context, name string) (bool, error)
	Start(c *exec.Context, name string) error
	Stop(c *exec.Context, name string) error
	Restart(c *exec.Context, name string) error
	Reload(c *exec.Context, name string) error
	Enable(c *exec.Context, name string) error
	Disable(c *exec.Context, name string) error
}

var serviceProviders = []serviceProvider{
	systemdProvider{},
	freebsdRCProvider{},
	sysvProvider{},
	launchdProvider{},
}

// availableServiceProviders is the cross-platform list plus whatever
// this platform adds. A provider reached through an API rather than by
// running a binary cannot be compiled for another platform, so it cannot
// sit in the list above.
func availableServiceProviders() []serviceProvider {
	return append(platformServiceProviders(), serviceProviders...)
}

func pickServiceProvider(c *exec.Context) (serviceProvider, error) {
	providers := availableServiceProviders()
	for _, p := range providers {
		if p.Available(c) {
			return p, nil
		}
	}
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	return nil, fmt.Errorf("no init system was recognised on this node (%s); halite ships providers for %s",
		runtime.GOOS, strings.Join(names, ", "))
}

func registerService(r *Registries) {
	nameOnly := []signature.Param{req("name", signature.String, "The service.")}

	simple := func(function, doc string, run func(serviceProvider, *exec.Context, string) error) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: function, Doc: doc, Params: nameOnly,
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"}, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickServiceProvider(c)
				if err != nil {
					return nil, err
				}
				name := states.Str(args, "name", "")
				if c.Test {
					return true, nil
				}
				if err := run(p, c, name); err != nil {
					return nil, err
				}
				return true, nil
			},
		}
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "status",
				Doc: "Report whether a service is running.", Params: nameOnly,
				TestMode: signature.TestNotApplicable, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickServiceProvider(c)
				if err != nil {
					return nil, err
				}
				return p.Status(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "service", Function: "enabled",
				Doc: "Report whether a service starts at boot.", Params: nameOnly,
				TestMode: signature.TestNotApplicable, Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickServiceProvider(c)
				if err != nil {
					return nil, err
				}
				return p.Enabled(c, states.Str(args, "name", ""))
			},
		},
		simple("start", "Start a service.", serviceProvider.Start),
		simple("stop", "Stop a service.", serviceProvider.Stop),
		simple("restart", "Restart a service.", serviceProvider.Restart),
		simple("reload", "Reload a service's configuration.", serviceProvider.Reload),
		simple("enable", "Make a service start at boot.", serviceProvider.Enable),
		simple("disable", "Stop a service starting at boot.", serviceProvider.Disable),
	)

	runningParams := []signature.Param{
		nameParam("The service. Defaults to the state ID."),
		opt("enable", signature.Bool, nil, "Also manage whether the service starts at boot."),
		opt("reload", signature.Bool, false, "Reload rather than restart when a watch fires."),
	}

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "service", Function: "running",
				Doc:        "Ensure a service is running, and optionally enabled at boot.",
				Params:     runningParams,
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn:       serviceRunning,
			ModWatch: serviceModWatch,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "service", Function: "dead",
				Doc: "Ensure a service is not running, and optionally not enabled at boot.",
				Params: []signature.Param{
					nameParam("The service. Defaults to the state ID."),
					opt("enable", signature.Bool, nil, "Also manage whether the service starts at boot."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: serviceDead,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "service", Function: "enabled",
				Doc:        "Ensure a service starts at boot, without changing whether it is running now.",
				Params:     []signature.Param{nameParam("The service. Defaults to the state ID.")},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return serviceBootState(c, args, true)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "service", Function: "disabled",
				Doc:        "Ensure a service does not start at boot.",
				Params:     []signature.Param{nameParam("The service. Defaults to the state ID.")},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return serviceBootState(c, args, false)
			},
		},
	)
}

func serviceRunning(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickServiceProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No service provider is available: %v", err)), nil
	}
	name := states.Str(args, "name", "")
	changes := value.NewMap(2)

	running, err := p.Status(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The status of %s could not be read: %v", name, err)), nil
	}
	if !running {
		changes.Set(name, states.Change("stopped", "running"))
	}

	wantEnabled, manageBoot := boolArg(args, "enable")
	enabledNow := false
	if manageBoot {
		enabledNow, err = p.Enabled(c, name)
		if err != nil {
			return states.False(fmt.Sprintf("The boot state of %s could not be read: %v", name, err)), nil
		}
		if enabledNow != wantEnabled {
			changes.Set("enabled", states.Change(enabledNow, wantEnabled))
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The service %s is already in the requested state.", name)), nil
	}
	if c.Test {
		return states.WouldChange(describeServiceChange(name, !running, manageBoot && enabledNow != wantEnabled, wantEnabled, true), changes), nil
	}

	if !running {
		if err := p.Start(c, name); err != nil {
			return states.False(fmt.Sprintf("The service %s could not be started: %v", name, err)), nil
		}
	}
	if manageBoot && enabledNow != wantEnabled {
		if err := applyBootState(p, c, name, wantEnabled); err != nil {
			return states.False(fmt.Sprintf("The boot state of %s could not be set: %v", name, err)), nil
		}
	}
	return states.Changed(describeServiceChange(name, !running, manageBoot && enabledNow != wantEnabled, wantEnabled, true), changes), nil
}

func serviceDead(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickServiceProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No service provider is available: %v", err)), nil
	}
	name := states.Str(args, "name", "")
	changes := value.NewMap(2)

	running, err := p.Status(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The status of %s could not be read: %v", name, err)), nil
	}
	if running {
		changes.Set(name, states.Change("running", "stopped"))
	}

	wantEnabled, manageBoot := boolArg(args, "enable")
	enabledNow := false
	if manageBoot {
		enabledNow, err = p.Enabled(c, name)
		if err != nil {
			return states.False(fmt.Sprintf("The boot state of %s could not be read: %v", name, err)), nil
		}
		if enabledNow != wantEnabled {
			changes.Set("enabled", states.Change(enabledNow, wantEnabled))
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The service %s is already stopped.", name)), nil
	}
	if c.Test {
		return states.WouldChange(describeServiceChange(name, running, manageBoot && enabledNow != wantEnabled, wantEnabled, false), changes), nil
	}
	if running {
		if err := p.Stop(c, name); err != nil {
			return states.False(fmt.Sprintf("The service %s could not be stopped: %v", name, err)), nil
		}
	}
	if manageBoot && enabledNow != wantEnabled {
		if err := applyBootState(p, c, name, wantEnabled); err != nil {
			return states.False(fmt.Sprintf("The boot state of %s could not be set: %v", name, err)), nil
		}
	}
	return states.Changed(describeServiceChange(name, running, manageBoot && enabledNow != wantEnabled, wantEnabled, false), changes), nil
}

func serviceBootState(c *exec.Context, args *value.Map, want bool) (states.Result, error) {
	p, err := pickServiceProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No service provider is available: %v", err)), nil
	}
	name := states.Str(args, "name", "")
	now, err := p.Enabled(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The boot state of %s could not be read: %v", name, err)), nil
	}
	verb := "enabled"
	if !want {
		verb = "disabled"
	}
	if now == want {
		return states.True(fmt.Sprintf("The service %s is already %s at boot.", name, verb)), nil
	}
	changes := value.MapOf("enabled", states.Change(now, want))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The service %s would be %s at boot.", name, verb), changes), nil
	}
	if err := applyBootState(p, c, name, want); err != nil {
		return states.False(fmt.Sprintf("The boot state of %s could not be set: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("The service %s was %s at boot.", name, verb), changes), nil
}

func applyBootState(p serviceProvider, c *exec.Context, name string, want bool) error {
	if want {
		return p.Enable(c, name)
	}
	return p.Disable(c, name)
}

// serviceModWatch is the reaction a `watch` requisite triggers: the
// service is restarted, or reloaded when the state asked for that. This is
// the whole reason `watch` exists on a service.
func serviceModWatch(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickServiceProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("No service provider is available: %v", err)), nil
	}
	name := states.Str(args, "name", "")
	verb := "restarted"
	action := p.Restart
	if states.Bool(args, "reload", false) {
		verb, action = "reloaded", p.Reload
	}

	changes := value.MapOf(name, states.Change("running", verb))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("The service %s would be %s because a watched state changed.", name, verb), changes), nil
	}
	if err := action(c, name); err != nil {
		return states.False(fmt.Sprintf("The service %s could not be %s: %v", name, verb, err)), nil
	}
	return states.Changed(
		fmt.Sprintf("The service %s was %s because a watched state changed.", name, verb), changes), nil
}

func describeServiceChange(name string, runState, bootState, wantEnabled, wantRunning bool) string {
	var parts []string
	if runState {
		if wantRunning {
			parts = append(parts, "started")
		} else {
			parts = append(parts, "stopped")
		}
	}
	if bootState {
		if wantEnabled {
			parts = append(parts, "enabled at boot")
		} else {
			parts = append(parts, "disabled at boot")
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("The service %s is already in the requested state.", name)
	}
	return fmt.Sprintf("The service %s was %s.", name, strings.Join(parts, " and "))
}

// boolArg reads a tri-state boolean: set true, set false, or not given.
func boolArg(args *value.Map, name string) (val, given bool) {
	v, ok := args.Get(name)
	if !ok || v == nil {
		return false, false
	}
	return value.Truthy(v), true
}

// ---- providers ----

type systemdProvider struct{}

func (systemdProvider) Name() string { return "systemd_service" }

func (systemdProvider) Available(c *exec.Context) bool {
	if c.Which("systemctl") == "" {
		return false
	}
	// The binary being present is not enough: a container image often
	// carries systemctl with no running systemd behind it.
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

func (systemdProvider) Status(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"systemctl", "is-active", "--quiet", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	return res.Code == 0, nil
}

func (systemdProvider) Enabled(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"systemctl", "is-enabled", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	state := strings.TrimSpace(firstLine(res.Stdout))
	return state == "enabled" || state == "enabled-runtime" || state == "static", nil
}

func systemctl(c *exec.Context, verb, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"systemctl", verb, name}})
	return err
}

func (systemdProvider) Start(c *exec.Context, name string) error { return systemctl(c, "start", name) }
func (systemdProvider) Stop(c *exec.Context, name string) error  { return systemctl(c, "stop", name) }
func (systemdProvider) Restart(c *exec.Context, name string) error {
	return systemctl(c, "restart", name)
}
func (systemdProvider) Reload(c *exec.Context, name string) error {
	return systemctl(c, "reload", name)
}
func (systemdProvider) Enable(c *exec.Context, name string) error {
	return systemctl(c, "enable", name)
}
func (systemdProvider) Disable(c *exec.Context, name string) error {
	return systemctl(c, "disable", name)
}

// freebsdRCProvider drives FreeBSD's rc.d through service(8) and sysrc(8).
type freebsdRCProvider struct{}

func (freebsdRCProvider) Name() string { return "freebsd_service" }

func (freebsdRCProvider) Available(c *exec.Context) bool {
	return c.Which("service") != "" && c.Which("sysrc") != ""
}

func (freebsdRCProvider) Status(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"service", name, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	return res.Code == 0, nil
}

func (freebsdRCProvider) Enabled(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"service", name, "enabled"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	return res.Code == 0, nil
}

func (freebsdRCProvider) Start(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "start"}})
	return err
}

func (freebsdRCProvider) Stop(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "stop"}})
	return err
}

func (freebsdRCProvider) Restart(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "restart"}})
	return err
}

func (freebsdRCProvider) Reload(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "reload"}})
	return err
}

func (freebsdRCProvider) Enable(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"sysrc", name + "_enable=YES"}})
	return err
}

func (freebsdRCProvider) Disable(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"sysrc", name + "_enable=NO"}})
	return err
}

// sysvProvider is the fallback for a Linux node without systemd.
type sysvProvider struct{}

func (sysvProvider) Name() string { return "sysvinit_service" }

func (sysvProvider) Available(c *exec.Context) bool {
	_, err := os.Stat("/etc/init.d")
	return err == nil && c.Which("service") != ""
}

func (sysvProvider) Status(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"service", name, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	return res.Code == 0, nil
}

func (sysvProvider) Enabled(c *exec.Context, name string) (bool, error) {
	if c.Which("update-rc.d") == "" && c.Which("chkconfig") == "" {
		return false, fmt.Errorf("no tool to read the boot state of %s was found", name)
	}
	if c.Which("chkconfig") != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"chkconfig", "--list", name},
			IgnoreExitCode: true,
		})
		if err != nil {
			return false, err
		}
		return strings.Contains(res.Stdout, ":on"), nil
	}
	// Debian's update-rc.d has no query mode, so the runlevel links are
	// read directly, which is what the tool would write anyway.
	entries, err := os.ReadDir("/etc/rc3.d")
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "S") && strings.HasSuffix(e.Name(), name) {
			return true, nil
		}
	}
	return false, nil
}

func (sysvProvider) Start(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "start"}})
	return err
}

func (sysvProvider) Stop(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "stop"}})
	return err
}

func (sysvProvider) Restart(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "restart"}})
	return err
}

func (sysvProvider) Reload(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, "reload"}})
	return err
}

func (sysvProvider) Enable(c *exec.Context, name string) error {
	if c.Which("chkconfig") != "" {
		_, err := c.Run(exec.Command{Argv: []string{"chkconfig", name, "on"}})
		return err
	}
	_, err := c.Run(exec.Command{Argv: []string{"update-rc.d", name, "defaults"}})
	return err
}

func (sysvProvider) Disable(c *exec.Context, name string) error {
	if c.Which("chkconfig") != "" {
		_, err := c.Run(exec.Command{Argv: []string{"chkconfig", name, "off"}})
		return err
	}
	_, err := c.Run(exec.Command{Argv: []string{"update-rc.d", name, "disable"}})
	return err
}

// launchdProvider is macOS.
type launchdProvider struct{}

func (launchdProvider) Name() string { return "mac_service" }

func (launchdProvider) Available(c *exec.Context) bool { return c.Which("launchctl") != "" }

func (launchdProvider) Status(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "list", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	if res.Code != 0 {
		return false, nil
	}
	// Querying one label prints its job dump rather than the tabular
	// listing, and that dump carries a "PID" key only while the job is
	// actually running — a job that is loaded but idle (OnDemand, not
	// currently triggered) is found with exit 0 and no PID key at all, so
	// the exit code alone says "known to launchd", not "running".
	// Verified against real launchd output on this host.
	return strings.Contains(res.Stdout, `"PID" =`), nil
}

func (launchdProvider) Enabled(c *exec.Context, name string) (bool, error) {
	// launchctl keeps a persistent enable/disable override store,
	// independent of whether the job is currently loaded; a label named
	// there as disabled will not start at boot even if it is loaded right
	// now. A label absent from the store carries no override, and the
	// best answer this build has for it is whether launchd knows the job
	// at all, since there is no plist RunAtLoad reader here.
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "print-disabled", "system"},
		IgnoreExitCode: true,
	})
	if err == nil && res.Code == 0 {
		quoted := `"` + name + `"`
		for _, ln := range strings.Split(res.Stdout, "\n") {
			ln = strings.TrimSpace(ln)
			if !strings.HasPrefix(ln, quoted) {
				continue
			}
			return strings.HasSuffix(ln, "=> enabled"), nil
		}
	}
	return launchdProvider{}.Status(c, name)
}

func (launchdProvider) Start(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"launchctl", "start", name}})
	return err
}

func (launchdProvider) Stop(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"launchctl", "stop", name}})
	return err
}

func (launchdProvider) Restart(c *exec.Context, name string) error {
	if err := (launchdProvider{}).Stop(c, name); err != nil {
		return err
	}
	return launchdProvider{}.Start(c, name)
}

func (launchdProvider) Reload(c *exec.Context, name string) error {
	return launchdProvider{}.Restart(c, name)
}

func (launchdProvider) Enable(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"launchctl", "enable", "system/" + name}})
	return err
}

func (launchdProvider) Disable(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"launchctl", "disable", "system/" + name}})
	return err
}
