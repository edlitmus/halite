package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readServiceFiles returns every file under contrib/rc.d and
// contrib/systemd, walked rather than listed so the FIPS drop-ins in
// their subdirectories are read too.
func readServiceFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"rc.d", "systemd"} {
		root := filepath.Join("..", "..", "contrib", dir)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(body)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no service files were read; this check has stopped checking")
	}
	return out
}

// rc.subr reserves `${name}_program`: it assigns it over `command`.
//
// A FIPS switch was added as `halite_hub_program`, which silently
// replaced `/usr/sbin/daemon` with the halite binary — so daemon(8) was
// never run and the binary was handed daemon's own flags as arguments.
// The service failed to start, and the only clue was in rc_debug.
func TestRcScriptsDoNotUseRcSubrsReservedProgramVariable(t *testing.T) {
	reserved := regexp.MustCompile(`\b([a-z_]+)_program\b`)
	for path, body := range readServiceFiles(t) {
		if !strings.Contains(path, "rc.d") {
			continue
		}
		for i, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if m := reserved.FindStringSubmatch(line); m != nil {
				t.Errorf("%s:%d uses %s_program, which rc.subr assigns over `command`: %s",
					path, i+1, m[1], strings.TrimSpace(line))
			}
		}
	}
}

// An rc.d script whose `command` is daemon(8) must set `procname`.
//
// daemon's `-p` records the *child's* pid, and rc.subr matches that pid
// against `procname`, which defaults to `command`. Without it rc looked
// for `daemon` at a pid belonging to halite-hub, found nothing, and
// reported a running service as stopped — so `stop` and `restart` did
// nothing at all.
func TestRcScriptsUsingDaemonNameTheProcessTheySupervise(t *testing.T) {
	checked := 0
	for path, body := range readServiceFiles(t) {
		if !strings.Contains(path, "rc.d") {
			continue
		}
		if !strings.Contains(body, `command="/usr/sbin/daemon"`) {
			continue
		}
		checked++
		if !strings.Contains(body, "\nprocname=") {
			t.Errorf("%s supervises with daemon(8) and sets no procname, "+
				"so rc.subr cannot find the process its pidfile names", path)
		}
	}
	if checked == 0 {
		t.Error("no rc.d script uses daemon(8); this check has stopped checking")
	}
}

// Nothing in this build handles SIGHUP, so Go's default disposition
// applies and the signal terminates the process. A unit advertising
// ExecReload therefore turned `systemctl reload` into an outage.
func TestNoUnitOffersAReloadThatWouldKillTheService(t *testing.T) {
	handled := false
	err := filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// Tests excluded: this file names the signal in its own reason,
		// which was enough to make the check skip itself.
		if strings.Contains(path, "vendor/") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The registration, not the word: a comment mentioning the
		// signal is not a handler for it.
		if strings.Contains(string(body), "signal.Notify") &&
			strings.Contains(string(body), "syscall.SIGHUP") {
			handled = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Skip("something handles SIGHUP now; a reload directive may be correct again")
	}
	for path, body := range readServiceFiles(t) {
		for i, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "ExecReload=") {
				t.Errorf("%s:%d offers a reload, and SIGHUP terminates this build: %s",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The Makefile installs where the binaries look.
//
// Two places that have to agree about the same platform layout: the code
// computes it, and `make install` writes to it. A Makefile installing
// somewhere the binary does not read produces a service that starts and
// finds nothing — the failure names neither, which is the shape this
// project keeps meeting.
func TestTheMakefileInstallsWhereTheBinariesLook(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}

	// Each is `NAME != case ... BSDs) echo <bsd> ;; *) echo <other> ;;`.
	pattern := regexp.MustCompile(
		`(?m)^(CONFDIR|STATEDIR)\s*!=.*DragonFly\)\s*echo\s+(\S+)\s*;;\s*\*\)\s*echo\s+(\S+)\s*;;`)
	matches := pattern.FindAllStringSubmatch(string(body), -1)
	if len(matches) != 2 {
		t.Fatalf("read %d platform paths from the Makefile, want 2; the recipe has changed shape",
			len(matches))
	}

	for _, m := range matches {
		name, bsd, other := m[1], m[2], m[3]
		var wantBSD, wantOther string
		switch name {
		case "CONFDIR":
			wantBSD, wantOther = RootFor("freebsd"), RootFor("linux")
		case "STATEDIR":
			wantBSD, wantOther = VarPathFor("freebsd", "lib"), VarPathFor("linux", "lib")
		}
		if bsd != wantBSD {
			t.Errorf("the Makefile installs %s to %s on a BSD; the binaries read %s",
				name, bsd, wantBSD)
		}
		if other != wantOther {
			t.Errorf("the Makefile installs %s to %s elsewhere; the binaries read %s",
				name, other, wantOther)
		}
	}

	// And the service files name the binary the Makefile installs, which
	// is one path on every platform for exactly that reason: moving it
	// would leave every unit pointing at nothing.
	if !strings.Contains(string(body), "BINDIR    ?= /usr/local/bin") {
		t.Error("BINDIR is no longer /usr/local/bin; the rc.d scripts and systemd units name that path")
	}
	for path, unit := range readServiceFiles(t) {
		if !strings.Contains(unit, "ExecStart=") && !strings.Contains(unit, "_binary=") {
			continue
		}
		if !strings.Contains(unit, "/usr/local/bin/halite") {
			t.Errorf("%s does not name a binary under /usr/local/bin, where make install puts them", path)
		}
	}
}
