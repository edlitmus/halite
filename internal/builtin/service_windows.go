package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/winsvc"
)

// windowsProvider is the service control manager, and it is what
// `service.running` reaches on this platform.
//
// There was no provider here at all: a Windows node answered every
// service call with "no init system was recognised on this node",
// naming four providers and none of them for it, so a node could not be
// told to start anything.
//
// It speaks to the manager through its API rather than by running sc.exe
// and parsing what comes back — the same choice SPEC 15.2 makes for
// systemd, and for the same reason. internal/winsvc holds the client.
type windowsProvider struct{}

func (windowsProvider) Name() string { return "windows" }

// Available is true on Windows and asks nothing.
//
// Every other provider looks for a binary, because a machine may have
// systemd and sysvinit both installed and the question is which one is
// running. There is exactly one service manager here, it is part of the
// operating system, and a Windows without it is not a Windows that
// booted.
func (windowsProvider) Available(c *exec.Context) bool { return true }

func (windowsProvider) Status(c *exec.Context, name string) (bool, error) {
	return winsvc.Running(name)
}

func (windowsProvider) Enabled(c *exec.Context, name string) (bool, error) {
	return winsvc.Enabled(name)
}

func (windowsProvider) Start(c *exec.Context, name string) error { return winsvc.Start(name) }

func (windowsProvider) Stop(c *exec.Context, name string) error { return winsvc.Stop(name) }

func (windowsProvider) Restart(c *exec.Context, name string) error { return winsvc.Restart(name) }

// Reload has no counterpart here.
//
// The manager has a control code for it, and almost nothing implements
// it: a service that is sent one it does not accept returns an error
// that reads like a failure of the state rather than of the request. So
// it is refused by name, and the message says what to do instead.
// Silently restarting would be a bigger change than the state asked for.
func (windowsProvider) Reload(c *exec.Context, name string) error {
	return fmt.Errorf("the Windows service manager has no reload that services implement; "+
		"use service.restart for %s, which is what the services console does", name)
}

// Enable sets a service to start with the machine.
//
// Automatic, not delayed. A tree that wants delayed start says so with
// win_service.set_start_type, which is the module SPEC 15.3 puts this
// in; `service.enabled` has one meaning everywhere else and this keeps
// it.
func (windowsProvider) Enable(c *exec.Context, name string) error {
	return winsvc.SetStartType(name, "auto")
}

// Disable stops a service from starting with the machine.
//
// `disabled`, not `manual`. They differ: a manual service still starts
// when something asks for it, and `service.dead` with `enable: False`
// means the machine must not bring it up — which manual does not
// guarantee.
func (windowsProvider) Disable(c *exec.Context, name string) error {
	return winsvc.SetStartType(name, "disabled")
}

// List implements serviceLister.
func (windowsProvider) List(c *exec.Context) ([]string, error) { return winsvc.List() }
