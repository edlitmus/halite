package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The fixtures under testdata/logrotate were copied off real hosts on
// 2026-09-30: debian13/ is Debian 13's /etc/logrotate.conf and
// /etc/logrotate.d as installed (logrotate 3.22.0-1), byte for byte;
// freebsd15/ is what the sysutils/logrotate 3.22.0 port installs on
// FreeBSD 15.1. The debug-*.txt files are `logrotate -d -s <state>`
// output for configurations made to provoke each diagnostic, on the
// Debian host.

const lrFixtures = "testdata/logrotate"

// debianLogrotate copies the captured Debian configuration into a
// directory of its own, pointing its include at the copied drop-ins.
func debianLogrotate(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	incDir := filepath.Join(dir, "logrotate.d")
	if err := os.Mkdir(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(lrFixtures, "debian13", "logrotate.d"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(lrFixtures, "debian13", "logrotate.d", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(incDir, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	main, err := os.ReadFile(filepath.Join(lrFixtures, "debian13", "logrotate.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(main), "include /etc/logrotate.d", "include "+incDir, 1)
	conf := filepath.Join(dir, "logrotate.conf")
	if err := os.WriteFile(conf, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return conf, incDir
}

func TestDebiansLogrotateConfigurationReadsAsLogrotateReadsIt(t *testing.T) {
	conf, _ := debianLogrotate(t)
	cfg, err := loadLogrotate(conf)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.included) != 10 {
		t.Fatalf("read %d included files, want the 10 Debian ships", len(cfg.included))
	}
	view := cfg.showConf()
	for key, want := range map[string]any{"weekly": true, "rotate": int64(4), "create": true} {
		if got, _ := view.Get(key); got != want {
			t.Errorf("global %s = %#v, want %#v", key, got, want)
		}
	}
	if _, ok := view.Get("compress"); ok {
		t.Error("compress is commented out in Debian's file and was reported as set")
	}
	// unattended-upgrades names three logs over three lines, the first with
	// a trailing space, and puts the { on a fourth.
	for _, name := range []string{
		"/var/log/unattended-upgrades/unattended-upgrades.log",
		"/var/log/unattended-upgrades/unattended-upgrades-dpkg.log",
		"/var/log/unattended-upgrades/unattended-upgrades-shutdown.log",
	} {
		if got := cfg.get(name, "rotate"); got != int64(6) {
			t.Errorf("%s rotate = %#v, want 6", name, got)
		}
	}
	// ufw's { is on the line after its name.
	if got := cfg.get("/var/log/ufw.log", "delaycompress"); got != true {
		t.Errorf("ufw delaycompress = %#v", got)
	}
	// cloud-init's two names share one line.
	if got := cfg.get("/var/log/cloud-init-output.log", "size"); got != "1M" {
		t.Errorf("cloud-init-output size = %#v", got)
	}
	if got := cfg.get("/var/log/cloud-init.log", "create"); got != "644 root adm" {
		t.Errorf("cloud-init create = %#v", got)
	}
	// chrony's glob header and its script, kept opaque.
	script := cfg.get("/var/log/chrony/*.log", "postrotate")
	lines, ok := script.([]any)
	if !ok || len(lines) != 1 || lines[0] != "/usr/bin/chronyc cyclelogs > /dev/null 2>&1 || true" {
		t.Errorf("chrony postrotate = %#v", script)
	}
	files, _ := view.Get("include files")
	fm, _ := files.(*value.Map)
	if fm == nil {
		t.Fatal("no include files")
	}
	apt, _ := fm.Get("apt")
	if l, _ := apt.([]any); len(l) != 2 {
		t.Errorf("include files[apt] = %#v, want its two stanzas", apt)
	}
}

func TestTheFreeBSDPortsSampleReads(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(lrFixtures, "freebsd15", "logrotate.conf.sample"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseLogrotate("logrotate.conf.sample", string(b))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &lrConfig{main: f}
	if got := cfg.get("compress", ""); got != true {
		t.Errorf("compress = %#v", got)
	}
	if got := cfg.get("/var/log/lastlog", "rotate"); got != int64(1) {
		t.Errorf("lastlog rotate = %#v", got)
	}
	b, err = os.ReadFile(filepath.Join(lrFixtures, "freebsd15", "syslog.sample"))
	if err != nil {
		t.Fatal(err)
	}
	f, err = parseLogrotate("syslog.sample", string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Stanzas) != 9 {
		t.Errorf("syslog.sample has %d stanzas, want 9", len(f.Stanzas))
	}
}

func TestTheGrammarFollowsLogrotateNotSalt(t *testing.T) {
	for name, text := range map[string]string{
		"text after a }":           "/var/log/a {\n    missingok\n} rotate 3\n",
		"a glob with no slash":     "*.log {\n    missingok\n}\n",
		"a relative name":          "1.log {\n    missingok\n}\n",
		"a } with nothing open":    "rotate 4\n}\n",
		"a whole stanza on a line": "/var/log/a { rotate 2 }\n",
		"names with no {":          "/var/log/a\n",
		"a script with no end":     "/var/log/a {\n    postrotate\n        true\n}\n",
	} {
		if _, err := parseLogrotate("t.conf", text); err == nil {
			t.Errorf("%s: parsed, and logrotate refuses it", name)
		}
	}
	// `#` is a comment only at the start of a line, and `=` separates.
	f, err := parseLogrotate("t.conf", "/var/log/a {\n    rotate 2 # two\n    maxage=7\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &lrConfig{main: f}
	if got := cfg.get("/var/log/a", "rotate"); got != "2 # two" {
		t.Errorf("rotate = %#v; logrotate reads the whole line as the count", got)
	}
	if got := cfg.get("/var/log/a", "maxage"); got != int64(7) {
		t.Errorf("maxage = %#v", got)
	}
	// A stanza left open at the end of the file is accepted, as logrotate
	// accepts it, and a directive after the { is the stanza's.
	f, err = parseLogrotate("t.conf", "/var/log/a { missingok\n    rotate 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := (&lrConfig{main: f}).get("/var/log/a", "missingok"); got != true {
		t.Errorf("missingok after the { = %#v", got)
	}
	if _, err := (&lrConfig{main: f}).plan("/var/log/a", "compress", true, true); err == nil {
		t.Error("added a directive to a stanza with no closing brace")
	}
}

func TestALaterOppositeOrCriterionWins(t *testing.T) {
	f, err := parseLogrotate("t.conf", "compress\nweekly\nnocompress\nsize 1M\n/var/log/a {\n    nocompress\n    compress\n    daily\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &lrConfig{main: f}
	for key, want := range map[string]any{"compress": false, "nocompress": true, "weekly": false, "size": "1M"} {
		if got := cfg.get(key, ""); got != want {
			t.Errorf("global %s = %#v, want %#v", key, got, want)
		}
	}
	if got := cfg.get("/var/log/a", "compress"); got != true {
		t.Errorf("stanza compress = %#v", got)
	}
	view := cfg.showConf()
	if view.Has("compress") || view.Has("weekly") {
		t.Errorf("show_conf reported overridden settings: %v", view.StringKeys())
	}
}

func TestValuesCompareAsLogrotateReadsThem(t *testing.T) {
	for _, c := range []struct {
		key  string
		a, b string
		same bool
	}{
		{"rotate", "010", "8", true},
		{"rotate", "0x10", "16", true},
		{"rotate", "010", "10", false},
		{"rotate", "+3", "3", true},
		{"size", "1k", "1024", true},
		{"size", "1K", "1024", true},
		{"size", "010k", "8192", true},
		{"size", "1M", "1048576", true},
		{"size", "1m", "1048576", false},
		{"create", "644 root root", "0644 root root", true},
		{"create", "00644 root adm", "0644 root adm", true},
		{"create", "644 root root", "0644 root adm", false},
		{"su", "root  adm", "root adm", true},
	} {
		got := lrCanon(c.key, lrFields(c.a)) == lrCanon(c.key, lrFields(c.b))
		if got != c.same {
			t.Errorf("%s %q vs %q: same=%v, want %v", c.key, c.a, c.b, got, c.same)
		}
	}
}

func planOrFail(t *testing.T, cfg *lrConfig, key string, val, setting any, given bool) lrPlan {
	t.Helper()
	p, err := cfg.plan(key, val, setting, given)
	if err != nil {
		t.Fatalf("plan %s %v %v: %v", key, val, setting, err)
	}
	return p
}

func TestSetEditsOneLineAndLeavesTheRestOfTheFile(t *testing.T) {
	conf, inc := debianLogrotate(t)
	cfg, err := loadLogrotate(conf)
	if err != nil {
		t.Fatal(err)
	}
	orig := strings.Join(cfg.main.Lines, "\n")

	p := planOrFail(t, cfg, "rotate", int64(7), nil, false)
	if want := strings.Replace(orig, "\nrotate 4\n", "\nrotate 7\n", 1); p.text != want || p.old != int64(4) {
		t.Errorf("rotate 7:\n%s\nold %#v", p.text, p.old)
	}
	// daily overrides weekly, so it takes weekly's line rather than
	// landing above it, where weekly would go on winning.
	p = planOrFail(t, cfg, "daily", true, nil, false)
	if want := strings.Replace(orig, "\nweekly\n", "\ndaily\n", 1); p.text != want {
		t.Errorf("daily:\n%s", p.text)
	}
	// A new global goes before the first directive, as Salt prepends it.
	p = planOrFail(t, cfg, "compress", true, nil, false)
	if want := strings.Replace(orig, "\nweekly\n", "\ncompress\nweekly\n", 1); p.text != want {
		t.Errorf("compress:\n%s", p.text)
	}
	// Removing a global.
	p = planOrFail(t, cfg, "create", false, nil, false)
	if want := strings.Replace(orig, "\ncreate\n", "\n", 1); p.text != want {
		t.Errorf("create removed:\n%s", p.text)
	}
	// Already so, in another spelling.
	if p = planOrFail(t, cfg, "rotate", "04", nil, false); p.changed {
		t.Error("rotate 04 is rotate 4 and was planned as a change")
	}
	if p = planOrFail(t, cfg, "/var/log/cloud-init.log /var/log/cloud-init-output.log", "size", "1024k", true); p.changed {
		t.Error("size 1024k is size 1M and was planned as a change")
	}

	// A stanza in an included file, keeping its tab indent.
	dpkg := filepath.Join(inc, "dpkg")
	b, _ := os.ReadFile(dpkg)
	p = planOrFail(t, cfg, "/var/log/dpkg.log", "rotate", int64(6), true)
	if p.file.Path != dpkg {
		t.Fatalf("edited %s, want %s", p.file.Path, dpkg)
	}
	if want := strings.Replace(string(b), "\trotate 12\n", "\trotate 6\n", 1); p.text != want {
		t.Errorf("dpkg rotate 6:\n%s", p.text)
	}
	p = planOrFail(t, cfg, "/var/log/dpkg.log", "dateext", true, true)
	if want := strings.Replace(string(b), "\tcreate 644 root root\n}", "\tcreate 644 root root\n\tdateext\n}", 1); p.text != want {
		t.Errorf("dpkg dateext:\n%q", p.text)
	}
	// rotate 0 is written. Salt's `if setting:` deleted it.
	p = planOrFail(t, cfg, "/var/log/dpkg.log", "rotate", int64(0), true)
	if !strings.Contains(p.text, "\trotate 0\n") {
		t.Errorf("rotate 0:\n%s", p.text)
	}
	p = planOrFail(t, cfg, "/var/log/dpkg.log", "compress", false, true)
	if strings.Contains(p.text, "\tcompress\n") || !strings.Contains(p.text, "\tdelaycompress\n") {
		t.Errorf("compress removed:\n%s", p.text)
	}
}

func TestAStanzaWithSeveralNamesIsChangedOnlyByAllOfThem(t *testing.T) {
	conf, inc := debianLogrotate(t)
	cfg, err := loadLogrotate(conf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.plan("/var/log/cloud-init.log", "rotate", int64(9), true); err == nil ||
		!strings.Contains(err.Error(), "/var/log/cloud-init-output.log") {
		t.Fatalf("one name of two: %v", err)
	}
	p := planOrFail(t, cfg, "/var/log/cloud-init-output.log /var/log/cloud-init.log", "rotate", int64(9), true)
	b, _ := os.ReadFile(filepath.Join(inc, "cloud-init"))
	if want := strings.Replace(string(b), "  rotate 5\n", "  rotate 9\n", 1); p.text != want {
		t.Errorf("cloud-init rotate 9:\n%s", p.text)
	}
}

func TestANewStanzaIsAppendedToTheNamedFile(t *testing.T) {
	conf, _ := debianLogrotate(t)
	cfg, err := loadLogrotate(conf)
	if err != nil {
		t.Fatal(err)
	}
	p := planOrFail(t, cfg, "/var/log/halite.log", "rotate", int64(3), true)
	orig := strings.Join(cfg.main.Lines, "\n")
	if want := orig + "\n/var/log/halite.log {\n    rotate 3\n}\n"; p.text != want || p.file != cfg.main {
		t.Errorf("new stanza:\n%q", p.text)
	}
	f, err := parseLogrotate(conf, p.text)
	if err != nil {
		t.Fatal(err)
	}
	if got := (&lrConfig{main: f}).get("/var/log/halite.log", "rotate"); got != int64(3) {
		t.Errorf("read back %#v", got)
	}
	for _, bad := range []string{"halite.log", "*.log"} {
		if _, err := cfg.plan(bad, "rotate", int64(3), true); err == nil {
			t.Errorf("made a stanza for %q, which logrotate would not read as a file name", bad)
		}
	}
	if _, err := cfg.plan("rotate", int64(3), nil, false); err != nil {
		t.Errorf("global rotate: %v", err)
	}
	if _, err := cfg.plan("/var/log/dpkg.log", int64(3), nil, false); err == nil {
		t.Error("set a stanza's name as though it were a global")
	}
	if _, err := cfg.plan("/var/log/dpkg.log", "postrotate", "true", true); err == nil {
		t.Error("set a script through logrotate.set")
	}
	if _, err := cfg.plan("rotate", "3\n/etc/passwd {", nil, false); err == nil {
		t.Error("a value carrying a newline and a brace was accepted")
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(lrFixtures, "debian13", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDiagnosticsAreWhatLogrotateSaysAboutTheConfiguration(t *testing.T) {
	if d := logrotateDiagnostics(readFixture(t, "debug-clean.txt"), "", ""); len(d) != 0 {
		t.Errorf("clean: %q", d)
	}
	got := logrotateDiagnostics(readFixture(t, "debug-errors.txt"), "", "")
	want := []string{
		"warning: unknown option 'yes' -- ignoring line",
		"error: duplicate log entry for /tmp/halite-lr-capture/logs/a.log",
		"error: unexpected text after }",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("errors: %q\nwant %q (the stat of a missing log is not about the configuration)", got, want)
	}
	if got := logrotateDiagnostics(readFixture(t, "debug-global-error.txt"), "", ""); len(got) != 1 ||
		got[0] != "error: bad rotation count 'four'" {
		t.Errorf("global error: %q", got)
	}
	got = logrotateDiagnostics(readFixture(t, "debug-group-writable.txt"), "/tmp/halite-lr-capture/groupw.conf", "/etc/x.conf")
	if len(got) != 2 || !strings.Contains(got[1], "Ignoring /etc/x.conf because it is writable") {
		t.Errorf("group writable: %q", got)
	}
	if added := newDiagnostics([]string{"a", "b", "a"}, []string{"a", "a", "b", "a"}); len(added) != 1 || added[0] != "a" {
		t.Errorf("newDiagnostics counted %q", added)
	}
}

// fakeLogrotate answers `logrotate -d` with a captured output, chosen by
// whether it is being asked about the edited copy.
type fakeLogrotate struct {
	candidate string
	ran       []string
}

func (f *fakeLogrotate) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	file := cmd.Argv[len(cmd.Argv)-1]
	f.ran = append(f.ran, file)
	if strings.Contains(file, "halite-logrotate-") {
		return exec.Result{Code: 0, Stderr: strings.ReplaceAll(f.candidate, "/tmp/halite-lr-capture/broken.conf", file)}, nil
	}
	return exec.Result{Code: 1, Stderr: "error: stat of /var/log/x failed: No such file or directory\n"}, nil
}

func TestAWriteLogrotateRefusesIsNotMade(t *testing.T) {
	conf, _ := debianLogrotate(t)
	before, _ := os.ReadFile(conf)
	runner := &fakeLogrotate{candidate: readFixture(t, "debug-errors.txt")}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(string) string { return "/usr/sbin/logrotate" }
	_, err := logrotateSet(c, conf, "rotate", int64(5), nil, false)
	if err == nil || !strings.Contains(err.Error(), "unknown option 'yes'") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(conf); string(after) != string(before) {
		t.Error("the file was written although logrotate -d refused the edit")
	}

	// And with nothing new to say, it is written, keeping its mode.
	if err := os.Chmod(conf, 0o640); err != nil {
		t.Fatal(err)
	}
	runner.candidate = readFixture(t, "debug-clean.txt")
	if _, err := logrotateSet(c, conf, "rotate", int64(5), nil, false); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(conf)
	if !strings.Contains(string(after), "\nrotate 5\n") || strings.Contains(string(after), "\nrotate 4\n") {
		t.Errorf("not written:\n%s", after)
	}
	if fi, _ := os.Stat(conf); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode %o after the write, want 0640", fi.Mode().Perm())
	}
}

func TestTestModePredictsAndWritesNothing(t *testing.T) {
	conf, _ := debianLogrotate(t)
	before, _ := os.ReadFile(conf)
	c := newCtx(true)
	args := value.MapOf("name", "x", "key", "rotate", "value", int64(9), "conf_file", conf)
	res, err := logrotateSetState(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil || !res.HasChanges() {
		t.Errorf("test mode: %+v", res)
	}
	if after, _ := os.ReadFile(conf); string(after) != string(before) {
		t.Error("test mode wrote the file")
	}
	if ran := c.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 0 {
		t.Errorf("test mode ran %q", ran)
	}
	args = value.MapOf("name", "x", "key", "rotate", "value", "4", "conf_file", conf)
	if res, _ = logrotateSetState(c, args); res.Result == nil || !*res.Result || res.HasChanges() {
		t.Errorf("already rotate 4: %+v", res)
	}
}

func TestTheDefaultConfigurationFollowsThePlatform(t *testing.T) {
	if got := logrotateDefaultConf("linux"); got != "/etc/logrotate.conf" {
		t.Error(got)
	}
	if got := logrotateDefaultConf("freebsd"); got != "/usr/local/etc/logrotate.conf" {
		t.Error(got)
	}
	if !logrotateTabooName("apt.dpkg-old") || !logrotateTabooName("x.rhn-cfg-tmp-1") || logrotateTabooName(".hidden") ||
		logrotateTabooName("x.conf") {
		t.Error("taboo list does not match what logrotate 3.22.0 skipped")
	}
}
