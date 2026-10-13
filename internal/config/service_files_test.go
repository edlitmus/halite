package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
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
	repo := filepath.Join("..", "..")
	err := repotree.Walk(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
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

// rc.subr runs `$command $rc_flags $command_args`, and rc_flags is the value
// of ${name}_flags. In these scripts `command` is /usr/sbin/daemon, so a
// script that documents ${name}_flags as "extra arguments to the program"
// and does not take the variable away from rc.subr hands the operator's
// flags to daemon(8). It stops with `daemon: unrecognized option --names`
// and its usage text; the same value was also repeated at the end of
// command_args, which is the only place it was meant to go. Found by setting
// halite_hub_flags='--names …' on a FreeBSD 15.1 host, as handbook chapter
// 30 says to; it only ever worked while empty, which is how every test and
// every earlier lab run left it. DIVERGENCE 5.283.
//
// This reads the scripts, since rc.subr is not here to run. The shape it
// holds them to is the one that was run on the host: the operator's value is
// copied to ${name}_args, the variable rc.subr reads is emptied, and
// command_args uses ${name}_args.
func TestRcScriptsKeepTheirFlagsAwayFromDaemon(t *testing.T) {
	checked := 0
	for path, body := range readServiceFiles(t) {
		if !strings.Contains(path, "rc.d") || !strings.Contains(body, `command="/usr/sbin/daemon"`) {
			continue
		}
		checked++
		name := filepath.Base(path)
		args := regexp.MustCompile(`(?s)\ncommand_args="(.*?)"\n`).FindString(body)
		if args == "" {
			t.Errorf("%s runs daemon(8) and has no command_args", path)
			continue
		}
		if strings.Contains(args, name+"_flags") {
			t.Errorf("%s puts ${%s_flags} in command_args, where rc.subr will also pass it to daemon(8)", path, name)
		}
		if !strings.Contains(args, name+"_args") {
			t.Errorf("%s does not pass ${%s_args} to the program", path, name)
		}
		if !strings.Contains(body, name+`_args="${`+name+`_flags}"`) || !strings.Contains(body, "\n"+name+`_flags=""`) {
			t.Errorf("%s does not move %s_flags to %s_args and empty it before rc.subr reads it", path, name, name)
		}
	}
	if checked < 3 {
		t.Fatalf("only %d daemon-wrapped rc.d scripts were checked; this has stopped checking", checked)
	}
}
