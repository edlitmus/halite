//go:build unix

package builtin

import (
	"fmt"
	"os"
	osexec "os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// `cron.present` and `cron.absent` writing another account's crontab, as
// root, through the `user:` argument -- which is what a node does and what
// the conformance case, which manages root's own crontab, cannot reach.
//
// # What it establishes
//
// For a throwaway account: `cron.present` adds a job to that account's
// crontab and converges on a second run; the system's own `crontab -l`,
// run here directly rather than through the module, shows the job and
// its identifier in the account's crontab and not in root's; and root's
// crontab is byte-for-byte what it was before. Then `cron.absent` removes
// the job and converges, and root's crontab is still untouched.
//
// It also drives the failure the module used to get wrong: `cron.present`
// for an account that does not exist must fail, and must not touch root's
// crontab. Until the retry without -u was confined to the caller's own
// account, a failed `crontab -u <account>` was retried as plain `crontab`,
// which is root's (see crontabIsTheCallers in cron.go).
//
// # What it does not assert
//
// The module writes through crontab(1) and never touches the spool
// directly, so the spool file's owner and mode are crontab(1)'s business,
// and they differ by platform. They are logged, not asserted -- an
// assertion here would be written from documentation, and the log is what
// a ledger entry can quote. Nothing here waits for the cron daemon to
// fire the job; the schedule is far enough out that it will not.
//
// # What it touches
//
// An account `halcr<pid>` with its home under /tmp, and that account's
// crontab, both removed in cleanups. Root's crontab is read, never
// written. Root and `HALITE_SYSTEM_LIVE=1`.
func TestLiveCronForAnotherAccount(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if runtime.GOOS == "windows" {
		t.Skip("cron is a unix module")
	}
	if os.Geteuid() != 0 {
		t.Skip("writing another account's crontab needs root; run it under sudo")
	}
	if _, err := osexec.LookPath("crontab"); err != nil {
		t.Skip("no crontab(1) on this machine")
	}
	c := realCtx(t)
	r := New()

	// crontabL is the system's own view, run directly so that the module
	// is not checking itself. "no crontab" is an empty crontab, as the
	// module treats it; anything else that fails is the test's failure.
	crontabL := func(t *testing.T, account string) string {
		t.Helper()
		out, err := osexec.Command("crontab", "-u", account, "-l").CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "no crontab") {
				return ""
			}
			t.Fatalf("crontab -u %s -l: %v: %s", account, err, out)
		}
		return string(out)
	}

	account := fmt.Sprintf("halcr%d", os.Getpid())
	home := filepath.Join(os.TempDir(), account)
	if _, err := user.Lookup(account); err == nil {
		t.Fatalf("%s already exists on this machine; refusing to touch it", account)
	}
	rootBefore := crontabL(t, "root")

	t.Cleanup(func() {
		if _, err := r.States.Call(c, "user.absent", value.MapOf("name", account, "purge", true)); err != nil {
			t.Errorf("removing %s: %v", account, err)
		}
		os.RemoveAll(home)
	})
	res, err := r.States.Call(c, "user.present", value.MapOf("name", account, "home", home,
		"createhome", true, "shell", "/bin/sh", "fullname", "halite cron test"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("user.present: %v %+v", err, res)
	}
	if _, err := user.Lookup(account); err != nil {
		t.Fatal(err)
	}
	// Registered after the account's, so it runs first: the crontab goes
	// while its account still exists for crontab(1) to name. Whether
	// removing an account takes its crontab with it differs by platform,
	// and this does not want to find out by leaving one behind.
	t.Cleanup(func() {
		_ = osexec.Command("crontab", "-u", account, "-r").Run()
	})

	const (
		command    = "/usr/bin/true # halite cron account test"
		identifier = "halite-live-cron-account"
	)
	jobArgs := func() *value.Map {
		return value.MapOf("name", command, "user", account, "identifier", identifier,
			"minute", "17", "hour", "4", "daymonth", "29", "month", "2")
	}

	apply := func(t *testing.T, fn string, args *value.Map, wantChange bool) {
		t.Helper()
		res, err := r.States.Call(c, fn, args)
		if err != nil || !res.Succeeded() {
			t.Fatalf("%s: %v %+v", fn, err, res)
		}
		if changed := res.Changes != nil && res.Changes.Len() > 0; changed != wantChange {
			t.Errorf("%s changed = %v, want %v: %s", fn, changed, wantChange, res.Comment)
		}
	}
	rootUnchanged := func(t *testing.T, after string) {
		t.Helper()
		if got := crontabL(t, "root"); got != rootBefore {
			t.Errorf("root's crontab changed %s:\nbefore:\n%s\nafter:\n%s", after, rootBefore, got)
		}
	}

	apply(t, "cron.present", jobArgs(), true)
	apply(t, "cron.present", jobArgs(), false)

	tab := crontabL(t, account)
	if !strings.Contains(tab, "17 4 29 2 * "+command) {
		t.Errorf("%s's crontab does not hold the job:\n%s", account, tab)
	}
	if !strings.Contains(tab, identifierPrefix+" "+identifier) {
		t.Errorf("%s's crontab does not hold the identifier:\n%s", account, tab)
	}
	if strings.Contains(crontabL(t, "root"), identifier) {
		t.Errorf("the job meant for %s is in root's crontab", account)
	}
	rootUnchanged(t, "after cron.present for "+account)
	logSpool(t, account)

	absentArgs := value.MapOf("name", command, "user", account, "identifier", identifier)
	apply(t, "cron.absent", absentArgs, true)
	apply(t, "cron.absent", absentArgs, false)
	if tab := crontabL(t, account); strings.Contains(tab, identifier) || strings.Contains(tab, command) {
		t.Errorf("cron.absent left the job in %s's crontab:\n%s", account, tab)
	}
	rootUnchanged(t, "after cron.absent for "+account)

	// An account that does not exist: crontab(1) refuses -u for it, and the
	// state must fail rather than fall back to root's crontab. The name is
	// checked absent first, since the test is meaningless if it is not.
	ghost := fmt.Sprintf("halcrx%d", os.Getpid())
	if _, err := user.Lookup(ghost); err == nil {
		t.Fatalf("%s exists on this machine; it is meant not to", ghost)
	}
	out, _ := osexec.Command("crontab", "-u", ghost, "-l").CombinedOutput()
	t.Logf("crontab -u %s -l on this machine says: %s", ghost, strings.TrimSpace(string(out)))
	res, err = r.States.Call(c, "cron.present", value.MapOf("name", command, "user", ghost,
		"identifier", identifier, "minute", "17", "hour", "4", "daymonth", "29", "month", "2"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Errorf("cron.present succeeded for %s, an account that does not exist: %s", ghost, res.Comment)
	} else {
		t.Logf("cron.present for %s failed, as it must: %s", ghost, res.Comment)
	}
	rootUnchanged(t, "after cron.present for an account that does not exist")
}

// logSpool prints the owner, group and mode of whichever spool file holds
// the account's crontab, for the record. The candidates are the
// directories crontab(1) is known to write on the platforms the fleet legs
// run; none existing is logged too, since that is a fact worth knowing.
func logSpool(t *testing.T, account string) {
	t.Helper()
	dirs := []string{
		"/var/spool/cron/crontabs", // Debian and Ubuntu
		"/var/spool/cron",          // cronie (EL, SUSE)
		"/var/cron/tabs",           // FreeBSD
		"/usr/lib/cron/tabs",       // macOS
		"/var/at/tabs",             // macOS, the directory /usr/lib/cron links to
	}
	seen := false
	for _, dir := range dirs {
		path := filepath.Join(dir, account)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		seen = true
		owner := "?"
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf("uid %d gid %d", st.Uid, st.Gid)
		}
		t.Logf("spool file %s: %s, mode %o", path, owner, info.Mode().Perm())
	}
	if !seen {
		t.Logf("no spool file for %s in any of %v", account, dirs)
	}
}
