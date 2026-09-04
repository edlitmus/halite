//go:build windows

package winsvc

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// settleTimeout bounds how long a start or a stop is waited on.
//
// A service that reports START_PENDING and never arrives is a real
// condition — a dependency that is not there, a binary that is missing —
// and a state run that blocked on it forever would take the highstate
// with it. Long enough for a database to open its files, short enough
// that a wedged service is reported rather than waited on.
const settleTimeout = 90 * time.Second

// pollInterval is how often a pending service is asked again. The
// manager offers a hint for this and most services do not set it, so a
// fixed interval that is short enough to feel immediate is better than
// one derived from a number that is usually zero.
const pollInterval = 200 * time.Millisecond

// Status is what the manager knows about one service.
type Status struct {
	Name string
	// DisplayName is the name the services console shows, which is what
	// an operator recognises and not what a state names.
	DisplayName string
	// State is "running", "stopped", or one of the pending states.
	State string
	// StartType is "auto", "auto_delayed", "manual", "disabled",
	// "boot" or "system".
	StartType string
	// PID is the process, or zero when it is not running.
	PID uint32
	// BinaryPath is the command the manager runs.
	BinaryPath string
	// Account is the identity it runs as.
	Account string
	// Description is the prose the console shows.
	Description string
}

// manager is an open handle to the service control manager.
type manager struct{ handle windows.Handle }

// openManager connects to the local manager with the access asked for.
//
// Read access is separated from write access on purpose: `service.status`
// on a node that is not running as an administrator should answer, and it
// does, because querying needs only SC_MANAGER_CONNECT.
func openManager(write bool) (*manager, error) {
	access := uint32(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	if write {
		access |= windows.SC_MANAGER_ALL_ACCESS
	}
	h, err := windows.OpenSCManager(nil, nil, access)
	if err != nil {
		return nil, fmt.Errorf("connecting to the service control manager: %w%s", err, adminHint(err))
	}
	return &manager{handle: h}, nil
}

func (m *manager) close() { _ = windows.CloseServiceHandle(m.handle) }

// adminHint explains the one failure an operator will meet most.
func adminHint(err error) string {
	if err == windows.ERROR_ACCESS_DENIED {
		return " (this needs administrator rights, and this process does not have them)"
	}
	return ""
}

// openService opens one service by name.
//
// The name is the service's key name, not its display name. They differ
// for most of what ships with Windows — the spooler is `Spooler` and
// shows as "Print Spooler" — so a failure says which of the two it was
// looking for.
func (m *manager) openService(name string, access uint32) (windows.Handle, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, err := windows.OpenService(m.handle, wide, access)
	if err != nil {
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			return 0, fmt.Errorf("there is no service called %q on this node "+
				"(this is the service name, not the display name the console shows)", name)
		}
		return 0, fmt.Errorf("opening the service %q: %w%s", name, err, adminHint(err))
	}
	return h, nil
}

// stateName renders a status code.
func stateName(code uint32) string {
	switch code {
	case windows.SERVICE_STOPPED:
		return "stopped"
	case windows.SERVICE_START_PENDING:
		return "starting"
	case windows.SERVICE_STOP_PENDING:
		return "stopping"
	case windows.SERVICE_RUNNING:
		return "running"
	case windows.SERVICE_CONTINUE_PENDING:
		return "resuming"
	case windows.SERVICE_PAUSE_PENDING:
		return "pausing"
	case windows.SERVICE_PAUSED:
		return "paused"
	}
	return fmt.Sprintf("unknown(%d)", code)
}

// startTypeName renders a start type, distinguishing delayed automatic
// start from ordinary automatic start.
//
// They are one code and a separate flag in the manager, and an operator
// reading a state that says `enable: true` needs to know which one the
// host has: a service set to delayed start is enabled, and reporting it
// as `manual` would make a state change it every run.
func startTypeName(code uint32, delayed bool) string {
	switch code {
	case windows.SERVICE_BOOT_START:
		return "boot"
	case windows.SERVICE_SYSTEM_START:
		return "system"
	case windows.SERVICE_AUTO_START:
		if delayed {
			return "auto_delayed"
		}
		return "auto"
	case windows.SERVICE_DEMAND_START:
		return "manual"
	case windows.SERVICE_DISABLED:
		return "disabled"
	}
	return fmt.Sprintf("unknown(%d)", code)
}

// StartTypeCode resolves a start type's name to what the manager stores,
// and whether the delayed flag goes with it.
func StartTypeCode(name string) (code uint32, delayed bool, err error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "auto", "automatic":
		return windows.SERVICE_AUTO_START, false, nil
	case "auto_delayed", "delayed", "automatic_delayed":
		return windows.SERVICE_AUTO_START, true, nil
	case "manual", "demand":
		return windows.SERVICE_DEMAND_START, false, nil
	case "disabled":
		return windows.SERVICE_DISABLED, false, nil
	}
	return 0, false, fmt.Errorf("%q is not a start type; this build understands "+
		"auto, auto_delayed, manual and disabled "+
		"(boot and system are for drivers and are not settable here)", name)
}

// Query reports what the manager knows about one service.
func Query(name string) (Status, error) {
	m, err := openManager(false)
	if err != nil {
		return Status{}, err
	}
	defer m.close()

	h, err := m.openService(name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return Status{}, err
	}
	defer windows.CloseServiceHandle(h)
	return statusOf(name, h)
}

func statusOf(name string, h windows.Handle) (Status, error) {
	out := Status{Name: name}

	var raw windows.SERVICE_STATUS_PROCESS
	var needed uint32
	err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO,
		(*byte)(unsafe.Pointer(&raw)), uint32(unsafe.Sizeof(raw)), &needed)
	if err != nil {
		return out, fmt.Errorf("reading the status of %q: %w", name, err)
	}
	out.State = stateName(raw.CurrentState)
	out.PID = raw.ProcessId

	config, err := queryConfig(h)
	if err != nil {
		// The status is the answer to the common question and is
		// already in hand; a configuration this process may not read is
		// not a reason to fail the whole query.
		return out, nil
	}
	out.DisplayName = windows.UTF16PtrToString(config.DisplayName)
	out.BinaryPath = windows.UTF16PtrToString(config.BinaryPathName)
	out.Account = windows.UTF16PtrToString(config.ServiceStartName)
	out.StartType = startTypeName(config.StartType, delayedStart(h))
	out.Description = description(h)
	return out, nil
}

// queryConfig reads a service's configuration into a buffer of the size
// the manager asks for.
//
// QUERY_SERVICE_CONFIG is a header followed by the strings it points at,
// so the call has to be made twice: once to be told how much room the
// whole thing needs, and once to read it.
func queryConfig(h windows.Handle) (*windows.QUERY_SERVICE_CONFIG, error) {
	var needed uint32
	err := windows.QueryServiceConfig(h, nil, 0, &needed)
	if err != nil && err != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, err
	}
	if needed == 0 {
		return nil, fmt.Errorf("the manager reported no configuration")
	}
	buf := make([]byte, needed)
	config := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(h, config, needed, &needed); err != nil {
		return nil, err
	}
	return config, nil
}

// config2 reads one of the extended configuration blocks, which are the
// same two-call shape.
func config2(h windows.Handle, level uint32) ([]byte, error) {
	var needed uint32
	err := windows.QueryServiceConfig2(h, level, nil, 0, &needed)
	if err != nil && err != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, err
	}
	if needed == 0 {
		return nil, nil
	}
	buf := make([]byte, needed)
	if err := windows.QueryServiceConfig2(h, level, &buf[0], needed, &needed); err != nil {
		return nil, err
	}
	return buf, nil
}

// delayedStart reports whether an automatic service is set to start
// late. Best effort: a service whose flag cannot be read is reported as
// an ordinary automatic start, which is what it is in every respect the
// manager will act on.
func delayedStart(h windows.Handle) bool {
	buf, err := config2(h, windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO)
	if err != nil || len(buf) < int(unsafe.Sizeof(windows.SERVICE_DELAYED_AUTO_START_INFO{})) {
		return false
	}
	info := (*windows.SERVICE_DELAYED_AUTO_START_INFO)(unsafe.Pointer(&buf[0]))
	return info.IsDelayedAutoStartUp != 0
}

func description(h windows.Handle) string {
	buf, err := config2(h, windows.SERVICE_CONFIG_DESCRIPTION)
	if err != nil || len(buf) < int(unsafe.Sizeof(windows.SERVICE_DESCRIPTION{})) {
		return ""
	}
	info := (*windows.SERVICE_DESCRIPTION)(unsafe.Pointer(&buf[0]))
	return windows.UTF16PtrToString(info.Description)
}

// Running reports whether a service is running.
func Running(name string) (bool, error) {
	s, err := Query(name)
	if err != nil {
		return false, err
	}
	return s.State == "running", nil
}

// Enabled reports whether a service starts on its own at boot.
//
// Automatic, delayed or not. `manual` is not enabled: a service that
// starts only when something asks for it is not one the machine brings
// up, which is what `service.enabled` means everywhere else.
func Enabled(name string) (bool, error) {
	s, err := Query(name)
	if err != nil {
		return false, err
	}
	return s.StartType == "auto" || s.StartType == "auto_delayed", nil
}

// Exists reports whether the manager knows a service at all.
func Exists(name string) bool {
	m, err := openManager(false)
	if err != nil {
		return false
	}
	defer m.close()
	h, err := m.openService(name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(h)
	return true
}

// Start starts a service and waits for it to be running.
//
// Waiting is the point. StartService returns as soon as the manager has
// launched the process, and a state that reported success there would
// report a database as running while it was still opening its files —
// and the state after it, which requires it, would fail.
func Start(name string) error {
	m, err := openManager(true)
	if err != nil {
		return err
	}
	defer m.close()

	h, err := m.openService(name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)

	if err := windows.StartService(h, 0, nil); err != nil {
		if err == windows.ERROR_SERVICE_ALREADY_RUNNING {
			return nil
		}
		return fmt.Errorf("starting %q: %w%s", name, err, adminHint(err))
	}
	return settle(name, h, windows.SERVICE_RUNNING)
}

// Stop stops a service and waits for it to have stopped.
func Stop(name string) error {
	m, err := openManager(true)
	if err != nil {
		return err
	}
	defer m.close()

	h, err := m.openService(name, windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)

	var status windows.SERVICE_STATUS
	if err := windows.ControlService(h, windows.SERVICE_CONTROL_STOP, &status); err != nil {
		if err == windows.ERROR_SERVICE_NOT_ACTIVE {
			return nil
		}
		return fmt.Errorf("stopping %q: %w%s", name, err, adminHint(err))
	}
	return settle(name, h, windows.SERVICE_STOPPED)
}

// Restart stops a service and starts it again.
//
// Two operations rather than one, because the manager has no restart:
// the services console does the same thing. A service that was not
// running is started, which is what `service.running` with a watch
// expects of a restart.
func Restart(name string) error {
	if err := Stop(name); err != nil {
		return err
	}
	return Start(name)
}

// settle waits for a service to reach a state.
func settle(name string, h windows.Handle, want uint32) error {
	deadline := time.Now().Add(settleTimeout)
	for {
		var raw windows.SERVICE_STATUS_PROCESS
		var needed uint32
		err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO,
			(*byte)(unsafe.Pointer(&raw)), uint32(unsafe.Sizeof(raw)), &needed)
		if err != nil {
			return fmt.Errorf("waiting for %q: %w", name, err)
		}
		if raw.CurrentState == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%q is %s after %s and has not reached %s",
				name, stateName(raw.CurrentState), settleTimeout, stateName(want))
		}
		// A service that has stopped while we waited for it to start is
		// not going to arrive, and its exit code is the reason.
		if want == windows.SERVICE_RUNNING && raw.CurrentState == windows.SERVICE_STOPPED {
			return fmt.Errorf("%q stopped instead of starting%s", name, exitReason(raw))
		}
		time.Sleep(pollInterval)
	}
}

// exitReason turns a stopped service's exit code into something an
// operator can act on.
func exitReason(raw windows.SERVICE_STATUS_PROCESS) string {
	if raw.ServiceSpecificExitCode != 0 {
		return fmt.Sprintf(" (its own exit code %d; the service's log says what that means)",
			raw.ServiceSpecificExitCode)
	}
	if raw.Win32ExitCode != 0 && raw.Win32ExitCode != 1077 {
		return fmt.Sprintf(" (exit code %d: %s)",
			raw.Win32ExitCode, windows.Errno(raw.Win32ExitCode).Error())
	}
	return ""
}

// SetStartType changes when a service starts.
func SetStartType(name, startType string) error {
	code, delayed, err := StartTypeCode(startType)
	if err != nil {
		return err
	}
	m, err := openManager(true)
	if err != nil {
		return err
	}
	defer m.close()

	h, err := m.openService(name, windows.SERVICE_CHANGE_CONFIG|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)

	err = windows.ChangeServiceConfig(h,
		windows.SERVICE_NO_CHANGE, code, windows.SERVICE_NO_CHANGE,
		nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("setting the start type of %q to %s: %w%s",
			name, startType, err, adminHint(err))
	}
	if code != windows.SERVICE_AUTO_START {
		return nil
	}
	// The delayed flag is a separate block, and it has to be written
	// even when clearing it: a service that was delayed and is now set
	// to plain automatic keeps the flag otherwise, and `auto` and
	// `auto_delayed` would then never converge on each other.
	info := windows.SERVICE_DELAYED_AUTO_START_INFO{}
	if delayed {
		info.IsDelayedAutoStartUp = 1
	}
	err = windows.ChangeServiceConfig2(h,
		windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO, (*byte)(unsafe.Pointer(&info)))
	if err != nil {
		return fmt.Errorf("setting the delayed start of %q: %w", name, err)
	}
	return nil
}

// List names every service the manager knows, sorted.
func List() ([]string, error) {
	statuses, err := ListStatus()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out, nil
}

// ListStatus enumerates every service with its state, which is one call
// rather than one call per service.
func ListStatus() ([]Status, error) {
	m, err := openManager(false)
	if err != nil {
		return nil, err
	}
	defer m.close()

	var needed, count, resume uint32
	err = windows.EnumServicesStatusEx(m.handle, windows.SC_ENUM_PROCESS_INFO,
		windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL,
		nil, 0, &needed, &count, &resume, nil)
	if err != nil && err != windows.ERROR_MORE_DATA {
		return nil, fmt.Errorf("listing the services: %w", err)
	}
	if needed == 0 {
		return nil, nil
	}
	buf := make([]byte, needed)
	resume = 0
	err = windows.EnumServicesStatusEx(m.handle, windows.SC_ENUM_PROCESS_INFO,
		windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL,
		&buf[0], needed, &needed, &count, &resume, nil)
	if err != nil {
		return nil, fmt.Errorf("listing the services: %w", err)
	}

	var out []Status
	entries := unsafe.Slice(
		(*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), int(count))
	for _, e := range entries {
		out = append(out, Status{
			Name:        windows.UTF16PtrToString(e.ServiceName),
			DisplayName: windows.UTF16PtrToString(e.DisplayName),
			State:       stateName(e.ServiceStatusProcess.CurrentState),
			PID:         e.ServiceStatusProcess.ProcessId,
		})
	}
	return out, nil
}
