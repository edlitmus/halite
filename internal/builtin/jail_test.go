package builtin

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// # About the fixtures in this file
//
// `pf` shipped with a fixture written in its own module's spelling, so
// its idempotence test compared this build's text against this build's
// text and agreed while the module matched no rule at all on a real
// host. DIVERGENCE 5.31. That is the mistake this file is written to
// avoid twice.
//
// Two things are done differently because of it.
//
// The module reads `jls --libxo=json` rather than the table, so the
// surface where a spelling can be wrong is a structured document rather
// than a column layout. And **the envelope is checked against a real
// `jls`**: `TestJailReadsWhatARealJlsPrints` runs the actual command on
// FreeBSD and feeds its output to the same parser, which CI does on
// every change. A host with no jails still proves the wrapper's shape,
// which is the part this build had to guess at.
//
// What remains invented, and is marked where it appears: the *field
// names inside* a jail entry — `name`, `path`, `host.hostname`,
// `osrelease`, `state`. Those need a host with a jail running, and this
// project's own fleet has FreeBSD hosts that could settle it. Until then
// the parser reads what it does not find as empty rather than failing,
// so a wrong guess costs a blank field and not a broken module.

// jailFixture gives a context whose jls and jail say what a test says.
func jailFixture(t *testing.T, responses map[string]hexec.Result) (*hexec.Context, *hexec.RecordingRunner) {
	t.Helper()
	if responses == nil {
		responses = map[string]hexec.Result{}
	}
	runner := &hexec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "jls" || name == "jail" {
			return "/usr/sbin/" + name
		}
		return ""
	}
	return c, runner
}

const jlsCmd = "jls --libxo=json -v"

// jlsJSON is libxo's envelope as this build expects it. **Invented**:
// the container names are what libxo's convention produces and no real
// `jls` has confirmed them here. The parser falls back to finding the
// array by shape for exactly that reason, and the test below checks the
// real thing.
const jlsJSON = `{
  "__version": "1",
  "jail-information": {
    "jail": [
      {"jid": 1, "name": "mail", "path": "/jails/mail", "host.hostname": "mail.example",
       "state": "ACTIVE", "osrelease": "14.1-RELEASE"},
      {"jid": 2, "name": "web", "path": "/jails/web", "host.hostname": "web.example",
       "state": "ACTIVE", "osrelease": "13.3-RELEASE"}
    ]
  }
}`

// The envelope is read, and every field with it.
func TestJailReadsTheLibxoEnvelope(t *testing.T) {
	got, err := parseJls(jlsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d jails, want 2: %v", len(got), got)
	}
	mail := got["mail"]
	if mail.JID != 1 || mail.Path != "/jails/mail" || mail.Hostname != "mail.example" {
		t.Errorf("mail read as %+v", mail)
	}
	// The userland version is the reason to look at a jail at all: it
	// can be older than the host's.
	if got["web"].OSRelease != "13.3-RELEASE" {
		t.Errorf("web's osrelease read as %q", got["web"].OSRelease)
	}
}

// The array is found by shape when the container is not called what this
// build expects.
//
// This is the fallback that exists because the container name is a
// guess. If libxo ever spells it differently — or `jls` wraps it in
// something else — the module keeps working and the FreeBSD test below
// is what would report the difference.
func TestJailFindsTheJailsWhateverTheContainerIsCalled(t *testing.T) {
	for _, tc := range []struct{ what, doc string }{
		{"a differently named container",
			`{"jls": {"jail": [{"jid": 3, "name": "db", "path": "/jails/db"}]}}`},
		{"no container at all",
			`{"jail": [{"jid": 3, "name": "db", "path": "/jails/db"}]}`},
	} {
		got, err := parseJls(tc.doc)
		if err != nil {
			t.Errorf("%s: %v", tc.what, err)
			continue
		}
		if len(got) != 1 || got["db"].JID != 3 {
			t.Errorf("%s: read %v", tc.what, got)
		}
	}

	// And an array that is not jails is not mistaken for them. Every
	// entry must carry a jid, which is what makes a jail entry one.
	got, err := parseJls(`{"something": {"else": [{"name": "not-a-jail"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an array with no jids was read as %d jails: %v", len(got), got)
	}
}

// A host with no jails is an empty answer, not a failure.
//
// It is the ordinary case on almost every FreeBSD machine, and failing
// there would make `jail.list` fail on most of them.
func TestAHostWithNoJailsIsEmpty(t *testing.T) {
	for _, doc := range []string{
		"",
		"   \n",
		`{"__version": "1", "jail-information": {}}`,
		`{"__version": "1", "jail-information": {"jail": []}}`,
	} {
		got, err := parseJls(doc)
		if err != nil {
			t.Errorf("%q was an error: %v", doc, err)
			continue
		}
		if len(got) != 0 {
			t.Errorf("%q read %d jails", doc, len(got))
		}
	}
}

// A jid written as a string is the same jid.
//
// libxo emits numbers as numbers, and some versions and some encoders
// quote every value. A module that read 0 for one of them would be wrong
// about which jail it was looking at.
func TestAJidIsAJidWhicheverWayItIsWritten(t *testing.T) {
	quoted, err := parseJls(`{"jail-information": {"jail": [{"jid": "7", "name": "q"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if quoted["q"].JID != 7 {
		t.Errorf("a quoted jid read as %d", quoted["q"].JID)
	}
}

// A jail with no name is kept, keyed by its jid.
//
// `jail -c` without a name produces one, and dropping it would make
// `jail.list` disagree with `jls` about how many jails are running —
// which is the one thing this function is for.
func TestAnUnnamedJailIsKeptByItsJid(t *testing.T) {
	got, err := parseJls(`{"jail-information": {"jail": [{"jid": 9, "path": "/jails/nine"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("an unnamed jail was dropped: %v", got)
	}
	if got["9"].Path != "/jails/nine" {
		t.Errorf("it is not keyed by its jid: %v", got)
	}
}

// Output that is not JSON says what was asked for.
//
// A `jls` without libxo support prints its table and this build has to
// say why that is not readable, rather than reporting no jails on a host
// that has them.
func TestJlsOutputThatIsNotJSONIsRefusedWithTheReason(t *testing.T) {
	_, err := parseJls("   JID  IP Address      Hostname          Path\n     1  -               mail.example      /jails/mail\n")
	if err == nil {
		t.Fatal("a jls table was read as JSON")
	}
	if !strings.Contains(err.Error(), "libxo") {
		t.Errorf("the error does not say what was asked for: %v", err)
	}
}

// The state starts a jail that is defined and not running.
func TestTheJailStateStartsWhatIsDefinedAndNotRunning(t *testing.T) {
	c, runner := jailFixture(t, map[string]hexec.Result{
		jlsCmd:         {Stdout: `{"jail-information": {"jail": []}}`},
		"jail -e ,":    {Stdout: "mail,web\n"},
		"jail -c mail": {},
	})
	res, err := jailRunningState(c, value.MapOf("name", "mail"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("a defined jail that is not running reported no change: %+v", res)
	}
	if !hasRun(runner, "jail -c mail") {
		t.Errorf("it was not started: %v", runner.RanCommands())
	}

	// One already running changes nothing and starts nothing.
	c, runner = jailFixture(t, map[string]hexec.Result{jlsCmd: {Stdout: jlsJSON}})
	res, err = jailRunningState(c, value.MapOf("name", "mail"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a running jail reported a change: %+v", res.Changes)
	}
	if hasRun(runner, "jail -c mail") {
		t.Errorf("a running jail was started again: %v", runner.RanCommands())
	}
}

// A jail nothing defines is refused by name, with what is defined.
//
// `jail -c` on an undefined name fails with its own wording; the fix is
// a file, and naming the file and what is in it is what an operator
// needs rather than the tool's error.
func TestStartingAJailNothingDefinesSaysSo(t *testing.T) {
	c, runner := jailFixture(t, map[string]hexec.Result{
		jlsCmd:      {Stdout: `{"jail-information": {"jail": []}}`},
		"jail -e ,": {Stdout: "mail,web\n"},
	})
	res, err := jailRunningState(c, value.MapOf("name", "database"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a jail nothing defines was reported as started")
	}
	for _, want := range []string{"jail.conf", "file.managed", "mail", "web"} {
		if !strings.Contains(res.Comment, want) {
			t.Errorf("the refusal does not mention %q: %s", want, res.Comment)
		}
	}
	if hasRun(runner, "jail -c database") {
		t.Errorf("it was started anyway: %v", runner.RanCommands())
	}
}

// Stopping is the mirror, and stopping one that is not running changes
// nothing.
func TestTheJailStateStopsAndIsIdempotent(t *testing.T) {
	c, runner := jailFixture(t, map[string]hexec.Result{
		jlsCmd:         {Stdout: jlsJSON},
		"jail -r mail": {},
	})
	res, err := jailRunningState(c, value.MapOf("name", "mail", "running", false))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("stopping a running jail reported no change: %+v", res)
	}
	if !hasRun(runner, "jail -r mail") {
		t.Errorf("it was not stopped: %v", runner.RanCommands())
	}

	c, runner = jailFixture(t, map[string]hexec.Result{
		jlsCmd: {Stdout: `{"jail-information": {"jail": []}}`},
	})
	res, err = jailRunningState(c, value.MapOf("name", "mail", "running", false))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("stopping a jail that is not running reported a change: %+v", res.Changes)
	}
	if len(runner.Ran) != 1 {
		t.Errorf("it ran %v", runner.RanCommands())
	}
}

// Test mode predicts and runs nothing.
func TestTheJailStateInTestModeRunsNothing(t *testing.T) {
	c, runner := jailFixture(t, map[string]hexec.Result{
		jlsCmd:      {Stdout: `{"jail-information": {"jail": []}}`},
		"jail -e ,": {Stdout: "mail\n"},
	})
	c.Test = true
	res, err := jailRunningState(c, value.MapOf("name", "mail"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Error("test mode predicted no change where there is one")
	}
	for _, ran := range runner.RanCommands() {
		if strings.HasPrefix(ran, "jail -c") || strings.HasPrefix(ran, "jail -r") {
			t.Errorf("test mode ran %q", ran)
		}
	}
}

// The configured list comes from `jail -e` rather than from parsing
// jail.conf here.
//
// jail.conf has includes, variables and inheritance, and a second parser
// for it in this module would disagree with the real one eventually.
func TestTheConfiguredJailsComeFromJailItself(t *testing.T) {
	c, runner := jailFixture(t, map[string]hexec.Result{
		"jail -e ,": {Stdout: "web,mail,db\n"},
	})
	got, err := jailConfigured(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "db,mail,web" {
		t.Errorf("read %v, want them sorted", got)
	}
	if !hasRun(runner, "jail -e ,") {
		t.Errorf("ran %v", runner.RanCommands())
	}

	// A named configuration is passed through.
	c, runner = jailFixture(t, map[string]hexec.Result{
		"jail -f /etc/jail.conf.d/mail.conf -e ,": {Stdout: "mail\n"},
	})
	if _, err := jailConfigured(c, "/etc/jail.conf.d/mail.conf"); err != nil {
		t.Fatal(err)
	}
	if !hasRun(runner, "jail -f /etc/jail.conf.d/mail.conf -e ,") {
		t.Errorf("ran %v", runner.RanCommands())
	}
}

// Off FreeBSD the registry refuses by name, both sides.
func TestJailRefusesOffFreeBSD(t *testing.T) {
	if runtime.GOOS == "freebsd" {
		t.Skip("this is the refusal, and it does not happen on freebsd")
	}
	r := New()
	if _, err := r.Exec.Call(newCtx(false), "jail.list", value.NewMap(0)); err == nil {
		t.Error("jail.list was accepted off freebsd")
	} else if !strings.Contains(err.Error(), runtime.GOOS) || !strings.Contains(err.Error(), "freebsd") {
		t.Errorf("the refusal does not name both sides: %v", err)
	}
}

// The envelope is what a real jls prints.
//
// **This is the test that pf did not have.** Everything above feeds the
// parser a document this build wrote, which proves the parser reads its
// own spelling — exactly the trap `pf`'s idempotence test fell into. This
// one runs the actual command and feeds the parser what came back, so the
// container names and the shape of the envelope are checked against the
// program rather than against an assumption.
//
// A host with no jails still settles it: the wrapper is there whether or
// not the array has anything in it, and the wrapper is the part this
// build had to guess. What it cannot settle is the field names *inside* a
// jail entry, which need a jail running; those are still assumed, and the
// parser reads what it does not find as empty so a wrong guess costs a
// blank field rather than a broken module.
//
// Runs on FreeBSD, which CI has on every change.
func TestJailReadsWhatARealJlsPrints(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("this checks the parser against the real jls, which is FreeBSD's")
	}
	path, err := exec.LookPath("jls")
	if err != nil {
		t.Skip("no jls on this host")
	}
	out, err := exec.CommandContext(context.Background(), path, "--libxo=json", "-v").Output()
	if err != nil {
		// A jls too old for libxo, or one that refuses without
		// privilege. Either is a fact about the host rather than a
		// failure of the parser, and skipping says which.
		t.Skipf("jls --libxo=json did not run here: %v", err)
	}
	t.Logf("jls returned %d bytes", len(out))

	// It is JSON at all, which the table is not.
	var probe map[string]any
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("jls --libxo=json did not print JSON: %v\n%s", err, out)
	}
	// The container this build looks for first is the one that is there.
	if _, ok := probe["jail-information"]; !ok {
		t.Errorf("the real jls wraps its output in %v, and this build looks for "+
			"`jail-information` first. It falls back to finding the array by shape, so "+
			"the module still works — but internal/builtin/jail.go's comment and "+
			"DIVERGENCE should say the real name.", keysOf(probe))
	}

	jails, err := parseJls(string(out))
	if err != nil {
		t.Fatalf("the parser could not read what the real jls printed: %v\n%s", err, out)
	}
	t.Logf("parsed %d jails from a real jls", len(jails))
	for name, j := range jails {
		if j.JID == 0 {
			t.Errorf("%s parsed with no jid, so the field name is wrong: %+v", name, j)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hasRun(r *hexec.RecordingRunner, want string) bool {
	for _, ran := range r.RanCommands() {
		if ran == want {
			return true
		}
	}
	return false
}
