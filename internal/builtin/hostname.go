package builtin

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// EtcHostnamePath is where Linux and most others keep the persistent
// name, as a variable so a test can point it somewhere harmless.
var EtcHostnamePath = "/etc/hostname"

// registerHostname installs the hostname module of SPEC sections 15.2
// and 15.5.
//
// A hostname has the same two states a sysctl has — the running one and
// the one that survives a reboot — and the same reason to manage both at
// once. A node whose running name and configured name disagree is a node
// that renames itself at the least convenient moment, and it is common:
// `hostname foo` alone lasts until the next boot, and editing
// /etc/hostname alone does nothing until then.
//
// Reading works everywhere. Setting is unix only and declared so, for
// the reason `mount` is: Windows renames a machine through the Win32
// SetComputerNameEx call and the new name does not take effect until a
// reboot, so the running name and the pending name differ by design
// until then. A state that set it would report a change on every run
// until somebody rebooted, which is the failure `win_registry` is
// already an open question about (SPEC 33, and this plan's section 6).
// Rather than ship that shape twice, this refuses on Windows by name and
// the question gets answered once.
func registerHostname(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "hostname", Function: "get_hostname",
				Doc:      "Return this node's running hostname, without its domain.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return runningHostname()
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hostname", Function: "get_fqdn",
				Doc:      "Return this node's fully qualified domain name, or its hostname if it resolves to nothing longer.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return fqdn()
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hostname", Function: "get_persistent",
				Doc:       "Return the hostname this node will have after a reboot, which is not always the one it has now.",
				TestMode:  signature.TestNotApplicable,
				Platforms: unixOnly,
				Section:   "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				name, _, err := persistentHostname(c)
				if err != nil {
					return nil, err
				}
				return name, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "hostname", Function: "set_hostname",
				Doc: "Set this node's hostname, both running and persistent.",
				Params: []signature.Param{
					req("hostname", signature.String, "The name to set."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  unixOnly,
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				want := strings.TrimSpace(states.Str(args, "hostname", ""))
				if err := validHostname(want); err != nil {
					return nil, err
				}
				if c.Test {
					return want, nil
				}
				if err := setHostname(c, want); err != nil {
					return nil, err
				}
				return want, nil
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "hostname", Function: "system",
			Doc: "Ensure this node's hostname is the one named, now and after a reboot.",
			Params: []signature.Param{
				nameParam("The hostname. Defaults to the state ID."),
			},
			Mutates:    true,
			TestMode:   signature.TestReliable,
			Privileges: []string{"root"},
			Platforms:  unixOnly,
			Section:    "15.5",
		},
		Fn: hostnameSystem,
	})
}

// hostnameSystem converges both halves of the name.
//
// Both are reported separately in the change set, because "the running
// name was already right and the file was not" is the case an operator
// most needs to see: it is the node that was renamed by hand and would
// have gone back on the next boot.
func hostnameSystem(c *exec.Context, args *value.Map) (states.Result, error) {
	want := strings.TrimSpace(states.Str(args, "name", ""))
	if err := validHostname(want); err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}

	running, err := runningHostname()
	if err != nil {
		return states.False(fmt.Sprintf("This node's hostname could not be read: %v", err)), nil
	}
	persistent, where, err := persistentHostname(c)
	if err != nil {
		return states.False(fmt.Sprintf("This node's persistent hostname could not be read: %v", err)), nil
	}

	// The running name is compared on its short form. `hostname` returns
	// whatever was set, which on a node configured with a fully
	// qualified name is the whole thing, and a state naming the short
	// form should not fight one that named the long one.
	runningMatches := sameHostname(running, want)
	persistentMatches := sameHostname(persistent, want)
	if runningMatches && persistentMatches {
		return states.True(fmt.Sprintf("The hostname is already %s, in %s and running.", want, where)), nil
	}

	changes := value.NewMap(2)
	if !runningMatches {
		changes.Set("running", states.Change(running, want))
	}
	if !persistentMatches {
		changes.Set(where, states.Change(persistent, want))
	}

	what := describeHostnameChange(runningMatches, persistentMatches, where)
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The hostname would be set to %s (%s).", want, what), changes), nil
	}
	if err := setHostname(c, want); err != nil {
		return states.False(fmt.Sprintf("The hostname could not be set to %s: %v", want, err)), nil
	}
	return states.Changed(fmt.Sprintf("The hostname was set to %s (%s).", want, what), changes), nil
}

func describeHostnameChange(runningOK, persistentOK bool, where string) string {
	switch {
	case runningOK:
		return "the running name was already right; " + where + " was not"
	case persistentOK:
		return where + " was already right; the running name was not"
	}
	return "running and " + where
}

// runningHostname is the name the kernel answers with.
func runningHostname() (string, error) {
	name, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return name, nil
}

// fqdn resolves the running name to a fully qualified one.
//
// Best effort by design: a node with no reverse record, or none that
// resolves, has no fully qualified name to report and its short name is
// the honest answer rather than a fabricated one.
func fqdn() (string, error) {
	host, err := runningHostname()
	if err != nil {
		return "", err
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return host, nil
	}
	for _, addr := range addrs {
		names, err := net.LookupAddr(addr)
		if err != nil {
			continue
		}
		for _, n := range names {
			n = strings.TrimSuffix(n, ".")
			if strings.Contains(n, ".") {
				return n, nil
			}
		}
	}
	return host, nil
}

// persistentHostname reads the name that survives a reboot, and says
// which file it came from.
//
// FreeBSD keeps it in rc.conf and has `sysrc` to read it, which this
// build already shells out to for `sysrc.get`. Everything else keeps it
// in /etc/hostname.
func persistentHostname(c *exec.Context) (name, where string, err error) {
	if runtime.GOOS == "freebsd" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"sysrc", "-n", "hostname"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", "rc.conf", err
		}
		if res.Code != 0 {
			// Unset is not an error: a node that has never had one
			// configured has an empty persistent name, and that is the
			// difference the state exists to close.
			return "", "rc.conf", nil
		}
		return strings.TrimSpace(res.Stdout), "rc.conf", nil
	}

	body, err := os.ReadFile(EtcHostnamePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", EtcHostnamePath, nil
		}
		return "", EtcHostnamePath, err
	}
	// The file is one line. A comment is not part of the name, and a
	// trailing newline is not either.
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line, EtcHostnamePath, nil
	}
	return "", EtcHostnamePath, nil
}

// setHostname sets both halves.
//
// The running name goes first on the systemd path because hostnamectl
// does both together and there is nothing left to do; elsewhere the file
// is written and then the running name set, so that a failure between
// the two leaves the node with the name it will boot with rather than
// one it will lose.
func setHostname(c *exec.Context, want string) error {
	if runtime.GOOS == "freebsd" {
		if _, err := c.Run(exec.Command{Argv: []string{"sysrc", "hostname=" + want}}); err != nil {
			return err
		}
		_, err := c.Run(exec.Command{Argv: []string{"hostname", want}})
		return err
	}

	// systemd's tool writes /etc/hostname, sets the running name, and
	// tells the rest of the system, and it is the only supported way to
	// do it where it is running: a file written behind its back is
	// reverted by the next `hostnamectl` call.
	if c.Which("hostnamectl") != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"hostnamectl", "set-hostname", want},
			IgnoreExitCode: true,
		})
		if err != nil {
			return err
		}
		if res.Code == 0 {
			return nil
		}
		// hostnamectl fails inside a container with no systemd running,
		// where writing the file and calling hostname(1) is both
		// possible and correct. Falling through rather than failing is
		// what makes this work in the places an estate actually builds
		// images.
	}

	if err := writeAtomic(EtcHostnamePath, []byte(want+"\n"), 0o644); err != nil {
		return fmt.Errorf("%s could not be written: %w", EtcHostnamePath, err)
	}
	if _, err := c.Run(exec.Command{Argv: []string{"hostname", want}}); err != nil {
		return err
	}
	return nil
}

// sameHostname compares a configured name against a running one.
//
// Case-insensitively, because DNS is, and on the short form when one
// side carries a domain and the other does not: a node configured as
// `web1.example` running as `web1` is the same node, and a state that
// rewrote it every run over that would be worse than one that did
// nothing.
func sameHostname(have, want string) bool {
	if have == "" || want == "" {
		return have == want
	}
	if strings.EqualFold(have, want) {
		return true
	}
	return strings.EqualFold(shortHostname(have), shortHostname(want)) &&
		(!strings.Contains(have, ".") || !strings.Contains(want, "."))
}

func shortHostname(name string) string {
	if i := strings.Index(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

// validHostname refuses what the resolver and the kernel will refuse,
// before anything is written.
//
// A hostname that setting fails on halfway leaves a node with one name
// in a file and another in the kernel, which is the state this module
// exists to prevent rather than to create.
func validHostname(name string) error {
	if name == "" {
		return fmt.Errorf("this needs a hostname")
	}
	if len(name) > 253 {
		return fmt.Errorf("%q is %d characters; a hostname is at most 253", name, len(name))
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			return fmt.Errorf("%q has an empty label", name)
		}
		if len(label) > 63 {
			return fmt.Errorf("%q has a label of %d characters; the limit is 63", name, len(label))
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("%q has a label that starts or ends with a hyphen", name)
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
				r >= '0' && r <= '9', r == '-':
			default:
				return fmt.Errorf("%q contains %q, which a hostname may not", name, r)
			}
		}
	}
	return nil
}
