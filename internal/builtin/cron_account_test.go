package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A failed `crontab -u <account>` must not be answered from, or written
// to, the caller's own crontab. Before this was fixed, both readCrontab and
// writeCrontab retried without -u on any failure but "no crontab", and
// `crontab` without -u is the *caller's* crontab -- root's, on a node. So
// `cron.present` for an account whose crontab could not be read (an unknown
// account, one listed in cron.deny) read root's entries, added the job, and,
// when the write for the account failed in turn, installed the lot as root's
// crontab: a job meant to run as somebody else ran as root, and whatever
// root's crontab had held was replaced by a copy of itself plus that job.
//
// The stderr below is deliberately not any cron's real message. The
// behaviour under test is that *every* failure other than "no crontab"
// stops, so the wording must not matter; a captured message would suggest
// that it did.
func TestCronForAnotherAccountNeverFallsBackToTheCallers(t *testing.T) {
	const account = "halite-no-such-account"
	refused := exec.Result{Code: 1, Stderr: "crontab: refused for this test\n"}
	rootTab := exec.Result{Stdout: "0 0 * * * /root/only\n"}

	t.Run("read fails", func(t *testing.T) {
		r := New()
		rec := &exec.RecordingRunner{
			Responses: map[string]exec.Result{"crontab -u " + account + " -l": refused},
			// Plain `crontab -l`, root's crontab, would succeed.
			Default: rootTab,
		}
		c := newCtx(false)
		c.Runner = rec
		res, err := r.States.Call(c, "cron.present", value.MapOf(
			"name", "/usr/bin/true", "user", account, "minute", "17", "hour", "4"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Errorf("cron.present succeeded for an account whose crontab could not be read: %+v", res)
		}
		assertNoPlainCrontab(t, rec)
	})

	t.Run("write fails", func(t *testing.T) {
		r := New()
		rec := &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				"crontab -u " + account + " -l": {Stdout: "MAILTO=ops\n"},
				"crontab -u " + account + " -":  refused,
			},
		}
		c := newCtx(false)
		c.Runner = rec
		res, err := r.States.Call(c, "cron.present", value.MapOf(
			"name", "/usr/bin/true", "user", account, "minute", "17", "hour", "4"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Errorf("cron.present succeeded though the account's crontab was not written: %+v", res)
		}
		assertNoPlainCrontab(t, rec)
	})

	t.Run("absent write fails", func(t *testing.T) {
		r := New()
		rec := &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				"crontab -u " + account + " -l": {Stdout: "17 4 * * * /usr/bin/true\n"},
				"crontab -u " + account + " -":  refused,
			},
		}
		c := newCtx(false)
		c.Runner = rec
		res, err := r.States.Call(c, "cron.absent", value.MapOf("name", "/usr/bin/true", "user", account))
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Errorf("cron.absent succeeded though the account's crontab was not written: %+v", res)
		}
		assertNoPlainCrontab(t, rec)
	})
}

func assertNoPlainCrontab(t *testing.T, rec *exec.RecordingRunner) {
	t.Helper()
	for _, cmd := range rec.RanCommands() {
		if strings.HasPrefix(cmd, "crontab") && !strings.HasPrefix(cmd, "crontab -u ") {
			t.Errorf("ran %q, which acts on the caller's own crontab, not the account's", cmd)
		}
	}
}
