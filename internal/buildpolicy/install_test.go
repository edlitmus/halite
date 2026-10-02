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
		os      string
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
			notWant: []string{"contrib/rc.d", "pw useradd", "sysadminctl", "launchd"},
		},
	} {
		t.Run(tc.os, func(t *testing.T) {
			stage := t.TempDir()
			args := []string{"-n", "install", "install-service", "install-man",
				"INSTALL_OS=" + tc.os, "HALITE_USER=halite"}
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
