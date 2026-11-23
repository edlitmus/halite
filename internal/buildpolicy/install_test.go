package buildpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `make install` on a Mac installs no systemd units, and tells nobody to
// run systemctl or useradd; FreeBSD and Linux still get their own
// service files.
//
// macOS used to fall into the Linux arm of a shell `case`, so a Mac
// install said "installing for Darwin" and then put systemd units in
// /etc/systemd/system, told the operator to `systemctl daemon-reload`,
// and suggested `useradd` -- three things macOS does not have. The
// Makefile now picks each platform's commands with make variables
// ($(INSTALL_SERVICE_$(INSTALL_KIND)) and its siblings), so `make -n`
// prints only the branch a platform would run, and this test reads that
// dry run. Nothing is executed: -n prints the recipes, and the paths
// point into a temporary directory regardless.
//
// INSTALL_OS is set on the command line, which is the same override an
// operator has, so one machine checks all three branches. What this
// cannot show is that the real `uname -s` on each platform selects the
// branch named for it; the `case` in INSTALL_KIND is the whole of that,
// and it is the same `case` CONFDIR and STATEDIR have always used.
//
// It needs BSD make. GNU make 3.81, the Mac's /usr/bin/make, cannot
// evaluate `!=` at all (DIVERGENCE 5.154), so it would print a recipe
// with every variable empty, which proves nothing. On a BSD the system
// make is BSD make; elsewhere it has to be installed as bmake.
func TestInstallOnDarwinInstallsNoSystemdUnits(t *testing.T) {
	mk := bsdMake()
	if mk == "" {
		t.Skip("no BSD make here (no bmake on PATH, and the system make is not BSD make): " +
			"this test reads `make -n install`, and only BSD make evaluates this Makefile")
	}
	root := repoRoot(t)

	for _, tc := range []struct {
		name, os string
		// init is INSTALL_INIT on the command line, the operator's
		// override of the probe; empty leaves the probe to decide, and
		// the probe is pointed at an empty root, so this machine's own
		// /sbin and /run cannot pick the branch. An empty root is the
		// probe's fall-through, which is systemd, as Linux always was.
		init    string
		want    []string
		notWant []string
	}{
		{
			os: "Darwin",
			want: []string{
				"installing for Darwin",
				"no service files",
				"launchd",
				"sysadminctl -addUser",
				"/usr/bin/false",
				"install -m 0644 contrib/man/$b.8",
			},
			notWant: []string{
				"contrib/systemd", "contrib/rc.d",
				".service", ".timer",
				"systemctl", "useradd --system", "pw useradd",
				"/usr/sbin/nologin",
				// The service directory is not checked or named: it
				// resolves to /etc/systemd/system on a Mac.
				"/svc",
			},
		},
		{
			os: "FreeBSD",
			want: []string{
				"installing for FreeBSD",
				"for f in contrib/rc.d/*; do",
				"install -m 0555 $f",
				"pw useradd halite -d /nonexistent -s /usr/sbin/nologin",
				"/svc",
			},
			notWant: []string{"contrib/systemd", "systemctl", "useradd --system", "sysadminctl", "launchd"},
		},
		{
			os: "Linux",
			want: []string{
				"installing for Linux",
				"for f in contrib/systemd/*.service contrib/systemd/*.timer; do",
				"install -m 0644 $f",
				"run systemctl daemon-reload before enabling anything",
				"useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin halite",
				"/svc",
			},
			notWant: []string{"contrib/rc.d", "contrib/openrc", "contrib/sysvinit",
				"pw useradd", "sysadminctl", "launchd"},
		},
		{
			name: "Linux-openrc", os: "Linux", init: "openrc",
			want: []string{
				"installing for Linux",
				"install -m 0755 contrib/openrc/halite-node",
				"rc-update add halite-node default",
				"no OpenRC script ships for halite-hub or halite-api",
				"adduser -S -D -H -h /nonexistent -s /sbin/nologin halite",
				"/svc",
			},
			notWant: []string{"contrib/systemd", "contrib/rc.d", "contrib/sysvinit",
				"systemctl", "useradd --system", "update-rc.d", "launchd"},
		},
		{
			name: "Linux-sysvinit", os: "Linux", init: "sysvinit",
			want: []string{
				"installing for Linux",
				"install -m 0755 contrib/sysvinit/halite-node",
				"update-rc.d halite-node defaults",
				"no sysvinit script ships for halite-hub or halite-api",
				"useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin halite",
				"/svc",
			},
			notWant: []string{"contrib/systemd", "contrib/rc.d", "contrib/openrc",
				"systemctl", "rc-update", "launchd"},
		},
	} {
		name := tc.name
		if name == "" {
			name = tc.os
		}
		t.Run(name, func(t *testing.T) {
			stage := t.TempDir()
			args := []string{"-n", "install", "install-service", "install-man",
				"INSTALL_OS=" + tc.os, "HALITE_USER=halite", "INSTALL_PROBE_ROOT=" + t.TempDir()}
			if tc.init != "" {
				args = append(args, "INSTALL_INIT="+tc.init)
			}
			// Every path the install target writes, staged. SERVICEDIR's
			// stage is <stage>/svc, which the cases above look for.
			for _, v := range [][2]string{{"BINDIR", "bin"}, {"CONFDIR", "etc"},
				{"STATEDIR", "state"}, {"SERVICEDIR", "svc"}, {"MANDIR", "man"},
				{"CACHEDIR", "cache"}, {"LOGDIR", "log"}} {
				args = append(args, v[0]+"="+filepath.Join(stage, v[1]))
			}

			cmd := exec.Command(mk, args...)
			cmd.Dir = root
			cmd.Env = withoutMakeFlags(os.Environ())
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s %s: %v\n%s", mk, strings.Join(args, " "), err, out)
			}
			got := string(out)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the %s dry run does not contain %q", tc.os, w)
				}
			}
			for _, n := range tc.notWant {
				if strings.Contains(got, n) {
					t.Errorf("the %s dry run contains %q", tc.os, n)
				}
			}
			if t.Failed() {
				t.Logf("%s dry run:\n%s", tc.os, got)
			}
		})
	}
}

// bsdMake is the BSD make to run, or "" when there is none.
func bsdMake() string {
	if p, err := exec.LookPath("bmake"); err == nil {
		return p
	}
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		if p, err := exec.LookPath("make"); err == nil {
			return p
		}
	}
	return ""
}

// withoutMakeFlags drops what an enclosing make passes down, so a test
// run from `make check` sees the same Makefile a person would.
func withoutMakeFlags(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch {
		case k == "MAKEFLAGS", k == "MFLAGS", k == "MAKELEVEL", k == "MAKEOVERRIDES",
			strings.HasPrefix(k, ".MAKE"):
			continue
		}
		out = append(out, e)
	}
	return out
}

// `make install-man MANDIR=<dir>/stage/man8` installs the pages when
// <dir>/stage does not exist yet.
//
// Issue 309 asked for a run with a staged MANDIR, and the example it gave
// is the one that failed: the recipe installed only if MANDIR or its
// parent was writable, so staging into a directory that was about to be
// made read as "cannot write", said so on stderr, and exited 0 with
// nothing installed. A person checking what an install would do saw a
// success and an empty directory. The pages are counted here, not the
// exit status, because the status was the part that was wrong.
//
// This runs the target for real, into a temporary directory, with every
// make this machine has that can evaluate the Makefile: GNU make 4 and a
// BSD make read the recipe differently (5.281 is the same lesson), and the
// stock macOS make does not support `!=` and is skipped, as the Makefile
// itself refuses it. It does not show the real thing as root, or on a
// platform whose `install` is not this one's; it shows the staged one.
func TestInstallManStagesIntoADirectoryThatDoesNotExistYet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install-man runs install(1) and sh; there is no Windows install")
	}
	root := repoRoot(t)
	var makes []string
	for _, name := range []string{"bmake", "gmake", "make"} {
		if p, err := exec.LookPath(name); err == nil {
			makes = append(makes, p)
		}
	}
	if len(makes) == 0 {
		t.Skip("no make on this machine")
	}
	want := []string{"man5/halite-policy.5", "man5/halite-sls.5", "man7/halite.7",
		"man8/halite-api.8", "man8/halite-hub.8", "man8/halite-node.8"}
	ran := 0
	for _, mk := range makes {
		t.Run(filepath.Base(mk), func(t *testing.T) {
			// Not -n: a dry run prints the refusal instead of running it, so the
			// macOS make that cannot evaluate `!=` passed the probe and failed
			// the install.
			probe := exec.Command(mk, "make-supports-bang")
			probe.Dir, probe.Env = root, withoutMakeFlags(os.Environ())
			if out, err := probe.CombinedOutput(); err != nil {
				t.Skipf("%s cannot evaluate the Makefile: %v\n%s", mk, err, out)
			}
			stage := filepath.Join(t.TempDir(), "stage")
			cmd := exec.Command(mk, "install-man", "MANDIR="+filepath.Join(stage, "man8"))
			cmd.Dir, cmd.Env = root, withoutMakeFlags(os.Environ())
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s install-man: %v\n%s", mk, err, out)
			}
			ran++
			for _, w := range want {
				if _, err := os.Stat(filepath.Join(stage, w)); err != nil {
					t.Errorf("%s: %s was not installed: %v\n%s", mk, w, err, out)
				}
			}
		})
	}
	if ran == 0 {
		t.Skip("no make on this machine could evaluate the Makefile, so install-man was not run")
	}
}

// `make install` puts everything in place on a host that has nothing
// under /usr/local yet.
//
// A FreeBSD host with no packages has an empty /usr/local: no bin, no
// etc, no etc/rc.d. `make install` run as root in a fresh 14.5-RELEASE
// jail refused with "cannot write /usr/local/etc/halite — run make install
// as root", because it asked only whether a directory or its parent was
// writable; and where the parent was, it went on to `install` a file into
// a directory that did not exist ("No such file or directory"). The
// chapter 30 runbook's own `install … /usr/local/etc/rc.d/` failed the same
// way (DIVERGENCE 5.625).
//
// This runs the real target, not a dry run, from a copy of the parts of
// the repository it reads, with stand-in programs in bin/ so that nothing
// has to be built, into a staged root where every path is two or more
// directories below anything that exists. The staged root is what makes it
// runnable unprivileged; what it cannot show is root on a real /usr/local,
// which the jail run did. HALITE_USER names an account that does not
// exist, so the directories are left to the user running the test, as the
// target says it does.
func TestInstallMakesTheDirectoriesItInstallsInto(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install runs install(1) and sh; there is no Windows install")
	}
	root := repoRoot(t)
	var makes []string
	for _, name := range []string{"bmake", "gmake", "make"} {
		if p, err := exec.LookPath(name); err == nil {
			makes = append(makes, p)
		}
	}
	if len(makes) == 0 {
		t.Skip("no make on this machine")
	}

	// A copy of what the install targets read.
	src := t.TempDir()
	copyTree(t, root, src, "Makefile", "go.mod", "contrib/rc.d", "contrib/systemd",
		"contrib/openrc", "contrib/sysvinit", "contrib/man")
	if err := os.MkdirAll(filepath.Join(src, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"halite-node", "halite-hub", "halite-api"} {
		if err := os.WriteFile(filepath.Join(src, "bin", b), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ran := 0
	for _, mk := range makes {
		for _, tc := range []struct {
			os, host, servicedir string
			services             []string
		}{
			{"FreeBSD", "", "usr/local/etc/rc.d", []string{"halite_hub", "halite_node", "halite_api"}},
			{"Linux", "systemd", "etc/systemd/system", []string{"halite-hub.service", "halite-node.service", "halite-api.service"}},
			{"Linux", "openrc", "etc/init.d", []string{"halite-node"}},
			{"Linux", "sysvinit", "etc/init.d", []string{"halite-node"}},
		} {
			t.Run(filepath.Base(mk)+"/"+tc.os+tc.host, func(t *testing.T) {
				probe := exec.Command(mk, "make-supports-bang")
				probe.Dir, probe.Env = src, withoutMakeFlags(os.Environ())
				if out, err := probe.CombinedOutput(); err != nil {
					t.Skipf("%s cannot evaluate the Makefile: %v\n%s", mk, err, out)
				}
				stage := filepath.Join(t.TempDir(), "fresh")
				args := []string{"install", "INSTALL_OS=" + tc.os, "HALITE_USER=halite-test-no-such-account"}
				if tc.host != "" {
					// Probed, not overridden: this is the path a real
					// install on that host takes.
					args = append(args, "INSTALL_PROBE_ROOT="+fakeInitRoot(t, tc.host))
				}
				for _, v := range [][2]string{{"BINDIR", "usr/local/bin"}, {"CONFDIR", "usr/local/etc/halite"},
					{"STATEDIR", "var/db/halite"}, {"CACHEDIR", "var/cache/halite"}, {"LOGDIR", "var/log/halite"},
					{"SERVICEDIR", tc.servicedir}, {"MANDIR", "usr/local/share/man/man8"}} {
					args = append(args, v[0]+"="+filepath.Join(stage, v[1]))
				}
				cmd := exec.Command(mk, args...)
				cmd.Dir, cmd.Env = src, withoutMakeFlags(os.Environ())
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s install into a root with nothing in it: %v\n%s", mk, err, out)
				}
				ran++
				want := []string{"usr/local/bin/halite-hub", "usr/local/bin/halite-node", "usr/local/bin/halite-api",
					"usr/local/share/man/man8/halite-hub.8"}
				for _, s := range tc.services {
					want = append(want, filepath.Join(tc.servicedir, s))
				}
				for _, w := range want {
					if _, err := os.Stat(filepath.Join(stage, w)); err != nil {
						t.Errorf("%s was not installed: %v", w, err)
					}
				}
				if tc.host == "openrc" || tc.host == "sysvinit" {
					// Both kinds install to the same name, so the name
					// alone cannot tell which script landed; the bytes and
					// the mode can.
					checkInstalledScript(t, filepath.Join(src, "contrib", tc.host, "halite-node"),
						filepath.Join(stage, tc.servicedir, "halite-node"))
				}
				for _, d := range []string{"usr/local/etc/halite/pki", "var/db/halite", "var/cache/halite", "var/log/halite"} {
					if fi, err := os.Stat(filepath.Join(stage, d)); err != nil || !fi.IsDir() {
						t.Errorf("the directory %s was not made: %v", d, err)
					}
				}
				if t.Failed() {
					t.Logf("%s output:\n%s", mk, out)
				}
			})
		}
	}
	if ran == 0 {
		t.Skip("no make on this machine could evaluate the Makefile, so install was not run")
	}
}

// copyTree copies the named files and directories from one root to
// another, keeping their modes.
func copyTree(t *testing.T, from, to string, names ...string) {
	t.Helper()
	for _, n := range names {
		err := filepath.WalkDir(filepath.Join(from, n), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(from, p)
			if err != nil {
				return err
			}
			dst := filepath.Join(to, rel)
			info, err := d.Info()
			if err != nil {
				return err
			}
			if d.IsDir() {
				return os.MkdirAll(dst, 0o755)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dst, b, info.Mode().Perm())
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// `make install` on Linux picks the service files by asking the host what
// its init is, not by assuming systemd.
//
// Linux used to be systemd unconditionally, so an Alpine host got units
// in /etc/systemd/system and nothing OpenRC could start, and a Debian
// converted to sysvinit the same (issue 812). INSTALL_INIT now probes the
// filesystem at install time. This builds a fake root per kind of host,
// holding only the files that kind has, and reads which branch the dry
// run takes with INSTALL_PROBE_ROOT pointed at it -- the same probe a real
// install runs against /, moved.
//
// The rows that matter most are the ones that must *not* move: a systemd
// host with OpenRC's package beside it, an image built in a chroot whose
// /sbin/init is a link to systemd, and a Debian container with
// update-rc.d and no init at all all stay systemd, as they were.
//
// What it cannot show is that a real Alpine or a real sysvinit Debian has
// these files where the probe looks. That was read from the distributions'
// packaging here. The probe was then run on Alpine 3.24, a sysvinit Debian
// 13 and Ubuntu 26.04, and picked openrc, sysvinit and systemd (DIVERGENCE
// 5.716.3).
func TestInstallPicksTheServiceFilesByTheHostsInit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe is sh; there is no Windows install")
	}
	root := repoRoot(t)
	var makes []string
	for _, name := range []string{"bmake", "gmake", "make"} {
		if p, err := exec.LookPath(name); err == nil {
			makes = append(makes, p)
		}
	}
	if len(makes) == 0 {
		t.Skip("no make on this machine")
	}
	marks := map[string]string{
		"systemd":  "for f in contrib/systemd/*.service contrib/systemd/*.timer; do",
		"openrc":   "install -m 0755 contrib/openrc/halite-node",
		"sysvinit": "install -m 0755 contrib/sysvinit/halite-node",
	}
	ran := 0
	for _, mk := range makes {
		probe := exec.Command(mk, "make-supports-bang")
		probe.Dir, probe.Env = root, withoutMakeFlags(os.Environ())
		if out, err := probe.CombinedOutput(); err != nil {
			t.Logf("%s cannot evaluate the Makefile, so it is not asked: %v\n%s", mk, err, out)
			continue
		}
		for _, host := range []string{"systemd", "openrc", "sysvinit",
			"systemd-with-openrc-installed", "systemd-image-in-a-chroot", "debian-container", "empty"} {
			t.Run(filepath.Base(mk)+"/"+host, func(t *testing.T) {
				want := host
				if _, ok := marks[host]; !ok {
					want = "systemd"
				}
				// No SERVICEDIR: the default follows the kind, and a dry
				// run writes nothing, so the real path is safe to read.
				args := []string{"-n", "install-service", "INSTALL_OS=Linux",
					"INSTALL_PROBE_ROOT=" + fakeInitRoot(t, host)}
				cmd := exec.Command(mk, args...)
				cmd.Dir, cmd.Env = root, withoutMakeFlags(os.Environ())
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %s: %v\n%s", mk, strings.Join(args, " "), err, out)
				}
				ran++
				for kind, mark := range marks {
					if got := strings.Contains(string(out), mark); got != (kind == want) {
						t.Errorf("a %s host: the dry run installs %s files = %v, want %v\n%s",
							host, kind, got, kind == want, out)
					}
				}
				dirs := map[string]string{"systemd": `install -d -m 0755 "/etc/systemd/system"`,
					"openrc": `"/etc/init.d/halite-node"`, "sysvinit": `"/etc/init.d/halite-node"`}
				if !strings.Contains(string(out), dirs[want]) {
					t.Errorf("a %s host: the dry run does not install into %s\n%s", host, dirs[want], out)
				}
			})
		}
	}
	if ran == 0 {
		t.Skip("no make on this machine could evaluate the Makefile, so the probe was not run")
	}
}

// fakeInitRoot makes a directory holding only the files the probe in the
// Makefile's INSTALL_INIT looks at, laid out as one kind of host has them.
// The symlinks are relative so that they resolve inside the fake root.
func fakeInitRoot(t *testing.T, host string) string {
	t.Helper()
	r := t.TempDir()
	file := func(rel string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(r, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	dir := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(r, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		t.Helper()
		p := filepath.Join(r, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
	switch host {
	case "systemd":
		// A Debian or Ubuntu booted by systemd: the runtime directory,
		// and the sysv compatibility a Debian still carries.
		dir("run/systemd/system")
		file("lib/systemd/systemd", 0o755)
		link("../lib/systemd/systemd", "sbin/init")
		file("usr/sbin/update-rc.d", 0o755)
	case "systemd-with-openrc-installed":
		dir("run/systemd/system")
		file("sbin/openrc-run", 0o755)
		file("lib/systemd/systemd", 0o755)
		link("../lib/systemd/systemd", "sbin/init")
	case "openrc":
		// Alpine: busybox is init, and OpenRC's runner is there.
		file("bin/busybox", 0o755)
		link("../bin/busybox", "sbin/init")
		file("sbin/openrc-run", 0o755)
	case "sysvinit":
		// Debian with sysvinit-core: /sbin/init is that package's own
		// binary, not a link, and update-rc.d is there.
		file("sbin/init", 0o755)
		file("usr/sbin/update-rc.d", 0o755)
	case "systemd-image-in-a-chroot":
		// An image being built: systemd installed, not running.
		file("lib/systemd/systemd", 0o755)
		link("../lib/systemd/systemd", "sbin/init")
		file("usr/sbin/update-rc.d", 0o755)
	case "debian-container":
		// Debian's base image has init-system-helpers and no init.
		file("usr/sbin/update-rc.d", 0o755)
	case "empty":
	default:
		t.Fatalf("fakeInitRoot: no such host %q", host)
	}
	return r
}

// checkInstalledScript fails unless the file at got is the script at want,
// byte for byte, and executable.
func checkInstalledScript(t *testing.T, want, got string) {
	t.Helper()
	wantBody, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	gotBody, err := os.ReadFile(got)
	if err != nil {
		t.Errorf("%s was not installed: %v", got, err)
		return
	}
	if string(gotBody) != string(wantBody) {
		t.Errorf("%s is not %s", got, want)
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is mode %v, not executable", got, fi.Mode())
	}
}
