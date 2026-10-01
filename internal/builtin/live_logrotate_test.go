//go:build linux || freebsd

package builtin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The logrotate module, driven against the real logrotate.
//
// The oracle is logrotate itself, never this module's parser: after each
// write the configuration is handed to `logrotate -d`, and the test reads
// what logrotate says it would do with the throwaway log -- "(9
// rotations)", "after 1 days", "compressing log with" -- so a parser that
// agreed with itself and not with the tool would fail here. `-d` runs no
// script, renames no log and writes no state (measured before this was
// written, with scripts that touch a file and a forced rotation), and the
// state file it is given is a throwaway one besides.
//
// Nothing here edits a file the host already had. The first test adds a
// drop-in of its own to the system's include directory and names the
// system's configuration as conf_file, so the include walk and the
// whole-configuration check run against what the distribution ships; it
// records the bytes of the system configuration and of every drop-in
// first, and fails -- after putting them back -- if any moved. The
// second brings a configuration of its own, so it runs wherever
// logrotate is installed, including a FreeBSD with the port.

func logrotateLiveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this writes logrotate configuration as root")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set and this is not root; logrotate refuses configuration it cannot trust, and the include directory is root's")
	}
	if _, err := exec.LookPath("logrotate"); err != nil {
		t.Skip("this host has no logrotate (FreeBSD rotates with newsyslog unless the sysutils/logrotate port is installed)")
	}
}

// On a FreeBSD without the port, the module says why there is nothing to
// read rather than failing on a path. This is the only test here that
// needs logrotate to be absent.
func TestLiveLogrotateNamesNewsyslogOnAFreeBSDWithoutThePort(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to check what this host says")
	}
	if runtime.GOOS != "freebsd" {
		t.Skip("newsyslog is FreeBSD's; elsewhere a missing logrotate is only a missing package")
	}
	if _, err := os.Stat(logrotateDefaultConf(runtime.GOOS)); err == nil {
		t.Skip("the sysutils/logrotate port is installed here; TestLiveLogrotate* cover this host")
	}
	_, err := New().Exec.Call(liveRoot(), "logrotate.show_conf", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "newsyslog") {
		t.Fatalf("show_conf on a FreeBSD with no logrotate: %v", err)
	}
	t.Log(err)
}

// logrotateSays is what logrotate -d prints about a configuration.
func logrotateSays(t *testing.T, conf string) string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "status")
	out, _ := exec.Command("logrotate", "-d", "-s", state, conf).CombinedOutput()
	return string(out)
}

// logrotateForcedSays is the same with -f, which makes logrotate say
// what it would do to a log that is not due, compression included.
func logrotateForcedSays(t *testing.T, conf string) string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "status")
	out, _ := exec.Command("logrotate", "-d", "-f", "-s", state, conf).CombinedOutput()
	return string(out)
}

// patternLine is logrotate's "rotating pattern:" line for one log.
func patternLine(t *testing.T, said, log string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^rotating pattern: ` + regexp.QuoteMeta(log) + ` (.*)$`)
	m := re.FindStringSubmatch(said)
	if m == nil {
		t.Fatalf("logrotate -d said nothing about %s:\n%s", log, said)
	}
	return m[1]
}

func snapshotFiles(t *testing.T, paths ...string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[p] = b
	}
	return out
}

func liveSet(t *testing.T, test bool, conf, key string, val, setting any) (bool, error) {
	t.Helper()
	c := liveRoot()
	c.Test = test
	args := value.MapOf("name", "live", "key", key, "value", val, "conf_file", conf)
	if setting != nil {
		args.Set("setting", setting)
	}
	res, err := logrotateSetState(c, args)
	if err != nil {
		return false, err
	}
	if res.Result != nil && !*res.Result {
		return false, fmt.Errorf("%s", res.Comment)
	}
	return res.HasChanges(), nil
}

func TestLiveLogrotateEditsADropInOfTheSystemsConfiguration(t *testing.T) {
	logrotateLiveGate(t)
	conf := logrotateDefaultConf(runtime.GOOS)
	if _, err := os.Stat(conf); err != nil {
		t.Skipf("%s does not exist here, so there is no system configuration to include a drop-in from; "+
			"TestLiveLogrotateDrivesAConfigurationOfItsOwn covers this host", conf)
	}
	cfg, err := loadLogrotate(conf)
	if err != nil {
		t.Fatalf("the module cannot read this host's own configuration: %v", err)
	}
	incDir := ""
	for _, d := range cfg.main.Globals {
		if d.Key == "include" && len(d.Args) == 1 {
			if fi, err := os.Stat(d.Args[0]); err == nil && fi.IsDir() {
				incDir = d.Args[0]
			}
		}
	}
	if incDir == "" {
		t.Skipf("%s includes no directory to add a drop-in to", conf)
	}

	// Everything the host had, to compare and put back.
	watched := []string{conf}
	for _, f := range cfg.included {
		watched = append(watched, f.Path)
	}
	before := snapshotFiles(t, watched...)
	t.Cleanup(func() {
		for p, b := range before {
			if now, _ := os.ReadFile(p); !bytes.Equal(now, b) {
				t.Errorf("%s changed under the test; putting it back", p)
				_ = os.WriteFile(p, b, 0o644)
			}
		}
	})

	logs := t.TempDir()
	log := filepath.Join(logs, "app.log")
	if err := os.WriteFile(log, []byte("a line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dropIn := filepath.Join(incDir, fmt.Sprintf("halite-test-%d", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(dropIn) })
	if err := os.WriteFile(dropIn, []byte(log+" {\n\tmissingok\n\trotate 2\n\tweekly\n}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// A group a file of root's would not otherwise have -- gid 4 is adm on
	// Debian and tty on FreeBSD -- to see the write keep it.
	const adm = 4
	_ = os.Chown(dropIn, 0, adm)
	if got := patternLine(t, logrotateSays(t, conf), log); !strings.Contains(got, "(2 rotations)") {
		t.Fatalf("logrotate does not read the drop-in as written: %s", got)
	}

	// Test mode predicts and writes nothing.
	changed, err := liveSet(t, true, conf, log, "rotate", int64(9))
	if err != nil || !changed {
		t.Fatalf("test mode: changed=%v err=%v", changed, err)
	}
	if got := patternLine(t, logrotateSays(t, conf), log); !strings.Contains(got, "(2 rotations)") {
		t.Fatalf("test mode changed what logrotate does: %s", got)
	}

	changed, err = liveSet(t, false, conf, log, "rotate", int64(9))
	if err != nil || !changed {
		t.Fatalf("rotate 9: changed=%v err=%v", changed, err)
	}
	if got := patternLine(t, logrotateSays(t, conf), log); !strings.Contains(got, "(9 rotations)") {
		t.Fatalf("after rotate 9 logrotate says: %s", got)
	}
	fi, err := os.Stat(dropIn)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("the drop-in's mode is %o after the write, want 0640", fi.Mode().Perm())
	}
	m := value.NewMap(4)
	addOwnership(m, dropIn, fi)
	if gid, _ := m.GetString("gid"); gid != int64(adm) {
		t.Errorf("the drop-in's group is %v after the write, want %d", gid, adm)
	}

	// Converged, in logrotate's spelling: 011 is nine.
	written, _ := os.ReadFile(dropIn)
	changed, err = liveSet(t, false, conf, log, "rotate", "011")
	if err != nil || changed {
		t.Fatalf("rotate 011 is rotate 9: changed=%v err=%v", changed, err)
	}
	if again, _ := os.ReadFile(dropIn); !bytes.Equal(again, written) {
		t.Error("a converged set rewrote the file")
	}

	// daily takes weekly's place rather than being added above it.
	if _, err := liveSet(t, false, conf, log, "daily", true); err != nil {
		t.Fatal(err)
	}
	if got := patternLine(t, logrotateSays(t, conf), log); !strings.Contains(got, "after 1 days") {
		t.Fatalf("after daily logrotate says: %s", got)
	}

	// What logrotate would refuse is not written.
	written, _ = os.ReadFile(dropIn)
	for _, bad := range []string{"four", "1m"} {
		key := "rotate"
		if bad == "1m" {
			key = "size"
		}
		if _, err := liveSet(t, false, conf, log, key, bad); err == nil {
			t.Errorf("%s %s was accepted, and logrotate refuses it", key, bad)
		}
	}
	if again, _ := os.ReadFile(dropIn); !bytes.Equal(again, written) {
		t.Errorf("a refused set changed the drop-in:\n%s", again)
	}
}

func TestLiveLogrotateDrivesAConfigurationOfItsOwn(t *testing.T) {
	logrotateLiveGate(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	incDir := filepath.Join(dir, "logrotate.d")
	if err := os.Mkdir(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logA := filepath.Join(dir, "a.log")
	logB := filepath.Join(dir, "b.log")
	for _, l := range []string{logA, logB} {
		if err := os.WriteFile(l, []byte("a line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(dir, "logrotate.conf")
	if err := os.WriteFile(conf, []byte("# a test configuration\nweekly\nrotate 4\ninclude "+incDir+"\n\n"+
		logA+" {\n    missingok\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "b"), []byte(logB+"\n{\n  missingok\n  nocompress\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A global, read back from logrotate as what a.log inherits.
	if changed, err := liveSet(t, false, conf, "rotate", int64(3), nil); err != nil || !changed {
		t.Fatalf("rotate 3: changed=%v err=%v", changed, err)
	}
	if got := patternLine(t, logrotateSays(t, conf), logA); !strings.Contains(got, "(3 rotations)") {
		t.Fatalf("after the global rotate 3 logrotate says about a.log: %s", got)
	}
	if changed, err := liveSet(t, false, conf, "rotate", int64(3), nil); err != nil || changed {
		t.Fatalf("second rotate 3: changed=%v err=%v", changed, err)
	}

	// The named file itself has no whole-configuration check after it is
	// written -- the check before the write already read everything it
	// includes -- so this refusal is that first check's alone.
	written, _ := os.ReadFile(conf)
	if _, err := liveSet(t, false, conf, "rotate", "four", nil); err == nil {
		t.Error("rotate four was accepted, and logrotate refuses it")
	}
	if again, _ := os.ReadFile(conf); !bytes.Equal(again, written) {
		t.Errorf("a refused set changed the configuration:\n%s", again)
	}

	// daily, globally, must take weekly's line: added above it, as Salt
	// prepends a new global, it would lose to it.
	if _, err := liveSet(t, false, conf, "daily", true, nil); err != nil {
		t.Fatal(err)
	}
	if got := patternLine(t, logrotateSays(t, conf), logA); !strings.Contains(got, "after 1 days") {
		t.Fatalf("after the global daily logrotate says about a.log: %s", got)
	}

	// A new global goes before the first directive, so it applies to
	// every stanza: a.log, which says nothing, now compresses, and b.log's
	// own nocompress still wins for b.
	if _, err := liveSet(t, false, conf, "compress", true, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logrotateForcedSays(t, conf), "compressing log with"); n != 1 {
		t.Fatalf("after the global compress logrotate compresses %d logs, want a.log alone", n)
	}

	// A directive in the included stanza: compress replaces nocompress
	// in place, which is the only way it takes effect.
	if changed, err := liveSet(t, false, conf, logB, "compress", true); err != nil || !changed {
		t.Fatalf("b compress: changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(incDir, "b"))
	if strings.Contains(string(b), "nocompress") || !strings.Contains(string(b), "  compress\n") {
		t.Errorf("b after compress:\n%s", b)
	}
	if n := strings.Count(logrotateForcedSays(t, conf), "compressing log with"); n != 2 {
		t.Errorf("logrotate compresses %d logs after both were set, want 2", n)
	}
	if changed, err := liveSet(t, false, conf, logB, "compress", true); err != nil || changed {
		t.Fatalf("second b compress: changed=%v err=%v", changed, err)
	}

	// Removing it in the stanza leaves the global to decide.
	if _, err := liveSet(t, false, conf, logB, "compress", false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(incDir, "b")); strings.Contains(string(b), "compress") {
		t.Errorf("b still names compress:\n%s", b)
	}

	// A stanza that does not exist yet is appended to the named file.
	logC := filepath.Join(dir, "c.log")
	if _, err := liveSet(t, false, conf, logC, "rotate", int64(5)); err != nil {
		t.Fatal(err)
	}
	if got := patternLine(t, logrotateSays(t, conf), logC); !strings.Contains(got, "(5 rotations)") {
		t.Fatalf("after the new stanza logrotate says about c.log: %s", got)
	}

	// show_conf and get agree with what was written.
	cfg, err := loadLogrotate(conf)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.get("rotate", ""); got != int64(3) {
		t.Errorf("get rotate = %#v", got)
	}
	if got := cfg.get(logC, "rotate"); got != int64(5) {
		t.Errorf("get c.log rotate = %#v", got)
	}
}
