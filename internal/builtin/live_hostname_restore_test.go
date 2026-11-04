package builtin

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// hostnameSnapshot is what a live test that renames this machine has to
// put back: the running name, and the boot-time record as it was --
// **including its absence**.
//
// The absence is the half that was lost. Both renaming tests restored
// through `hostname.set_hostname`, which sets both halves, so a machine
// with no boot-time name came back with one: the running name, written
// into it. On a Mac that is not a corner case but the default -- an
// unconfigured Mac has no `HostName` preference at all, and takes its
// running name from `LocalHostName` -- and the run that found it, on
// mini-me (macOS 27.3) on 2026-11-04, left `scutil --get HostName`
// answering `mini-me-1986.local` where it had answered "not set". Every
// other check passed, and the `macos` leg's own "put back" step looked
// only at the running name, which was right.
//
// So a snapshot records the boot-time name as the module reads it, and
// whether the platform's own record existed at all, and restore() puts
// back exactly that and then checks it from outside the module.
// DIVERGENCE 5.575.
type hostnameSnapshot struct {
	running    string
	persistent string // "" when the platform has none configured

	// Linux and the others that keep /etc/hostname: the file itself,
	// because "no file" and "a file with no name in it" both read as
	// unset and only one of them is what was there.
	hadEtcHostname bool
	etcHostname    []byte
}

func captureHostname(c *exec.Context, r *Registries) (hostnameSnapshot, error) {
	var s hostnameSnapshot
	running, err := r.Exec.Call(c, "hostname.get_hostname", value.NewMap(0))
	if err != nil {
		return s, fmt.Errorf("this machine's running hostname could not be read: %w", err)
	}
	s.running, _ = running.(string)
	s.running = strings.TrimSpace(s.running)
	if s.running == "" {
		return s, fmt.Errorf("this machine reports no running hostname, so there is nothing to put back")
	}
	persistent, err := r.Exec.Call(c, "hostname.get_persistent", value.NewMap(0))
	if err != nil {
		return s, fmt.Errorf("this machine's persistent hostname could not be read: %w", err)
	}
	s.persistent, _ = persistent.(string)
	s.persistent = strings.TrimSpace(s.persistent)
	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		body, err := os.ReadFile(EtcHostnamePath)
		switch {
		case err == nil:
			s.hadEtcHostname, s.etcHostname = true, body
		case !os.IsNotExist(err):
			return s, err
		}
	}
	return s, nil
}

// restore puts the snapshot back and says what it could not.
//
// The name goes back through the module, as it always did: restoring it
// by hand would be a second implementation of the thing under test, and
// one that agreed with a broken module would hide it. What the module
// cannot do is *unset* the boot-time name -- it has no such operation,
// and nor has Salt's -- so when there was none, the record the module
// has just written is removed here, by the platform's own tool. That is
// the one step taken by hand, and it is checked afterwards like the
// rest.
func (s hostnameSnapshot) restore(c *exec.Context, r *Registries) error {
	name := s.running
	if s.persistent != "" {
		name = s.persistent
	}
	if _, err := r.Exec.Call(c, "hostname.set_hostname", value.MapOf("hostname", name)); err != nil {
		return fmt.Errorf("restoring %q failed: %w", name, err)
	}
	if s.persistent == "" {
		if err := s.clearPersistent(c); err != nil {
			return err
		}
	}

	// Then from outside the module. The boot-time record first, as the
	// platform keeps it.
	got, where, err := persistentByHandErr(c)
	if err != nil {
		return err
	}
	if got != s.persistent {
		return fmt.Errorf("%s holds %q and held %q before this test; this machine "+
			"would come back as %q after a reboot", where, got, s.persistent, got)
	}
	// And the running name, which on a Mac is not settled when the
	// commands above return; see waitRunningName.
	if now, ok := waitRunningName(s.running); !ok {
		return fmt.Errorf("this machine was left running as %q; it was %q", now, s.running)
	}
	return nil
}

func (s hostnameSnapshot) clearPersistent(c *exec.Context) error {
	switch runtime.GOOS {
	case "darwin":
		// `scutil --set` has no way to remove a preference; its
		// interactive `--prefs` mode does. Run on mini-me (macOS 27.3,
		// 2026-11-04): afterwards `scutil --get HostName` exits 1 with
		// "HostName: not set" on stderr and nothing on stdout, which is
		// what darwinPersistentHostname reads as unset, and configd puts
		// the running name back to `LocalHostName` plus `.local`.
		res, err := c.Run(exec.Command{
			Argv:           []string{"scutil", "--prefs"},
			Stdin:          "open\nget /System/System\nd.remove HostName\nset /System/System\ncommit\napply\nquit\n",
			IgnoreExitCode: true,
		})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("removing the HostName preference: scutil --prefs exited %d: %s",
				res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
		}
		return nil
	case "freebsd":
		res, err := c.Run(exec.Command{Argv: []string{"sysrc", "-x", "hostname"}, IgnoreExitCode: true})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("sysrc -x hostname exited %d: %s", res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
		}
		return nil
	}
	if !s.hadEtcHostname {
		if err := os.Remove(EtcHostnamePath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if body, err := os.ReadFile(EtcHostnamePath); err == nil && bytes.Equal(body, s.etcHostname) {
		return nil
	}
	return os.WriteFile(EtcHostnamePath, s.etcHostname, 0o644)
}

// persistentByHandErr is persistentByHand for a caller with no *testing.T
// to fail, such as a conformance case's Cleanup.
func persistentByHandErr(c *exec.Context) (name, where string, err error) {
	switch runtime.GOOS {
	case "freebsd":
		res, err := c.Run(exec.Command{Argv: []string{"sysrc", "-n", "hostname"}, IgnoreExitCode: true})
		if err != nil {
			return "", "rc.conf", fmt.Errorf("sysrc: %w", err)
		}
		if res.Code != 0 {
			return "", "rc.conf", nil
		}
		return strings.TrimSpace(res.Stdout), "rc.conf", nil
	case "darwin":
		res, err := c.Run(exec.Command{Argv: []string{"scutil", "--get", "HostName"}, IgnoreExitCode: true})
		if err != nil {
			return "", "scutil HostName", fmt.Errorf("scutil: %w", err)
		}
		if res.Code != 0 {
			return "", "scutil HostName", nil
		}
		return strings.TrimSpace(res.Stdout), "scutil HostName", nil
	}
	body, err := os.ReadFile("/etc/hostname")
	if os.IsNotExist(err) {
		return "", "/etc/hostname", nil
	}
	if err != nil {
		return "", "/etc/hostname", err
	}
	return strings.TrimSpace(string(body)), "/etc/hostname", nil
}

// waitRunningName waits for the kernel's running name to become want,
// and returns what it last read.
//
// **On a Mac the running name is not settled when the command that
// changed it returns.** Writing the `HostName` preference makes configd
// set the running name to it, asynchronously: on mini-me (macOS 27.3,
// 2026-11-04) `scutil --set HostName A` followed at once by `hostname B`
// read B for 0.003 s and then A from 0.064 s on, for good. So a test
// that renames the machine by hand within that window, after anything
// wrote the preference, has its rename undone under it -- which is how
// TestLiveHostnameTellsTheRunningNameFromTheBootTimeOne failed there,
// reading the previous test's `halite-live-probe` 0.03 s into a run that
// came straight after that test's cleanup. macos-15 runners passed it,
// so the window is a fact about the machine and not something a fixed
// sleep can be sized from; this polls instead.
//
// Elsewhere nothing changes the running name behind the caller's back,
// and the first read answers. DIVERGENCE 5.576.
func waitRunningName(want string) (string, bool) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		now, err := runningHostname()
		if err == nil && now == want {
			return now, true
		}
		if time.Now().After(deadline) {
			return now, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
