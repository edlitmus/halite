package builtin

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/dbus"
	"github.com/edlitmus/halite/internal/exec"
)

// The systemd provider of `service` speaks to systemd over its D-Bus API,
// as SPEC 15.2 requires, and falls back to `systemctl` when the bus
// cannot be reached. This file is the D-Bus half: a thin binding over
// `org.freedesktop.systemd1.Manager` and the wire client in
// internal/dbus.
//
// The fallback boundary is deliberate. "The bus cannot be reached" -- no
// socket, authentication refused, `Hello` failed -- means the D-Bus path
// never started, so `systemctl` runs instead. An error that came *back*
// from systemd -- an unknown unit, a polkit refusal, a job that failed
// -- is a real answer and is returned, never retried on the shell where
// it would fail the same way with a worse message.
//
// "The same way" holds only while both paths are handed the same unit
// name. They once were not: systemctl mangles `php8.2-fpm` into
// `php8.2-fpm.service` itself, the Manager does not, and a name the bus
// refused was a name the shell would have accepted. withServiceSuffix
// is applied on both sides for that reason.

const (
	sdDest      = "org.freedesktop.systemd1"
	sdObject    = "/org/freedesktop/systemd1"
	sdManager   = "org.freedesktop.systemd1.Manager"
	sdUnitIface = "org.freedesktop.systemd1.Unit"
	dbusProps   = "org.freedesktop.DBus.Properties"

	// jobRemovedMatch routes the per-job completion signal to us. systemd
	// only emits these after a Subscribe, and the bus only delivers a
	// broadcast signal to a connection that asked for it.
	jobRemovedMatch = "type='signal',sender='org.freedesktop.systemd1'," +
		"interface='org.freedesktop.systemd1.Manager',member='JobRemoved'"
)

// systemdBackend is the slice of systemd's Manager interface the service
// provider uses. It is an interface so a test can hand the provider one
// that records calls instead of opening a bus.
type systemdBackend interface {
	// runJob issues one of StartUnit/StopUnit/RestartUnit/ReloadUnit and
	// blocks until systemd's JobRemoved says the job is done, the way
	// `systemctl` does.
	runJob(verb, unit string) error
	// enable/disable/mask/unmask change the unit files and then Reload,
	// which is what `systemctl enable` does that EnableUnitFiles alone
	// does not.
	enable(unit string) error
	disable(unit string) error
	mask(unit string) error
	unmask(unit string) error
	activeState(unit string) (string, error)
	unitFileState(unit string) (string, error)
	listUnitFiles() ([]string, error)
	Close() error
}

// systemdBus opens a connection to systemd over the system bus. Tests
// replace it; production dials the real bus.
var systemdBus = dialSystemd

// dialOrNil returns a live backend, or false when the bus is
// unreachable and the caller should fall back to `systemctl`.
func dialOrNil(c *exec.Context) (systemdBackend, bool) {
	b, err := systemdBus(c.Ctx)
	if err != nil {
		return nil, false
	}
	return b, true
}

// systemdUnitTypes is every unit type systemd has: the `UnitType` enum
// in systemd's src/basic/unit-def.c and the list in systemd.unit(5).
// Salt's VALID_UNIT_TYPES (modules/systemd_service.py) has the first
// nine and lacks `slice` and `scope`; those are real types, and a state
// naming `user.slice` must not have `.service` bolted onto it.
var systemdUnitTypes = []string{
	"service", "socket", "device", "mount", "automount", "swap",
	"target", "path", "timer", "slice", "scope",
}

// withServiceSuffix canonicalises a unit name the way Salt's
// _canonical_unit_name and systemctl's own name mangling both do: a
// name that ends in a real unit-type suffix -- `foo.socket`,
// `foo.timer` -- is left alone, and anything else gets `.service`.
//
// The test is the suffix, not the presence of a dot. It used to be the
// dot, which sent `php8.2-fpm` to systemd as it stood; systemd's
// Manager does no mangling and refused it ("Unit name php8.2-fpm is not
// valid"), and because a returned D-Bus error is not retried on the
// shell, every start and enable of such a unit failed while its reads,
// which do fall through to systemctl, looked fine (DIVERGENCE 5.39 and
// the section that corrects it).
//
// Every call site -- D-Bus and systemctl alike -- goes through this, so
// the two paths name the same unit by construction rather than by
// systemctl happening to mangle the way this function does.
func withServiceSuffix(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		suffix := name[i+1:]
		for _, t := range systemdUnitTypes {
			if suffix == t {
				return name
			}
		}
	}
	return name + ".service"
}

func dialSystemd(ctx context.Context) (systemdBackend, error) {
	conn, err := dbus.DialSystemBus()
	if err != nil {
		return nil, err
	}
	if _, err := conn.Hello(); err != nil {
		conn.Close()
		return nil, err
	}
	// Subscribe makes systemd emit per-job signals; the match rule makes
	// the bus route them here. Both are needed before the first job.
	if _, err := conn.Call(sdDest, sdObject, sdManager, "Subscribe", ""); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.AddMatch(jobRemovedMatch); err != nil {
		conn.Close()
		return nil, err
	}
	d := &sdConn{conn: conn, deadline: time.Now().Add(90 * time.Second)}
	if dl, ok := ctx.Deadline(); ok {
		d.deadline = dl
		d.jobDeadline = dl
	}
	return d, nil
}

// sdConn is one connection to systemd's Manager.
//
// Two deadlines, because two kinds of wait. A method call -- enable, mask,
// a property read -- is answered at once, and 90 s without an answer is a
// bus that is not answering. A job is different: it lasts as long as the
// unit takes to start or stop, which systemd bounds itself, per unit, by
// TimeoutStartSec and TimeoutStopSec. Those default to 90 s and are raised
// for exactly the units that need it, a database or a migration. The job
// wait shared the 90 s, so such a unit was reported failed at 90 s, with
// "read unix @->/run/dbus/system_bus_socket: i/o timeout", while systemd
// went on starting it (issue 321, M-48; measured on Ubuntu 24.04.5 with a
// unit allowed 180 s). `systemctl start`, the fallback and what Salt runs,
// waits for the job, and so does this now: only a deadline the caller set
// -- a state's `timeout`, a job's -- ends the wait early.
type sdConn struct {
	conn        *dbus.Conn
	deadline    time.Time // for method calls
	jobDeadline time.Time // for a job; zero waits as long as systemd does
}

func (d *sdConn) Close() error { return d.conn.Close() }

func (d *sdConn) call(iface, member, sig string, args ...any) ([]any, error) {
	_ = d.conn.SetReadDeadline(d.deadline)
	return d.conn.Call(sdDest, sdObject, iface, member, sig, args...)
}

// runJob sends the verb and waits for the JobRemoved that reports its
// outcome. The signal can arrive before the method return, so job
// results are buffered by job path until the return names ours.
func (d *sdConn) runJob(verb, unit string) error {
	_ = d.conn.SetReadDeadline(d.jobDeadline)
	serial, err := d.conn.SendCall(sdDest, sdObject, sdManager, verb, "ss", unit, "replace")
	if err != nil {
		return err
	}

	var jobPath string
	haveJob := false
	results := map[string]string{}

	for {
		m, err := d.conn.ReadMessage()
		if err != nil {
			return err
		}
		switch {
		case m.ReplySerial == serial && m.Type == dbus.TypeError:
			return &dbus.Error{Name: m.ErrorName, Body: firstBodyString(m.Body)}
		case m.ReplySerial == serial && m.Type == dbus.TypeMethodReturn:
			if len(m.Body) == 0 {
				return fmt.Errorf("%s returned no job", verb)
			}
			jobPath, _ = m.Body[0].(string)
			haveJob = true
		case m.Type == dbus.TypeSignal && m.Member == "JobRemoved" && len(m.Body) >= 4:
			jp, _ := m.Body[1].(string)
			res, _ := m.Body[3].(string)
			results[jp] = res
		}
		if haveJob {
			if res, ok := results[jobPath]; ok {
				if res != "" && res != "done" {
					return fmt.Errorf("job for %s finished %q", unit, res)
				}
				return nil
			}
		}
	}
}

func (d *sdConn) enable(unit string) error {
	if _, err := d.call(sdManager, "EnableUnitFiles", "asbb", []string{unit}, false, false); err != nil {
		return err
	}
	return d.reload()
}

func (d *sdConn) disable(unit string) error {
	if _, err := d.call(sdManager, "DisableUnitFiles", "asb", []string{unit}, false); err != nil {
		return err
	}
	return d.reload()
}

func (d *sdConn) mask(unit string) error {
	if _, err := d.call(sdManager, "MaskUnitFiles", "asbb", []string{unit}, false, false); err != nil {
		return err
	}
	return d.reload()
}

func (d *sdConn) unmask(unit string) error {
	if _, err := d.call(sdManager, "UnmaskUnitFiles", "asb", []string{unit}, false); err != nil {
		return err
	}
	return d.reload()
}

func (d *sdConn) reload() error {
	_, err := d.call(sdManager, "Reload", "")
	return err
}

func (d *sdConn) activeState(unit string) (string, error) {
	p, err := d.unitPath(unit)
	if err != nil {
		return "", err
	}
	_ = d.conn.SetReadDeadline(d.deadline)
	out, err := d.conn.Call(sdDest, p, dbusProps, "Get", "ss", sdUnitIface, "ActiveState")
	if err != nil {
		return "", err
	}
	s, _ := out[0].(string)
	return s, nil
}

func (d *sdConn) unitFileState(unit string) (string, error) {
	out, err := d.call(sdManager, "GetUnitFileState", "s", unit)
	if err != nil {
		return "", err
	}
	s, _ := out[0].(string)
	return s, nil
}

// unitPath resolves a unit name to its object path. GetUnit fails for a
// unit that is not loaded; LoadUnit loads it, and also returns a path
// for a unit that does not exist (its ActiveState then reads
// "inactive"), which matches what `systemctl is-active` reports for one.
func (d *sdConn) unitPath(unit string) (string, error) {
	out, err := d.call(sdManager, "GetUnit", "s", unit)
	if err != nil {
		out, err = d.call(sdManager, "LoadUnit", "s", unit)
		if err != nil {
			return "", err
		}
	}
	p, _ := out[0].(string)
	return p, nil
}

func (d *sdConn) listUnitFiles() ([]string, error) {
	out, err := d.call(sdManager, "ListUnitFiles", "")
	if err != nil {
		return nil, err
	}
	rows, _ := out[0].([]any)
	var names []string
	for _, row := range rows {
		fields, _ := row.([]any)
		if len(fields) == 0 {
			continue
		}
		p, _ := fields[0].(string)
		base := path.Base(p)
		if strings.HasSuffix(base, ".service") {
			names = append(names, base)
		}
	}
	sort.Strings(names)
	return names, nil
}

func firstBodyString(body []any) string {
	if len(body) == 0 {
		return ""
	}
	s, _ := body[0].(string)
	return s
}
