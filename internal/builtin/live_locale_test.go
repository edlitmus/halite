//go:build linux

package builtin

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The locale module, driven against the real localectl, locale-gen and
// langpacks.
//
// Both tests change something system-wide -- the system LANG, Debian's
// /etc/locale.gen and compiled archive, an EL langpack -- so each records
// what it found and puts it back byte for byte, and the files are
// compared again after the restore rather than trusted to it. The
// session the test arrived over is not affected by a change to the
// system LANG: that is read at login, and nothing here logs in.

func localeLiveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this changes the system locale")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set and this is not root; set_locale and gen_locale need it")
	}
	for _, tool := range []string{"localectl", "locale"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("this host has no %s", tool)
		}
	}
	if err := exec.Command("localectl", "status").Run(); err != nil {
		t.Skipf("localectl is installed and cannot reach systemd-localed: %v", err)
	}
}

// keepFile records a file's bytes and mtime, or its absence, and puts
// them back at cleanup, failing the test if the restore did not take.
func keepFile(t *testing.T, path string) {
	t.Helper()
	before, err := os.ReadFile(path)
	absent := os.IsNotExist(err)
	if err != nil && !absent {
		t.Fatalf("reading %s: %v", path, err)
	}
	var mtime time.Time
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime()
	}
	t.Cleanup(func() {
		if absent {
			_ = os.Remove(path)
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("%s was absent and could not be removed again", path)
			}
			return
		}
		// Written in place, not renamed, so a symlink pointing at it
		// (Debian's /etc/default/locale) and its inode are kept.
		if err := os.WriteFile(path, before, 0o644); err != nil {
			t.Errorf("restoring %s: %v", path, err)
			return
		}
		_ = os.Chtimes(path, mtime, mtime)
		if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
			t.Errorf("%s did not come back byte for byte", path)
		}
	})
}

func localectlLang(t *testing.T) map[string]string {
	t.Helper()
	out, err := exec.Command("localectl", "status").Output()
	if err != nil {
		t.Fatalf("localectl status: %v", err)
	}
	return parseLocalectlStatus(string(out))
}

func TestLiveLocaleSetsTheSystemLocaleInEitherSpelling(t *testing.T) {
	localeLiveGate(t)
	// Registered before the restore, so it runs after it: cleanups run
	// last-first.
	original := localectlLang(t)
	t.Cleanup(func() {
		if got := localectlLang(t); got["LANG"] != original["LANG"] || len(got) != len(original) {
			t.Errorf("after the restore localectl reports %v, want %v", got, original)
		}
	})
	saved, err := os.ReadFile("/etc/locale.conf")
	if err != nil {
		t.Fatalf("reading /etc/locale.conf: %v", err)
	}
	t.Cleanup(func() {
		if err := restoreLocaleConf(&hexec.Context{Runner: &hexec.OSRunner{}}, saved); err != nil {
			t.Errorf("restoring /etc/locale.conf: %v", err)
		}
		if after, _ := os.ReadFile("/etc/locale.conf"); !bytes.Equal(after, saved) {
			t.Error("/etc/locale.conf did not come back byte for byte")
		}
	})

	r := New()
	c := &hexec.Context{Runner: &hexec.OSRunner{}}
	call := func(fn string, kv ...any) (any, error) {
		return r.Exec.Call(c, "locale."+fn, value.MapOf(kv...))
	}

	got, err := call("get_locale")
	if err != nil || got != original["LANG"] {
		t.Fatalf("get_locale = %v, %v; localectl says %q", got, err, original["LANG"])
	}
	for _, name := range []string{"en_GB.UTF-8", "en_GB.utf8", "C.UTF-8"} {
		if ok, err := call("avail", "locale", name); err != nil || ok != true {
			t.Errorf("avail(%s) = %v, %v; want true", name, ok, err)
		}
	}
	if ok, err := call("avail", "locale", "xx_YY.UTF-8"); err != nil || ok != false {
		t.Errorf("avail(xx_YY.UTF-8) = %v, %v; want false", ok, err)
	}

	// Set in glibc's spelling, then ask for the other spelling: the
	// second must find it already set and leave the file alone.
	if ok, err := call("set_locale", "locale", "en_GB.utf8"); err != nil || ok != true {
		t.Fatalf("set_locale(en_GB.utf8) = %v, %v", ok, err)
	}
	if got := localectlLang(t)["LANG"]; got != "en_GB.utf8" {
		t.Errorf("localectl reports LANG=%q, want the spelling it was given", got)
	}
	conf, _ := os.ReadFile("/etc/locale.conf")
	if ok, err := call("set_locale", "locale", "en_GB.UTF-8"); err != nil || ok != true {
		t.Errorf("set_locale(en_GB.UTF-8) over en_GB.utf8 = %v, %v", ok, err)
	}
	if again, _ := os.ReadFile("/etc/locale.conf"); !bytes.Equal(again, conf) {
		t.Errorf("the other spelling rewrote /etc/locale.conf:\n%s", again)
	}

	// LANG only: an LC_* variable set beside it survives. It must differ
	// from the new LANG: localed drops one that equals LANG as redundant
	// (measured on Debian 13, where LC_TIME=en_US.UTF-8 vanished under
	// LANG=en_US.UTF-8 and LC_MESSAGES=C.UTF-8 beside it did not).
	if out, err := exec.Command("localectl", "set-locale", "LANG=en_GB.UTF-8", "LC_TIME=C.UTF-8").CombinedOutput(); err != nil {
		t.Fatalf("localectl: %v: %s", err, out)
	}
	if ok, err := call("set_locale", "locale", "en_US.UTF-8"); err != nil || ok != true {
		t.Fatalf("set_locale(en_US.UTF-8) = %v, %v", ok, err)
	}
	if vars := localectlLang(t); vars["LANG"] != "en_US.UTF-8" || vars["LC_TIME"] != "C.UTF-8" {
		t.Errorf("after set_locale localectl reports %v; LC_TIME should have survived", vars)
	}

	// A locale the node cannot load is refused before localectl is asked.
	before, _ := os.ReadFile("/etc/locale.conf")
	if _, err := call("set_locale", "locale", "xx_YY.UTF-8"); err == nil {
		t.Error("set_locale(xx_YY.UTF-8) succeeded")
	}
	if after, _ := os.ReadFile("/etc/locale.conf"); !bytes.Equal(after, before) {
		t.Error("a refused set_locale changed /etc/locale.conf")
	}

	// The state: a change, then none, in either spelling, and test mode
	// changes nothing.
	for i, step := range []struct {
		name    string
		test    bool
		changes bool
	}{
		{"en_GB.UTF-8", true, true},
		{"en_GB.UTF-8", false, true},
		{"en_GB.UTF-8", false, false},
		{"en_GB.utf8", false, false},
		{"en_GB.UTF-8", true, false},
	} {
		cc := &hexec.Context{Runner: &hexec.OSRunner{}, Test: step.test}
		pre, _ := os.ReadFile("/etc/locale.conf")
		res, err := r.States.Call(cc, "locale.system", value.MapOf("name", step.name))
		if err != nil || !stateSucceeded(res) && !step.test {
			t.Fatalf("step %d: locale.system(%s) = %+v, %v", i, step.name, res, err)
		}
		has := res.Changes != nil && res.Changes.Len() > 0
		if has != step.changes {
			t.Errorf("step %d: locale.system(%s, test=%v) changes = %v, want %v: %s",
				i, step.name, step.test, has, step.changes, res.Comment)
		}
		if post, _ := os.ReadFile("/etc/locale.conf"); step.test && !bytes.Equal(pre, post) {
			t.Errorf("step %d: test mode changed /etc/locale.conf", i)
		}
	}
}

func TestLiveLocaleGeneratesALocale(t *testing.T) {
	localeLiveGate(t)
	r := New()
	c := &hexec.Context{Runner: &hexec.OSRunner{}}
	scheme := localeScheme(c)
	switch scheme {
	case "locale-gen":
		keepFile(t, localeGenPath)
		keepFile(t, "/usr/lib/locale/locale-archive")
	case "langpack":
		if err := exec.Command("rpm", "-q", "glibc-langpack-de").Run(); err == nil {
			t.Skip("glibc-langpack-de is already installed here, so there is nothing to generate and nothing this test may remove")
		}
		t.Cleanup(func() {
			if err := exec.Command("rpm", "-q", "glibc-langpack-de").Run(); err == nil {
				if out, err := exec.Command("rpm", "-e", "glibc-langpack-de").CombinedOutput(); err != nil {
					t.Errorf("removing glibc-langpack-de: %v: %s", err, out)
				}
			}
		})
	default:
		t.Skip("this node has neither locale-gen nor rpm langpacks")
	}

	const want = "de_DE.UTF-8"
	avail := func() bool {
		ok, err := localeAvail(c, want)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	wasAvail := avail()
	if scheme == "langpack" && wasAvail {
		t.Fatalf("%s is loadable with no glibc-langpack-de", want)
	}

	// Test mode first: nothing installed, nothing written.
	tc := &hexec.Context{Runner: &hexec.OSRunner{}, Test: true}
	if ok, err := r.Exec.Call(tc, "locale.gen_locale", value.MapOf("locale", want)); err != nil || ok != true {
		t.Errorf("test-mode gen_locale = %v, %v", ok, err)
	}
	if avail() != wasAvail {
		t.Error("test-mode gen_locale changed what is loadable")
	}
	if scheme == "locale-gen" {
		if b, _ := os.ReadFile(localeGenPath); strings.Contains(string(b), "\n"+want+" UTF-8\n") {
			t.Fatalf("%s is already enabled in %s; this test would prove nothing", want, localeGenPath)
		}
	}

	if ok, err := r.Exec.Call(c, "locale.gen_locale", value.MapOf("locale", "de_DE.utf8")); err != nil || ok != true {
		t.Fatalf("gen_locale(de_DE.utf8) = %v, %v", ok, err)
	}
	if !avail() {
		t.Fatalf("%s is not loadable after gen_locale", want)
	}
	switch scheme {
	case "locale-gen":
		b, _ := os.ReadFile(localeGenPath)
		if !strings.Contains(string(b), "\n"+want+" UTF-8\n") {
			t.Errorf("%s does not enable SUPPORTED's `%s UTF-8` line", localeGenPath, want)
		}
		if strings.Count(string(b), want+" UTF-8") != 1 {
			t.Errorf("%s names %s more than once", localeGenPath, want)
		}
	case "langpack":
		if err := exec.Command("rpm", "-q", "glibc-langpack-de").Run(); err != nil {
			t.Error("glibc-langpack-de is not installed after gen_locale")
		}
	}

	// Again, in the other spelling: nothing to do.
	before, _ := os.ReadFile(localeGenPath)
	if ok, err := r.Exec.Call(c, "locale.gen_locale", value.MapOf("locale", want)); err != nil || ok != true {
		t.Errorf("a second gen_locale = %v, %v", ok, err)
	}
	if after, _ := os.ReadFile(localeGenPath); !bytes.Equal(before, after) {
		t.Errorf("a second gen_locale rewrote %s", localeGenPath)
	}

	if ok, err := r.Exec.Call(c, "locale.gen_locale", value.MapOf("locale", "xx_YY.UTF-8")); err == nil && ok == true {
		t.Error("gen_locale(xx_YY.UTF-8) succeeded")
	}

	res, err := r.States.Call(c, "locale.present", value.MapOf("name", want))
	if err != nil || !stateSucceeded(res) || (res.Changes != nil && res.Changes.Len() > 0) {
		t.Errorf("locale.present on a present locale = %+v, %v", res, err)
	}
}
