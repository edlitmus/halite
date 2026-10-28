package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// saltCrontab is `crontab -l` after Salt 3008.3's cron.present and
// cron.env_present wrote it (3006.28 wrote the same bytes), captured on
// 2026-10-27.
func saltCrontab(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cron", "salt-3008.3.crontab"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The jobs Salt wrote are read as managed entries with Salt's
// identifiers and comments, not as lines halite did not write.
func TestASaltCrontabIsReadAsManaged(t *testing.T) {
	byID := map[string]cronEntry{}
	for _, e := range parseCrontab(saltCrontab(t)) {
		if e.Identifier != "" {
			byID[e.Identifier] = e
		}
	}
	for id, want := range map[string]cronEntry{
		"myjob":             {Spec: "5 * * * *", Command: "/usr/bin/true one", Comment: "first job"},
		"/usr/bin/true two": {Spec: "10 * * * *", Command: "/usr/bin/true two"},
		"boot":              {Spec: "@reboot", Command: "/usr/bin/true three"},
	} {
		got, ok := byID[id]
		if !ok {
			t.Errorf("no entry for %q among %v", id, byID)
			continue
		}
		if got.Spec != want.Spec || got.Command != want.Command || got.Comment != want.Comment {
			t.Errorf("%q = %+v, want %+v", id, got, want)
		}
	}
}

// The point of adopting: the same jobs declared in a tree converge on
// what Salt wrote, instead of each being added a second time.
func TestTheSameJobsDeclaredInATreeChangeNothing(t *testing.T) {
	r := New()
	for _, args := range []*value.Map{
		value.MapOf("name", "/usr/bin/true one", "minute", "5", "identifier", "myjob", "comment", "first job"),
		value.MapOf("name", "/usr/bin/true two", "minute", "10"),
		value.MapOf("name", "/usr/bin/true three", "special", "@reboot", "identifier", "boot"),
	} {
		rec := &exec.RecordingRunner{Responses: map[string]exec.Result{
			"crontab -u root -l": {Stdout: saltCrontab(t)},
		}}
		c := newCtx(false)
		c.Runner = rec
		res, err := r.States.Call(c, "cron.present", args)
		if err != nil {
			t.Fatal(err)
		}
		if res.HasChanges() || !res.Succeeded() {
			t.Errorf("%v against Salt's crontab: %+v", args, res)
		}
		for _, cmd := range rec.Ran {
			if strings.HasSuffix(cmd.String(), " -") {
				t.Errorf("%v rewrote the crontab:\n%s", args, cmd.Stdin)
			}
		}
	}
}

// A change rewrites the adopted entries in halite's spelling, once, and
// keeps the rest of the file.
func TestAChangeRewritesSaltsEntriesInHalitesSpelling(t *testing.T) {
	rec := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"crontab -u root -l": {Stdout: saltCrontab(t)},
	}}
	c := newCtx(false)
	c.Runner = rec
	res, err := New().States.Call(c, "cron.present", value.MapOf(
		"name", "/usr/bin/true one", "minute", "6", "identifier", "myjob", "comment", "first job"))
	if err != nil || !res.HasChanges() {
		t.Fatalf("result = %+v, %v", res, err)
	}
	var written string
	for _, cmd := range rec.Ran {
		if strings.HasSuffix(cmd.String(), " -") {
			written = cmd.Stdin
		}
	}
	if strings.Count(written, "/usr/bin/true one") != 1 {
		t.Errorf("the job is not there exactly once:\n%s", written)
	}
	for _, want := range []string{"MAILTO=ops@example.org", identifierPrefix + " myjob", "6 * * * * /usr/bin/true one", identifierPrefix + " boot"} {
		if !strings.Contains(written, want) {
			t.Errorf("the rewrite has no %q:\n%s", want, written)
		}
	}
	if strings.Contains(written, saltIdentifier) {
		t.Errorf("Salt's identifier survived the rewrite:\n%s", written)
	}
}

// workstationCrontab is `crontab -l` for root on the Ubuntu workstation
// system76-pc, captured on 2026-10-27: a production crontab rather than
// a lab one, as Salt wrote it from edlitmus/salt's state/drake.sls (last
// written 2025-03-04; Salt has since been removed from the host, so the
// version that wrote it is not known).
func workstationCrontab(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "cron", "system76-pc-salt.crontab"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// drake.sls's job, with its arguments as written there, changes nothing.
//
// This half would pass without the adoption, and it is worth saying why
// so nobody reads it as proof of it: drake.sls names no identifier, so
// Salt used the command as one, and parseCrontab gives an untagged line
// its command as identifier too. The two agree whether or not Salt's
// comment is understood. The rewrite below is what the adoption decides.
func TestTheWorkstationsSaltJobChangesNothing(t *testing.T) {
	rec := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"crontab -u root -l": {Stdout: workstationCrontab(t)},
	}}
	c := newCtx(false)
	c.Runner = rec
	res, err := New().States.Call(c, "cron.present", value.MapOf(
		"name", "/root/service-restart.sh", "user", "root", "special", "@daily"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() || !res.Succeeded() {
		t.Errorf("drake.sls's job against the crontab Salt wrote: %+v", res)
	}
	for _, cmd := range rec.Ran {
		if strings.HasSuffix(cmd.String(), " -") {
			t.Errorf("the crontab was rewritten:\n%s", cmd.Stdin)
		}
	}
}

// Changing the job's schedule leaves one entry in halite's spelling, and
// none of Salt's lines behind as stale comments. halite writes no
// identifier line for a job whose identifier is its command, so the
// entry is the bare cron line.
func TestReschedulingTheWorkstationsJobLeavesNoSaltLines(t *testing.T) {
	rec := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"crontab -u root -l": {Stdout: workstationCrontab(t)},
	}}
	c := newCtx(false)
	c.Runner = rec
	res, err := New().States.Call(c, "cron.present", value.MapOf(
		"name", "/root/service-restart.sh", "user", "root", "special", "@weekly"))
	if err != nil || !res.HasChanges() {
		t.Fatalf("result = %+v, %v", res, err)
	}
	var written string
	for _, cmd := range rec.Ran {
		if strings.HasSuffix(cmd.String(), " -") {
			written = cmd.Stdin
		}
	}
	if strings.Count(written, "/root/service-restart.sh") != 1 || !strings.Contains(written, "@weekly /root/service-restart.sh\n") {
		t.Errorf("want the one @weekly entry:\n%s", written)
	}
	if strings.Contains(written, saltIdentifier) || strings.Contains(written, saltManagedMarker) {
		t.Errorf("Salt's lines survived the rewrite:\n%s", written)
	}
}
