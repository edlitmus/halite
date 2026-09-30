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

// Every fixture under testdata/dnf_module was captured as root on
// 2026-09-30 by one script run on each lab host -- rocky9 (Rocky Linux
// 9.8, dnf 4.14.0) and alma8 (AlmaLinux 8.10, dnf 4.7.0) -- that listed,
// enabled nginx:1.24, disabled and reset it, installed and removed
// redis's `common` profile (redis:7 on rocky9, redis:6 on alma8), and
// saved each command's stdout, stderr and exit status and each
// modules.d file it left behind, byte for byte. None is written by hand.

func dnfFixture(t *testing.T, host, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "dnf_module", host, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func dnfFindStream(t *testing.T, rows []dnfModuleStream, name, stream string) dnfModuleStream {
	t.Helper()
	for _, r := range rows {
		if r.Name == name && r.Stream == stream {
			return r
		}
	}
	t.Fatalf("no %s:%s among %d parsed rows", name, stream, len(rows))
	return dnfModuleStream{}
}

func TestDnfModuleParseListReadsTheWholeRealTableOnBothVersions(t *testing.T) {
	for _, tc := range []struct {
		host string
		rows int
		repo string
	}{
		// Counted from the fixtures: every line between the header and
		// the blank line above the hint.
		{"rocky9", 22, "Rocky Linux 9 - AppStream"},
		{"alma8", 99, "AlmaLinux 8 - AppStream"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			rows, err := dnfModuleParseList(dnfFixture(t, tc.host, "list-q.stdout"))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.rows {
				t.Fatalf("parsed %d rows, want %d", len(rows), tc.rows)
			}
			for _, r := range rows {
				if r.Repo != tc.repo {
					t.Errorf("%s:%s repo = %q, want %q", r.Name, r.Stream, r.Repo, tc.repo)
				}
				if r.Summary == "" {
					t.Errorf("%s:%s has no summary; a column offset is wrong", r.Name, r.Stream)
				}
			}
		})
	}
}

func TestDnfModuleParseListReadsMarkersAndEmptyCells(t *testing.T) {
	alma, err := dnfModuleParseList(dnfFixture(t, "alma8", "list-q.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	ct := dnfFindStream(t, alma, "container-tools", "rhel8")
	if !ct.Default || !ct.Enabled || ct.Disabled {
		t.Errorf("container-tools:rhel8 = %+v, want [d][e]", ct)
	}
	// 389-ds has an empty Profiles cell, so its summary is the third
	// word of the row: splitting on whitespace would call "389" a profile.
	ds := dnfFindStream(t, alma, "389-ds", "1.4")
	if len(ds.Profiles) != 0 || ds.Summary != "389 Directory Server (base)" {
		t.Errorf("389-ds:1.4 = %+v, want no profiles and the whole summary", ds)
	}
	// The widest Profiles cell in the table, filling its column exactly.
	idm := dnfFindStream(t, alma, "idm", "DL1")
	if strings.Join(idm.Profiles, ",") != "adtrust,client,common,dns,server" ||
		strings.Join(idm.DefaultProfiles, ",") != "common" || idm.Default {
		t.Errorf("idm:DL1 = %+v", idm)
	}
	if got := dnfFindStream(t, alma, "nodejs", "22"); len(got.DefaultProfiles) != 0 {
		t.Errorf("nodejs:22 has no [d] profile on alma8, parsed %v", got.DefaultProfiles)
	}

	rocky, err := dnfModuleParseList(dnfFixture(t, "rocky9", "list-q.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	// EL9 ships no default streams at all: not one [d] in the Stream column.
	for _, r := range rocky {
		if r.Default || r.Enabled || r.Disabled {
			t.Errorf("rocky9 %s:%s = %+v, want no stream markers on a fresh EL9", r.Name, r.Stream, r)
		}
	}
	maven := dnfFindStream(t, rocky, "maven", "3.9")
	if strings.Join(maven.Profiles, ",") != "common,openjdk11,openjdk17,openjdk21,openjdk25,openjdk8" {
		t.Errorf("maven:3.9 profiles = %v", maven.Profiles)
	}
}

func TestDnfModuleParseListReadsEachStateTheHostsWereDrivenThrough(t *testing.T) {
	for _, host := range []string{"rocky9", "alma8"} {
		t.Run(host, func(t *testing.T) {
			parse := func(name string) []dnfModuleStream {
				rows, err := dnfModuleParseList(dnfFixture(t, host, name))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return rows
			}
			if r := dnfFindStream(t, parse("list-nginx-enabled-q.stdout"), "nginx", "1.24"); !r.Enabled || r.Disabled {
				t.Errorf("after enable, nginx:1.24 = %+v", r)
			}
			for _, r := range parse("list-nginx-disabled-q.stdout") {
				if !r.Disabled || r.Enabled {
					t.Errorf("after disable, nginx:%s = %+v, want [x]", r.Stream, r)
				}
			}
			for _, r := range parse("list-nginx-reset-q.stdout") {
				if r.Enabled || r.Disabled {
					t.Errorf("after reset, nginx:%s = %+v, want neither [e] nor [x]", r.Stream, r)
				}
			}
			installed := parse("list-installed-after-q.stdout")
			if len(installed) != 1 {
				t.Fatalf("--installed after the redis install = %d rows, want 1", len(installed))
			}
			if r := installed[0]; r.Name != "redis" || !r.Enabled ||
				strings.Join(r.InstalledProfiles, ",") != "common" ||
				strings.Join(r.DefaultProfiles, ",") != "common" {
				t.Errorf("installed redis = %+v, want enabled with common [d] [i]", r)
			}
			if rows := parse("list-installed-q.stdout"); len(rows) != 0 {
				t.Errorf("--installed with nothing installed printed %d rows; the real output is empty", len(rows))
			}
		})
	}
	// dnf 4.7's [d][x]: a default stream that is also disabled.
	alma, _ := dnfModuleParseList(dnfFixture(t, "alma8", "list-nginx-disabled-q.stdout"))
	if r := dnfFindStream(t, alma, "nginx", "1.14"); !r.Default || !r.Disabled {
		t.Errorf("alma8 nginx:1.14 after disable = %+v, want [d][x]", r)
	}
}

func TestDnfModuleParseListRefusesARowThatDoesNotFitItsHeader(t *testing.T) {
	// The real rocky9 nginx table with the header shifted one column to
	// the right, which is what a table read with the wrong offsets looks
	// like: the name cell swallows the first character of the stream.
	shifted := strings.Replace(dnfFixture(t, "rocky9", "list-nginx-q.stdout"),
		"Name  Stream", "Name   Stream", 1)
	if _, err := dnfModuleParseList(shifted); err == nil {
		t.Fatal("a misaligned table parsed; it must be refused rather than shifted")
	}
	unknown := strings.Replace(dnfFixture(t, "rocky9", "list-nginx-enabled-q.stdout"), "1.24 [e]", "1.24 [q]", 1)
	if _, err := dnfModuleParseList(unknown); err == nil {
		t.Fatal("an unknown stream marker parsed silently")
	}
}

func TestDnfModuleParseStatusReadsTheRealFiles(t *testing.T) {
	for _, host := range []string{"rocky9", "alma8"} {
		enabled := dnfModuleParseStatus(dnfFixture(t, host, "nginx-enabled.module"))["nginx"]
		if enabled.State != "enabled" || enabled.Stream != "1.24" || len(enabled.Profiles) != 0 {
			t.Errorf("%s enabled = %+v", host, enabled)
		}
		disabled := dnfModuleParseStatus(dnfFixture(t, host, "nginx-disabled.module"))["nginx"]
		if disabled.State != "disabled" || disabled.Stream != "" {
			t.Errorf("%s disabled = %+v", host, disabled)
		}
		reset := dnfModuleParseStatus(dnfFixture(t, host, "nginx-reset.module"))["nginx"]
		if reset.State != "" {
			t.Errorf("%s reset = %+v, want an empty state", host, reset)
		}
		installed := dnfModuleParseStatus(dnfFixture(t, host, "redis-installed.module"))["redis"]
		if installed.State != "enabled" || strings.Join(installed.Profiles, ",") != "common" {
			t.Errorf("%s installed = %+v", host, installed)
		}
	}
}

// dnfModulesDirOf lays real captured modules.d files into a temporary
// directory, named as dnf names them, and points DnfModulesDir at it.
func dnfModulesDirOf(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := DnfModulesDir
	DnfModulesDir = dir
	t.Cleanup(func() { DnfModulesDir = old })
	return dir
}

func TestDnfModuleStatusTreatsAResetFileAsNoChoiceAtAll(t *testing.T) {
	dnfModulesDirOf(t, map[string]string{
		"nginx.module": dnfFixture(t, "alma8", "nginx-reset.module"),
		"redis.module": dnfFixture(t, "alma8", "redis-installed.module"),
	})
	out, err := dnfModuleStatusFn(&exec.Context{}, value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if _, ok := m.Get("nginx"); ok {
		t.Error("a reset module is listed; dnf reads `state=` as no choice, and so must status")
	}
	redis, ok := m.Get("redis")
	if !ok {
		t.Fatalf("redis missing from %v", m)
	}
	if st, _ := redis.(*value.Map).Get("state"); st != "enabled" {
		t.Errorf("redis state = %v", st)
	}
}

func TestDnfModuleStatusOfAMissingDirectoryIsEmpty(t *testing.T) {
	old := DnfModulesDir
	DnfModulesDir = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { DnfModulesDir = old })
	out, err := dnfModuleStatusFn(&exec.Context{}, value.NewMap(0))
	if err != nil || out.(*value.Map).Len() != 0 {
		t.Fatalf("status = %v, %v; want empty, no error", out, err)
	}
}

func TestDnfModuleRefusesSpecsThatDnfWouldReadAsOptions(t *testing.T) {
	for _, bad := range []string{"", "-y", "--setopt=module_stream_switch=1", "nginx 1.24", "nginx;reboot"} {
		if err := dnfModuleValidate([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"nginx", "nginx:1.24", "redis:7/common", "perl-IO-Socket-SSL:2.066", "389-ds", "node*"} {
		if err := dnfModuleValidate([]string{good}); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	if err := dnfModuleValidate(nil); err == nil {
		t.Error("no modules at all accepted")
	}
}

func TestDnfModuleListArgv(t *testing.T) {
	argv, err := dnfModuleListArgv("nginx", "enabled")
	if err != nil || strings.Join(argv, " ") != "dnf -q module list --enabled nginx" {
		t.Errorf("argv = %v, %v", argv, err)
	}
	if _, err := dnfModuleListArgv("--all", ""); err == nil {
		t.Error("a name starting with - was passed to dnf")
	}
}

// dnfScriptedRunner answers one `dnf module` command as the real host
// did and, when that command succeeded, leaves behind the modules.d file
// the real host was left with -- the part a RecordingRunner cannot do.
type dnfScriptedRunner struct {
	exec.RecordingRunner
	dir, file, text string
}

func (r *dnfScriptedRunner) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	res, err := r.RecordingRunner.Run(ctx, cmd)
	if res.Code == 0 && r.file != "" {
		_ = os.WriteFile(filepath.Join(r.dir, r.file), []byte(r.text), 0o644)
	}
	return res, err
}

func TestDnfModuleEnableReportsTheChangeModulesDotDRecorded(t *testing.T) {
	dir := dnfModulesDirOf(t, nil)
	runner := &dnfScriptedRunner{dir: dir, file: "nginx.module", text: dnfFixture(t, "rocky9", "nginx-enabled.module")}
	c := &exec.Context{Runner: runner, Lookup: func(n string) string { return "/usr/bin/" + n }}
	out, err := dnfModuleMutate(c, "enable", []string{"nginx:1.24"})
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.RanCommands(); len(got) != 1 || got[0] != "dnf -y -q module enable nginx:1.24" {
		t.Errorf("ran %v", got)
	}
	changes, _ := out.(*value.Map).Get("changes")
	nginx, ok := changes.(*value.Map).Get("nginx")
	if !ok {
		t.Fatalf("changes = %v, want nginx", changes)
	}
	if old, _ := nginx.(*value.Map).Get("old"); old != nil {
		t.Errorf("old = %v, want nil for a module never touched", old)
	}
	neu, _ := nginx.(*value.Map).Get("new")
	if st, _ := neu.(*value.Map).Get("stream"); st != "1.24" {
		t.Errorf("new = %v", neu)
	}
}

// The one error dnf prints on two lines, where only the second says
// which module: c.Run's own error would keep "Error: Problems in
// request:" and nothing else. Real stderr, from alma8.
func TestDnfModuleFailureCarriesAllOfDnfsStderr(t *testing.T) {
	dnfModulesDirOf(t, nil)
	runner := &dnfScriptedRunner{}
	runner.Responses = map[string]exec.Result{"dnf -y -q module enable nosuchmodule:1": {
		Code:   1,
		Stderr: "Error: Problems in request:\nmissing groups or modules: nosuchmodule:1\n",
	}}
	c := &exec.Context{Runner: runner, Lookup: func(n string) string { return "/usr/bin/" + n }}
	_, err := dnfModuleMutate(c, "enable", []string{"nosuchmodule:1"})
	if err == nil || !strings.Contains(err.Error(), "missing groups or modules: nosuchmodule:1") {
		t.Fatalf("err = %v, want dnf's second line in it", err)
	}
}

// exec.OSRunner turns a non-zero exit into an error unless the command
// asks for its exit code, and RecordingRunner does not reproduce that --
// so a module that reads res.Code without the flag passes every fake and
// never reaches its own branch on hardware (DIVERGENCE 5.113). This is
// the property the fakes cannot check behaviourally.
func TestDnfModuleCommandsAskForTheirExitCode(t *testing.T) {
	dnfModulesDirOf(t, nil)
	runner := &dnfScriptedRunner{}
	c := &exec.Context{Runner: runner, Lookup: func(n string) string { return "/usr/bin/" + n }}
	_, _ = dnfModuleMutate(c, "reset", []string{"nginx"})
	_, _ = dnfModuleListFn(c, value.NewMap(0))
	if len(runner.Ran) != 2 {
		t.Fatalf("ran %v", runner.RanCommands())
	}
	for _, cmd := range runner.Ran {
		if !cmd.IgnoreExitCode {
			t.Errorf("`%s` reads its exit code without asking for it", cmd.String())
		}
	}
}

func TestDnfModuleListOfANameThatMatchesNothingIsEmpty(t *testing.T) {
	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"dnf -q module list nosuchmodule": {Code: 1, Stderr: dnfFixture(t, "alma8", "list-nosuch-q.stderr")},
	}}
	c := &exec.Context{Runner: runner, Lookup: func(n string) string { return "/usr/bin/" + n }}
	out, err := dnfModuleListFn(c, value.MapOf("name", "nosuchmodule"))
	if err != nil || len(out.([]any)) != 0 {
		t.Fatalf("list = %v, %v; want an empty list", out, err)
	}
}

func TestDnfModuleRefusesWithoutDnf(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := dnfModuleMutate(c, "enable", []string{"nginx:1.24"}); err == nil ||
		!strings.Contains(err.Error(), "dnf") {
		t.Fatalf("err = %v, want it to name dnf", err)
	}
}
