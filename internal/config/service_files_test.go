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

// readServiceFiles returns every file under contrib/rc.d,
// contrib/systemd, contrib/openrc and contrib/sysvinit, walked rather
// than listed so the FIPS drop-ins in their subdirectories are read too.
func readServiceFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"rc.d", "systemd", "openrc", "sysvinit"} {
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

// A unit that runs the node leaves the kernel's module tree reachable.
//
// The node manages kernel modules -- kmod and modprobe load and unload
// them, kernelpkg installs kernels -- and halite-node.service and
// halite-highstate.service both set `ProtectKernelModules=yes`, which
// denies loading and makes /usr/lib/modules inaccessible. Run under
// that setting by a user manager (systemd 255), `halite-node call
// kmod.check_available mod=loop` answered False on a machine where it
// answers True, and gave no error. DIVERGENCE 5.427. The hub and API do
// not manage modules and keep the setting.
func TestTheNodeUnitsLeaveKernelModulesReachable(t *testing.T) {
	checked := 0
	on := regexp.MustCompile(`(?m)^\s*ProtectKernelModules\s*=\s*(?i:yes|true|on|1)\s*$`)
	for path, body := range readServiceFiles(t) {
		if !strings.HasSuffix(path, ".service") || !strings.Contains(body, "/halite-node ") {
			continue
		}
		checked++
		if on.MatchString(body) {
			t.Errorf("%s runs the node with ProtectKernelModules on, which hides "+
				"/usr/lib/modules from kmod, modprobe and kernelpkg", path)
		}
	}
	if checked < 2 {
		t.Fatalf("found %d units running halite-node, want the node and highstate units; "+
			"this check has stopped checking", checked)
	}
}

// A unit that runs the node shares the host's temporary directories.
//
// `PrivateTmp=yes` gives the unit its own /tmp and /var/tmp, discarded
// with the unit. A node manages the host, so a `file.managed` under either
// wrote into the private pair: the job said the file was created, a shell
// on the host could not see it, and after the agent's next restart it was
// gone and the next rehearsal reported it missing again. Seen on AlmaLinux
// 8.10 and openSUSE Leap 16.0 lab nodes, as false drift in chapter 24's
// mailed report after every reboot and upgrade. DIVERGENCE 5.709. The hub
// and API manage nothing on the host and keep the setting.
func TestTheNodeUnitsShareTheHostsTemporaryDirectories(t *testing.T) {
	checked := 0
	private := regexp.MustCompile(`(?m)^\s*PrivateTmp\s*=\s*(?i:yes|true|on|1|disconnected)\s*$`)
	for path, body := range readServiceFiles(t) {
		if !strings.HasSuffix(path, ".service") || !strings.Contains(body, "/halite-node ") {
			continue
		}
		checked++
		if private.MatchString(body) {
			t.Errorf("%s runs the node with a private /tmp and /var/tmp, so states "+
				"that manage files there change a namespace the host never sees", path)
		}
	}
	if checked < 2 {
		t.Fatalf("found %d units running halite-node, want the node and highstate units; "+
			"this check has stopped checking", checked)
	}
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
//
// The check skips once something registers a SIGHUP handler, because a
// handler could make a reload real. Every handler it has read is listed
// below with what it does, and does not count. `halite-node oneshot`
// began catching SIGHUP on 2026-10-15 to remove its inline tree, and from
// then on this check skipped on every run while the package reported ok
// (DIVERGENCE 5.713.2). A listed file that stops registering the signal
// fails the test, so the list cannot go on excusing a handler that is
// gone.
func TestNoUnitOffersAReloadThatWouldKillTheService(t *testing.T) {
	read := map[string]string{
		"cmd/halite-node/oneshot.go": "the agentless run catches SIGHUP to remove its inline " +
			"tree and then exits 128+n, as the default disposition would; it is not a " +
			"service and reloads nothing",
	}
	seen := map[string]bool{}
	var unread []string
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
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if _, ok := read[rel]; ok {
				seen[rel] = true
			} else {
				unread = append(unread, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, why := range read {
		if !seen[rel] {
			t.Errorf("%s no longer registers SIGHUP; drop it from the handlers this check has read (%s)", rel, why)
		}
	}
	if len(unread) > 0 {
		t.Skipf("%s handle SIGHUP, and this check has not read what they do with it; "+
			"a reload directive may be correct again", strings.Join(unread, ", "))
	}
	// An OpenRC script offers one by naming it in extra_started_commands
	// or extra_commands, and an LSB script by a `reload` arm in its case
	// on $1. force-reload is not one: LSB requires the action, and
	// contrib/sysvinit maps it to a restart.
	openrcReload := regexp.MustCompile(`^extra_(started_)?commands=.*\breload\b`)
	lsbReload := regexp.MustCompile(`^(\S+\|)*reload(\|\S+)*\)`)
	for path, body := range readServiceFiles(t) {
		for i, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.HasPrefix(trimmed, "ExecReload=") || openrcReload.MatchString(trimmed) ||
				lsbReload.MatchString(trimmed) {
				t.Errorf("%s:%d offers a reload, and SIGHUP terminates this build: %s",
					path, i+1, trimmed)
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
		script := strings.Contains(path, "openrc") || strings.Contains(path, "sysvinit")
		if !strings.Contains(unit, "ExecStart=") && !strings.Contains(unit, "_binary=") && !script {
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

// The OpenRC script runs the node under supervise-daemon, and sets none of
// the variables that mean something else there.
//
// rc.subr's `${name}_program` (above) is the shape: a familiar name that
// the init system reads differently, so the script looks right and runs
// wrong. OpenRC's documentation names three for supervise-daemon. `pidfile`
// is the supervisor's own pid file ("not the pid file of the process that
// is being supervised", supervise-daemon(8)), so pointing it at the
// program's is the FreeBSD script's habit carried into the wrong place.
// `command_background` belongs to start-stop-daemon and forks the program,
// which a supervised daemon must not do. `command_user` is not wrong, but a
// node runs as root, which is what leaving it unset gives, and setting it
// to anything else is the unprivileged node the unit refuses to be.
//
// What it holds is read from OpenRC's documentation, not from running
// openrc-run (DIVERGENCE 5.713); the lab is where that is checked.
func TestTheOpenRCScriptIsSupervisedAndAvoidsSupervisorTraps(t *testing.T) {
	path := filepath.Join("..", "..", "contrib", "openrc", "halite-node")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.HasPrefix(text, "#!/sbin/openrc-run\n") {
		t.Errorf("%s does not start #!/sbin/openrc-run, so OpenRC will not run it as a service", path)
	}
	assigned := map[string]string{}
	assignment := regexp.MustCompile(`^([a-z_]+)=(.*)$`)
	for _, line := range strings.Split(text, "\n") {
		if m := assignment.FindStringSubmatch(line); m != nil {
			assigned[m[1]] = m[2]
		}
	}
	if got := assigned["supervisor"]; got != "supervise-daemon" {
		t.Errorf("%s sets supervisor=%q, want supervise-daemon: nothing else restarts a node that dies", path, got)
	}
	for _, trap := range []string{"pidfile", "command_background", "command_user"} {
		if v, ok := assigned[trap]; ok {
			t.Errorf("%s sets %s=%s; under supervise-daemon that is not what it looks like", path, trap, v)
		}
	}
	// The names openrc-run reads; internal/docsaudit holds the flags in
	// command_args to the binary's usage, and this holds that they are in
	// the variable openrc-run passes on.
	if assigned["command"] != `"/usr/local/bin/halite-node"` {
		t.Errorf("%s sets command=%s, want the binary make install puts in /usr/local/bin", path, assigned["command"])
	}
	if !strings.HasPrefix(assigned["command_args"], `"connect `) {
		t.Errorf("%s sets command_args=%s, want `connect` and its flags", path, assigned["command_args"])
	}
	if !strings.Contains(assigned["retry"], "TERM/") {
		t.Errorf("%s sets no TERM retry schedule; supervise-daemon's default kills after 5 seconds", path)
	}
}

// The sysvinit script carries the LSB header update-rc.d reads, naming
// itself, and the actions LSB requires.
//
// update-rc.d takes the runlevels from `Default-Start` and `Default-Stop`
// and the ordering from the `Required-*` facilities, and `Provides` is the
// name other scripts depend on, so it is this file's own name. LSB's
// required actions are start, stop, restart, force-reload and status.
// This is read from the LSB and Debian documentation; update-rc.d itself
// has not read the header yet (DIVERGENCE 5.713).
func TestTheSysvinitScriptCarriesItsLSBHeader(t *testing.T) {
	path := filepath.Join("..", "..", "contrib", "sysvinit", "halite-node")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	begin := strings.Index(text, "### BEGIN INIT INFO\n")
	end := strings.Index(text, "### END INIT INFO\n")
	if begin < 0 || end < begin {
		t.Fatalf("%s has no LSB header, so update-rc.d cannot place it", path)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(text[begin:end], "\n") {
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "# "), ":")
		if ok && !strings.HasPrefix(key, " ") {
			fields[key] = strings.TrimSpace(value)
		}
	}
	want := map[string]string{
		"Provides":      filepath.Base(path),
		"Default-Start": "2 3 4 5",
		"Default-Stop":  "0 1 6",
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("%s: LSB %s is %q, want %q", path, key, fields[key], value)
		}
	}
	for _, key := range []string{"Required-Start", "Required-Stop", "Short-Description"} {
		if fields[key] == "" {
			t.Errorf("%s: LSB header has no %s", path, key)
		}
	}
	if !strings.Contains(fields["Required-Start"], "$network") {
		t.Errorf("%s starts before the network, which the node needs to reach its hub", path)
	}
	for _, action := range []string{"start)", "stop)", "status)", "restart|force-reload)"} {
		if !strings.Contains(text, "\n"+action+"\n") {
			t.Errorf("%s has no %s arm; LSB requires the action", path, strings.TrimSuffix(action, ")"))
		}
	}
}
