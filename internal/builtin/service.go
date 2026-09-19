package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

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
	// OpenRC before sysvinit, and the order is load-bearing: OpenRC
	// keeps its init scripts in /etc/init.d too, so the sysvinit
	// provider's "is there an /etc/init.d" test matches an Alpine or
	// Gentoo machine as readily as a Devuan one -- and would then drive
	// it with `update-rc.d`, which is Debian's and is not there.
	openrcProvider{},
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
		return states.WouldChange(describeServiceChange(name, !running, manageBoot && enabledNow != wantEnabled, wantEnabled, true, true), changes), nil
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
	return states.Changed(describeServiceChange(name, !running, manageBoot && enabledNow != wantEnabled, wantEnabled, true, false), changes), nil
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
		return states.WouldChange(describeServiceChange(name, running, manageBoot && enabledNow != wantEnabled, wantEnabled, false, true), changes), nil
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
	return states.Changed(describeServiceChange(name, running, manageBoot && enabledNow != wantEnabled, wantEnabled, false, false), changes), nil
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

// describeServiceChange says what happened, or what would. The tense is
// an argument because this backs both, and it said "was" for both: a
// `--test` run reported "The service salt-minion was started" for a // lexicon:allow — a real service name
// service it had not touched. A dry run that reads like it acted is the
// one sentence a test mode must not produce -- it was read that way on a
// host where starting that service purges the compilers.
func describeServiceChange(name string, runState, bootState, wantEnabled, wantRunning, would bool) string {
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
	if would {
		return fmt.Sprintf("The service %s would be %s.", name, strings.Join(parts, " and "))
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
	if b, ok := dialOrNil(c); ok {
		state, err := b.activeState(withServiceSuffix(name))
		b.Close()
		if err == nil {
			return state == "active" || state == "reloading", nil
		}
		// A read that reached systemd and still failed -- a malformed
		// unit name -- is rare. Fall through to systemctl rather than
		// fail the caller on it.
	}
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
	if b, ok := dialOrNil(c); ok {
		state, err := b.unitFileState(withServiceSuffix(name))
		b.Close()
		if err == nil {
			return systemdEnabledState(state), nil
		}
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"systemctl", "is-enabled", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	return systemdEnabledState(strings.TrimSpace(firstLine(res.Stdout))), nil
}

// systemdEnabledState reads the unit-file states that mean "this unit
// starts at boot" -- the same set `systemctl is-enabled` exits 0 for.
func systemdEnabledState(state string) bool {
	switch state {
	case "enabled", "enabled-runtime", "static":
		return true
	}
	return false
}

func systemctl(c *exec.Context, verb, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"systemctl", verb, name}})
	return err
}

// systemdJob issues a start/stop-class verb over D-Bus, waiting for the
// job to finish, or falls back to `systemctl` when the bus is
// unreachable. verb is the D-Bus member; shellVerb is the systemctl
// subcommand.
func systemdJob(c *exec.Context, member, shellVerb, name string) error {
	if b, ok := dialOrNil(c); ok {
		defer b.Close()
		return b.runJob(member, withServiceSuffix(name))
	}
	return systemctl(c, shellVerb, name)
}

func (systemdProvider) Start(c *exec.Context, name string) error {
	return systemdJob(c, "StartUnit", "start", name)
}
func (systemdProvider) Stop(c *exec.Context, name string) error {
	return systemdJob(c, "StopUnit", "stop", name)
}
func (systemdProvider) Restart(c *exec.Context, name string) error {
	return systemdJob(c, "RestartUnit", "restart", name)
}
func (systemdProvider) Reload(c *exec.Context, name string) error {
	return systemdJob(c, "ReloadUnit", "reload", name)
}

func (systemdProvider) Enable(c *exec.Context, name string) error {
	if b, ok := dialOrNil(c); ok {
		defer b.Close()
		return b.enable(withServiceSuffix(name))
	}
	return systemctl(c, "enable", name)
}

func (systemdProvider) Disable(c *exec.Context, name string) error {
	if b, ok := dialOrNil(c); ok {
		defer b.Close()
		return b.disable(withServiceSuffix(name))
	}
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

// freebsdRCVerb picks between `start` and `onestart`, and between their
// stop, restart and reload counterparts.
//
// **An rcvar is a permission as well as a boot setting.** An rc.d script
// that declares one refuses a plain `start` until rc.conf says YES --
// and it refuses it the way rc expects at boot, by printing an
// explanation and **exiting 0**, because a disabled service being
// skipped is the normal case there rather than a failure. Measured on
// the FreeBSD leg: `service.start` on a service rc.conf had not enabled
// returned no error and started nothing, so halite reported a service
// started, reported a change, and changed nothing -- on every run.
// DIVERGENCE 5.123.
//
// rc.subr's own answer to "do it now, whatever rc.conf says" is the
// `one` prefix, which is what its refusal tells the operator to use. So
// that is what this runs where rc.conf has not enabled the service, and
// it is what `service.running` means on every other platform: systemd
// starts a disabled unit when it is asked to.
//
// The decision is made from `service <name> enabled`'s exit status
// rather than from the refusal's prose. A script with no rcvar at all
// is always startable, and `one`-prefixed verbs work on those too, so
// guessing wrong in that direction costs nothing.
func freebsdRCVerb(c *exec.Context, name, verb string) string {
	enabled, err := freebsdRCProvider{}.Enabled(c, name)
	if err == nil && !enabled {
		return "one" + verb
	}
	return verb
}

func (freebsdRCProvider) Start(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, freebsdRCVerb(c, name, "start")}})
	return err
}

func (freebsdRCProvider) Stop(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, freebsdRCVerb(c, name, "stop")}})
	return err
}

func (freebsdRCProvider) Restart(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, freebsdRCVerb(c, name, "restart")}})
	return err
}

func (freebsdRCProvider) Reload(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"service", name, freebsdRCVerb(c, name, "reload")}})
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
	//
	// **In the runlevel this machine boots to**, which is not always 3.
	// This read rc3.d unconditionally and was demonstrated wrong on a
	// Debian booting to runlevel 2: `update-rc.d` places links from the
	// script's own `Default-Start` header, so a service declaring
	// `Default-Start: 2` has a link in rc2.d and nowhere else, and this
	// reported a service that does start at boot as one that does not.
	// A `service.enabled` state then rewrote the links and reported a
	// change on every run. DIVERGENCE 5.126.
	dir := "/etc/rc" + sysvDefaultRunlevel(c) + ".d"
	entries, err := os.ReadDir(dir)
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

// sysvDefaultRunlevel is the runlevel this machine boots to, which is
// the only runlevel "does it start at boot" can be a question about.
//
// It is asked rather than assumed because distributions disagree: Debian
// boots to 2 and RHEL's sysvinit era booted to 3, and `update-rc.d`
// places links from the init script's own `Default-Start` header — so a
// script declaring `Default-Start: 2` has a link in rc2.d and in no
// other, and a reader looking anywhere else finds nothing and reports a
// service that does start at boot as one that does not.
//
// `/etc/inittab` is the authority where there is one. Where there is
// not, the runlevel the machine is in now is the best available answer,
// and `runlevel` prints it as "<previous> <current>". Three is the last
// resort because it is what this code assumed before it asked.
func sysvDefaultRunlevel(c *exec.Context) string {
	if data, err := os.ReadFile("/etc/inittab"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Split(line, ":")
			if len(fields) >= 3 && fields[2] == "initdefault" && fields[1] != "" {
				return fields[1]
			}
		}
	}
	if c.Which("runlevel") != "" {
		res, err := c.Run(exec.Command{Argv: []string{"runlevel"}, IgnoreExitCode: true})
		if err == nil && res.Code == 0 {
			fields := strings.Fields(res.Stdout)
			if len(fields) == 2 && fields[1] != "unknown" {
				return fields[1]
			}
		}
	}
	return "3"
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

// openrcProvider is OpenRC, which is Alpine's init and Gentoo's, and an
// option on Devuan and on Artix.
//
// # Why it is not the sysvinit provider with different words
//
// OpenRC keeps init *scripts* in /etc/init.d, which is what
// `sysvProvider.Available` looks for — so before this existed, an Alpine
// node either fell through to that provider, whose `update-rc.d` and
// `chkconfig` are Debian's and RedHat's and exist on neither Alpine nor
// Gentoo, or found no provider at all and every `service.*` function
// failed with "no init system was recognised on this node". Which of the
// two it was depended on whether the machine happened to have a
// `service` shim.
//
// Runlevels are the other half. sysvinit's boot state is a symlink in
// /etc/rc3.d and OpenRC's is membership of a named runlevel, which
// `rc-update` maintains and prints. This provider asks `rc-update`
// rather than reading /etc/runlevels, for the reason the ledger keeps
// giving: the tool's own answer is the one that stays true when the
// layout changes.
type openrcProvider struct{}

func (openrcProvider) Name() string { return "openrc_service" }

func (openrcProvider) Available(c *exec.Context) bool {
	return c.Which("rc-service") != "" && c.Which("rc-update") != ""
}

func (openrcProvider) Status(c *exec.Context, name string) (bool, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"rc-service", name, "status"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return false, err
	}
	// OpenRC exits 0 for a started service and something else for every
	// other state it knows -- stopped, crashed, inactive -- and a
	// crashed service is not a running one.
	return res.Code == 0, nil
}

// openrcRunlevels reports which runlevels a service is in, out of
// `rc-update show`'s own listing.
//
// The format is the service name, right-aligned in a column of spaces,
// then `|`, then the runlevels it belongs to:
//
//	crond |      default
//	devfs | sysinit
//
// A service that is in no runlevel is absent from the default listing
// entirely, so an empty answer and "not enabled" are the same fact.
func openrcRunlevels(c *exec.Context, name string) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"rc-update", "show"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	for _, ln := range strings.Split(res.Stdout, "\n") {
		service, levels, found := strings.Cut(ln, "|")
		if !found || strings.TrimSpace(service) != name {
			continue
		}
		return strings.Fields(levels), nil
	}
	return nil, nil
}

func (openrcProvider) Enabled(c *exec.Context, name string) (bool, error) {
	levels, err := openrcRunlevels(c, name)
	if err != nil {
		return false, err
	}
	return len(levels) > 0, nil
}

func (openrcProvider) Start(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"rc-service", name, "start"}})
	return err
}

func (openrcProvider) Stop(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"rc-service", name, "stop"}})
	return err
}

func (openrcProvider) Restart(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"rc-service", name, "restart"}})
	return err
}

func (openrcProvider) Reload(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"rc-service", name, "reload"}})
	return err
}

// Enable puts the service in `default`, which is OpenRC's spelling of
// the runlevel a machine reaches when it has finished booting --
// systemd's multi-user.target, and what `rc-update add` assumes when
// nobody names one. It is named here anyway, because a state file that
// says "start at boot" should not depend on which runlevel the shell
// running halite happens to be in.
func (openrcProvider) Enable(c *exec.Context, name string) error {
	_, err := c.Run(exec.Command{Argv: []string{"rc-update", "add", name, "default"}})
	return err
}

// Disable takes the service out of **every** runlevel it is in, rather
// than out of `default` alone.
//
// "Does not start at boot" is the promise `service.disabled` makes, and
// a service in `boot` or `sysinit` starts at boot just as surely as one
// in `default`. Removing only the runlevel Enable adds would leave a
// state that reported success and changed nothing on any machine where
// the service had been enabled by hand somewhere else.
func (openrcProvider) Disable(c *exec.Context, name string) error {
	levels, err := openrcRunlevels(c, name)
	if err != nil {
		return err
	}
	for _, level := range levels {
		if _, err := c.Run(exec.Command{Argv: []string{"rc-update", "del", name, level}}); err != nil {
			return err
		}
	}
	return nil
}

// List is every init script OpenRC knows, which is what `rc-service
// --list` prints, one per line.
func (openrcProvider) List(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{Argv: []string{"rc-service", "--list"}})
	if err != nil {
		return nil, err
	}
	return sortedLines(res.Stdout), nil
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

// launchdSpawnLimit bounds the wait for launchd to honour a start. It
// matches the systemd provider's D-Bus deadline rather than being tuned
// to the throttle below, because the two providers are answering the
// same question -- has the init system finished doing what it was asked
// -- and a node whose job deadline is shorter caps it either way.
const launchdSpawnLimit = 90 * time.Second

func (launchdProvider) Start(c *exec.Context, name string) error {
	// The spawn count is read *before* the start, because it is the only
	// thing that distinguishes "launchd has run this job again" from
	// "this job was already running" and from "it ran and exited before
	// anybody looked". A pid cannot: an on-demand job that does its work
	// in fifty milliseconds is never observed with one.
	before, haveBaseline := launchdSpawnCount(c, name)
	if _, err := c.Run(exec.Command{Argv: []string{"launchctl", "start", name}}); err != nil {
		return err
	}
	if !haveBaseline {
		// No baseline, no wait. `launchctl print` needs root and the
		// system domain, and inventing a wait without something to
		// compare against would either return at once -- which is what
		// not waiting does anyway -- or block on a job that is already
		// where it should be.
		return nil
	}
	return launchdAwaitSpawn(c, name, before)
}

// launchdAwaitSpawn waits until launchd has actually spawned the job
// again.
//
// **`launchctl start` returns when the request is queued, not when the
// job is running**, and launchd throttles a respawn: a job asked to
// start again within ten seconds of its last spawn is held until that
// window passes, with `launchctl print` reporting `state = spawn
// scheduled` in the meantime. Measured on macOS 15 on the `macos` leg:
// `service.restart` returned in five milliseconds and the job came back
// 10.03 seconds later (DIVERGENCE 5.122).
//
// Without this, `service.restart` reports a service restarted while it
// is down, for ten seconds -- which is the same shape as a state whose
// "make it converge" and "is it converged?" disagree, one layer out: the
// module's answer and the machine's differ for long enough that anything
// reading the node in between is told the wrong thing.
func launchdAwaitSpawn(c *exec.Context, name string, before int) error {
	deadline := time.Now().Add(launchdSpawnLimit)
	if c.Ctx != nil {
		if jobDeadline, ok := c.Ctx.Deadline(); ok && jobDeadline.Before(deadline) {
			deadline = jobDeadline
		}
	}
	for {
		runs, ok := launchdSpawnCount(c, name)
		if !ok {
			// launchd answered a moment ago and does not now: the job
			// has been unloaded under us, and no respawn is coming.
			return fmt.Errorf("launchd stopped reporting %s while waiting for it to start", name)
		}
		if runs > before {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("launchd scheduled %s to start and had not done so after %s; "+
				"it throttles a respawn to ten seconds and this was longer",
				name, launchdSpawnLimit)
		}
		if c.Ctx != nil {
			select {
			case <-c.Ctx.Done():
				return c.Ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// launchdSpawnCount reads how many times launchd has spawned a job out
// of `launchctl print`, which is the only place it is reported --
// `launchctl list <label>`, which the rest of this provider reads, has
// no such key. A label launchd does not know, or a domain this account
// cannot print, answers false rather than zero, because "never spawned"
// and "cannot see it" are different facts.
func launchdSpawnCount(c *exec.Context, name string) (int, bool) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"launchctl", "print", "system/" + name},
		IgnoreExitCode: true,
	})
	if err != nil || res.Code != 0 {
		return 0, false
	}
	for _, ln := range strings.Split(res.Stdout, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "runs = ") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, "runs = ")))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	// No `runs` line is *not* a readable zero. It is a job launchd has
	// never spawned, or a release that words this differently, and
	// either way there is nothing to compare a later reading against --
	// so the caller does not wait, which is what it did before this
	// existed. A job that has never run is also the one job that cannot
	// be throttled, so nothing is lost by it.
	return 0, false
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
