//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// The API's own systemd unit, started by a real systemd, can write the
// token store on the built-in state_dir.
//
// It could not. The unit ran with ProtectSystem=strict and made only
// /var/lib/halite-api writable, while the API's default token store is
// /var/lib/halite/tokens: a service on the default could not create it,
// and `serve` exited 1, which the unit's RestartPreventExitStatus=1 then
// left stopped. internal/config's unit-paths test reads the unit against
// systemd.exec(5); this is the half that test says it cannot do, which is
// to have systemd actually start it.
//
// The unit run is the shipped file with as little changed as makes it
// runnable here: ExecStart runs `token list` -- which opens the token
// store exactly as `serve` does and needs no hub -- from this test binary
// re-executed as halite-api; Type is oneshot so `systemctl start` waits
// for it; Restart is off; and the account is a throwaway, since this
// machine has no `halite`. Every sandbox directive is the shipped one.
//
// And it runs the old StateDirectory first and requires that to fail, so
// a pass here means the sandbox was in force rather than that nothing was
// checked.
//
// It writes /var/lib/halite, so it refuses a machine that already has
// one: there it would be another installation's directory.
func TestLiveAPIUnitCanWriteItsTokenStoreOnTheDefaultStateDir(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to start a real systemd unit")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the systemd unit is Linux's")
	}
	if os.Geteuid() != 0 {
		t.Skip("installing and starting a unit needs root")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not running here")
	}
	for _, p := range []string{"/var/lib/halite", "/var/lib/halite-api", "/var/log/halite"} {
		if _, err := os.Lstat(p); err == nil {
			t.Skipf("%s exists, so this is a machine with halite on it, and this test "+
				"would write into that installation's directories", p)
		}
	}

	const (
		unit    = "halite-live-apiunit"
		account = "halitelive-api"
		work    = "/run/halite-live-apiunit"
	)
	unitPath := "/run/systemd/system/" + unit + ".service"
	sh := func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	if out, err := sh("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin",
		"--user-group", account); err != nil {
		t.Fatalf("useradd: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_, _ = sh("systemctl", "stop", unit)
		_ = os.Remove(unitPath)
		_, _ = sh("systemctl", "daemon-reload")
		_, _ = sh("systemctl", "reset-failed", unit)
		for _, p := range []string{"/var/lib/halite", "/var/lib/halite-api", "/var/log/halite", work} {
			if err := os.RemoveAll(p); err != nil {
				t.Errorf("removing %s: %v", p, err)
			}
		}
		if out, err := sh("userdel", account); err != nil {
			t.Errorf("userdel: %v: %s", err, out)
		}
	})

	// Outside /tmp: the unit's PrivateTmp=yes gives it a /tmp of its own,
	// in which a binary under the test's TempDir does not exist.
	if err := os.MkdirAll(filepath.Join(work, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(work, "halite-api")
	copyExecutable(t, os.Args[0], bin)

	shipped, err := os.ReadFile(filepath.Join("..", "..", "contrib", "systemd", "halite-api.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shipped), "\nStateDirectory=") {
		t.Fatal("the shipped unit has no StateDirectory= line; this test is reading the wrong file")
	}
	runnable := func(stateDirectory string) string {
		var out []string
		for _, line := range strings.Split(string(shipped), "\n") {
			switch {
			case strings.HasPrefix(line, "ExecStart="):
				line = fmt.Sprintf("ExecStart=%s token list --root %s", bin, filepath.Join(work, "etc"))
			case strings.HasPrefix(line, "Type="):
				line = "Type=oneshot"
			case strings.HasPrefix(line, "Restart="):
				line = "Restart=no"
			case strings.HasPrefix(line, "User="):
				line = "User=" + account
			case strings.HasPrefix(line, "Group="):
				line = "Group=" + account
			case strings.HasPrefix(line, "StateDirectory=") && stateDirectory != "":
				line = "StateDirectory=" + stateDirectory
			case line == "[Service]":
				line += "\nEnvironment=" + reexec + "=1"
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	}
	start := func(body string) (string, error) {
		t.Helper()
		if err := os.WriteFile(unitPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := sh("systemctl", "daemon-reload"); err != nil {
			t.Fatalf("daemon-reload: %v: %s", err, out)
		}
		_, _ = sh("systemctl", "reset-failed", unit)
		// This start's own lines: everything after a cursor taken just
		// before it. `-u` with `-n` took the unit's last lines, so the
		// shipped unit's excerpt began with the old unit's failure (Fleet
		// run 37338139260). The invocation ID is no substitute: systemd
		// clears it when a oneshot finishes cleanly, which on run
		// 37342637678 left the successful start with nothing to read.
		//
		// Diagnostic only. The verdict is systemctl's exit status, so a
		// journal that cannot be read is logged rather than failed on.
		cursor := journalCursor()
		_, startErr := sh("systemctl", "start", unit)
		args := []string{"-u", unit, "--no-pager", "-o", "cat"}
		if cursor != "" {
			args = append(args, "--after-cursor="+cursor)
		} else {
			t.Log("no journal cursor before this start, so the excerpt is the unit's whole journal")
		}
		journal, err := sh("journalctl", args...)
		if err != nil {
			t.Logf("reading the journal: %v", err)
		}
		return journal, startErr
	}

	// The old unit, on a machine with no /var/lib/halite: the API has to
	// create /var/lib/halite/tokens and the sandbox does not let it.
	journal, err := start(runnable("halite-api"))
	if err == nil {
		t.Fatalf("the unit with StateDirectory=halite-api alone started and wrote the "+
			"token store, so this run is not under the sandbox it is meant to test:\n%s", journal)
	}
	if !strings.Contains(journal, "read-only file system") {
		t.Errorf("the old unit failed, but not for the reason this test is about:\n%s", journal)
	}
	t.Logf("the old unit, as expected, failed:\n%s", journal)
	if err := os.RemoveAll("/var/lib/halite"); err != nil {
		t.Fatal(err)
	}

	// The shipped one.
	journal, err = start(runnable(""))
	if err != nil {
		t.Fatalf("the shipped unit did not run `token list` on the default state_dir: %v\n%s", err, journal)
	}
	t.Logf("the shipped unit ran:\n%s", journal)

	// systemd made the store, owned by the unit's account at the unit's
	// mode, and the parent it had to create as well is root's 0755 --
	// the claim the unit's comment makes about a host with no hub.
	tokens, err := os.Stat("/var/lib/halite/tokens")
	if err != nil {
		t.Fatalf("the token store does not exist after a successful run: %v", err)
	}
	if !tokens.IsDir() || tokens.Mode().Perm() != 0o700 {
		t.Errorf("/var/lib/halite/tokens is %v, want a 0700 directory", tokens.Mode())
	}
	want, _ := sh("id", "-u", account)
	if st, ok := tokens.Sys().(*syscall.Stat_t); !ok || fmt.Sprint(st.Uid) != want {
		t.Errorf("/var/lib/halite/tokens is not owned by %s (uid %s)", account, want)
	}
	parent, err := os.Stat("/var/lib/halite")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := parent.Sys().(*syscall.Stat_t)
	t.Logf("/var/lib/halite: %v, uid %d", parent.Mode(), st.Uid)
	if st.Uid != 0 || parent.Mode().Perm() != 0o755 {
		t.Errorf("the unit's comment says systemd creates a missing /var/lib/halite as root's "+
			"0755; it is %v, uid %d", parent.Mode(), st.Uid)
	}
}

// journalCursor is the cursor of the journal's newest entry, or empty.
func journalCursor() string {
	out, err := exec.Command("journalctl", "-n", "1", "--show-cursor", "--no-pager", "-o", "cat").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if c, ok := strings.CutPrefix(strings.TrimSpace(line), "-- cursor: "); ok {
			return c
		}
	}
	return ""
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
