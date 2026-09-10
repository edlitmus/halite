package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The fixed-width fixtures below are not remembered output.
//
// `freebsdRepquota` is written to the `printf` calls in FreeBSD 15.1's
// own usr.sbin/repquota/repquota.c, read off /usr/src on the host this
// project is developed on: the name padded to 32, one space, the two
// flag characters with no separator between them, `%6ju` for each block
// column, `%6s` for a grace, then `%7ju` three times and `%6s` again.
// `linuxRepquota` is quota-tools' report, whose grace columns are blank
// rather than `-` and whose header carries a rule of dashes.
//
// The difference between them is the whole reason this parser is not a
// `strings.Fields`: the same row has ten fields on one platform and
// eight on the other, and which it has depends on whether an account is
// over its limit.

const freebsdRepquota = `*** Report for user quotas on /home (/dev/da0s1e)
                                      Block  limits                    File  limits
User                              used   soft   hard  grace     used    soft    hard  grace
alice                            --  12000  20000  25000      -       43     500    1000      -
bob                              +-  21500  20000  25000  6days       12     500    1000      -
`

const linuxRepquota = `*** Report for user quotas on device /dev/sdb1
Block grace time: 7days; Inode grace time: 7days
                        Block limits                File limits
User            used    soft    hard  grace    used  soft  hard  grace
----------------------------------------------------------------------
alice     --   12000   20000   25000             43   500  1000
bob       +-   21500   20000   25000  6days      12   500  1000
`

const linuxRepquotaCSV = `User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,BlockGrace,FileUsed,FileSoftLimit,FileHardLimit,FileGrace
alice,ok,ok,12000,20000,25000,,43,500,1000,
bob,EXPIRED,ok,21500,20000,25000,6days,12,500,1000,
`

// The two readers agree on the same two accounts.
//
// FreeBSD is read from the fixed-width report and Linux from CSV, so
// these are separate code paths over the same facts, and every function
// above them is written against one row type. The pair agreeing is the
// contract — and a pair of paths that must agree and do not is this
// project's commonest defect shape.
func TestTheBSDTableAndTheLinuxCSVReadIntoTheSameRows(t *testing.T) {
	fromTable, err := quotaParseTable(freebsdRepquota, "/home")
	if err != nil {
		t.Fatalf("the FreeBSD report: %v", err)
	}
	fromCSV, err := quotaParseCSV(linuxRepquotaCSV, "/home")
	if err != nil {
		t.Fatalf("the Linux CSV: %v", err)
	}
	if len(fromTable) != 2 || len(fromCSV) != 2 {
		t.Fatalf("read %d rows from the table and %d from the CSV, want 2 of each",
			len(fromTable), len(fromCSV))
	}
	for i := range fromTable {
		if fromTable[i] != fromCSV[i] {
			t.Errorf("row %d differs between the two readers:\n  table: %+v\n  csv:   %+v",
				i, fromTable[i], fromCSV[i])
		}
	}

	alice, bob := fromTable[0], fromTable[1]
	for _, c := range []struct {
		label string
		got   int64
		want  int64
	}{
		{"alice blocks used", alice.BlocksUsed, 12000},
		{"alice block soft", alice.BlockSoft, 20000},
		{"alice block hard", alice.BlockHard, 25000},
		{"alice files used", alice.FilesUsed, 43},
		{"alice inode soft", alice.InodeSoft, 500},
		{"alice inode hard", alice.InodeHard, 1000},
		{"bob blocks used", bob.BlocksUsed, 21500},
	} {
		if c.got != c.want {
			t.Errorf("%s read as %d, want %d", c.label, c.got, c.want)
		}
	}
	// A grace that is not running is `-` on FreeBSD and nothing at all in
	// the CSV, and both mean the same thing.
	if alice.BlockGrace != "" {
		t.Errorf("alice is under her limit and her grace reads %q", alice.BlockGrace)
	}
	if bob.BlockGrace != "6days" {
		t.Errorf("bob's running grace reads %q, want 6days", bob.BlockGrace)
	}
	if !bob.OverBlocks || bob.OverFiles {
		t.Errorf("bob's flags read as blocks=%v files=%v, want blocks over and files not",
			bob.OverBlocks, bob.OverFiles)
	}
	if alice.OverBlocks || alice.OverFiles {
		t.Error("alice is under both limits and her flags read as over")
	}
}

// Linux's fixed-width report is refused rather than guessed at.
//
// This is the assertion behind the design, so it is worth stating as a
// test rather than only as a comment. quota-tools leaves a grace column
// blank, so alice's row below has eight fields and bob's has nine, and
// there is no rule that recovers which column is missing — a grace
// period of under an hour prints as a bare number, so "is field three a
// number" does not settle it. A reader that took the row anyway would
// file one account's inode count as another's block limit.
func TestTheLinuxFixedWidthReportIsRefusedRatherThanGuessedAt(t *testing.T) {
	if _, err := quotaParseTable(linuxRepquota, "/home"); err == nil {
		t.Fatal("quota-tools' ambiguous report was parsed as though it were unambiguous")
	}
	if csv, why := quotaReadPlan("linux"); !csv || why == "" {
		t.Errorf("Linux should be read from CSV; the plan says csv=%v (%s)", csv, why)
	}
	for _, goos := range []string{"freebsd", "openbsd", "netbsd", "dragonfly"} {
		csv, why := quotaReadPlan(goos)
		if csv {
			t.Errorf("%s has no `repquota -O` and the plan asks for CSV", goos)
		}
		if why == "" {
			t.Errorf("%s has quota tools and the plan does not know how to read them", goos)
		}
	}
	if _, why := quotaReadPlan("windows"); why != "" {
		t.Errorf("Windows has no filesystem quotas of this kind and the plan claims one: %s", why)
	}
}

// A humanised report is refused rather than read as small numbers.
//
// `repquota -h` prints `1.5G` where this reader expects a count of
// kilobytes. Read as a number that is 1, and an account using a gigabyte
// and a half would be reported as using one block — which is not a
// parse failure anybody would notice, and would be believed. This build
// never passes `-h`, and this is the assertion that a report carrying
// one is refused instead.
func TestAHumanisedReportIsRefusedRatherThanMisread(t *testing.T) {
	const humanised = `*** Report for user quotas on /home (/dev/da0s1e)
User                              used   soft   hard  grace     used    soft    hard  grace
alice                            --   1.5G   2.0G   2.5G      -       43     500    1000      -
`
	if _, err := quotaParseTable(humanised, "/home"); err == nil {
		t.Fatal("a humanised report was parsed as a report of small numbers")
	} else if !strings.Contains(err.Error(), "-h") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// A `-a` run learns each filesystem from the banner above its rows.
//
// Without this every account on every filesystem would be filed under
// one name, and a report saying alice is at her limit would not say
// where.
func TestAnAllFilesystemsReportKeepsTheFilesystemsApart(t *testing.T) {
	const all = `*** Report for user quotas on /home (/dev/da0s1e)
User                              used   soft   hard  grace     used    soft    hard  grace
alice                            --  12000  20000  25000      -       43     500    1000      -

*** Report for user quotas on /var/mail (/dev/da0s1f)
User                              used   soft   hard  grace     used    soft    hard  grace
alice                            --    900   5000  10000      -       12     100     200      -
`
	rows, err := quotaParseTable(all, "-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}
	if rows[0].Filesystem != "/home" || rows[1].Filesystem != "/var/mail" {
		t.Errorf("the two rows are filed under %q and %q", rows[0].Filesystem, rows[1].Filesystem)
	}
}

// Linux's banner names a device and FreeBSD's names a mount point, and
// each is reported as written.
func TestTheBannerIsReadOnBothPlatforms(t *testing.T) {
	for line, want := range map[string]string{
		"*** Report for user quotas on /home (/dev/da0s1e)": "/home",
		"*** Report for user quotas on device /dev/sdb1":    "/dev/sdb1",
		"*** Report for group quotas on /var (/dev/da0s1d)": "/var",
	} {
		got, ok := quotaReportHeaderMount(line)
		if !ok || got != want {
			t.Errorf("%q read as %q (ok=%v), want %q", line, got, ok, want)
		}
	}
	if _, ok := quotaReportHeaderMount("*** No user quotas on /home (/dev/da0s1e)"); !ok {
		t.Error("the `no quotas` banner should still say which filesystem it is about")
	}
}

// A usage message is not a report.
//
// A tool that does not know `-O` may print its usage and exit zero, and
// a usage message read as a table is a report of quotas that do not
// exist. The CSV path is entered on the header rather than on the exit
// code for this reason.
func TestAUsageMessageIsNotMistakenForCSV(t *testing.T) {
	if quotaLooksLikeCSV("usage: repquota [-h] [-v] [-g] [-n] [-u] -a\n") {
		t.Error("repquota's usage message was taken for CSV output")
	}
	if !quotaLooksLikeCSV(linuxRepquotaCSV) {
		t.Error("quota-tools' own CSV header was not recognised")
	}
}

// A ZFS filesystem is reported as having its quotas elsewhere, not as
// having none.
//
// This is the whole of what `repquota` gets wrong on this project's own
// fleet, which is entirely ZFS: it reports nothing, and nothing read as
// "no quotas" is false about a dataset that may have a quota on every
// account.
func TestAZFSFilesystemIsNotReportedAsHavingNoQuotas(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}}
	table := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(table, []byte("zroot/usr/home /usr/home zfs rw,noatime 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := ProcMountsPath
	ProcMountsPath = table
	t.Cleanup(func() { ProcMountsPath = old })

	why := quotaWrongMechanism(c, "/usr/home")
	if why == "" {
		t.Fatal("a ZFS filesystem was treated as one the quota tools can see")
	}
	for _, want := range []string{"ZFS", "zfs.get", "userquota@"} {
		if !strings.Contains(why, want) {
			t.Errorf("the answer does not mention %q: %s", want, why)
		}
	}

	// And a UFS filesystem in the same table is left alone.
	if why := quotaWrongMechanism(c, "/nowhere"); why != "" {
		t.Errorf("a filesystem that is not ZFS was diverted: %s", why)
	}
}

// A soft limit above its hard limit is refused.
//
// Both tools accept it, and what it means is that the soft limit never
// fires: the account gets no warning and no grace period, and simply
// stops writing one day. That is the failure a quota exists to prevent.
func TestASoftLimitAboveItsHardLimitIsRefused(t *testing.T) {
	r := New()
	args := value.NewMap(6)
	args.Set("filesystem", "/home")
	args.Set("name", "alice")
	args.Set("block_soft", int64(30000))
	args.Set("block_hard", int64(20000))
	_, err := r.Exec.Call(&exec.Context{Test: true, Runner: &exec.RecordingRunner{}}, "quota.set", args)
	if err == nil {
		t.Fatal("a soft limit above its hard limit was accepted")
	}
	if !strings.Contains(err.Error(), "never fires") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// Every platform's argument vector is the one its tool takes, checked
// from any host.
//
// This is plan.md §1.4's lesson applied before it costs anything. A
// decision made from runtime.GOOS is only checkable on the platform it
// decides for, so `hostname`'s FreeBSD branch had no test at all behind
// a fixture that looked like one. Here the three things that differ
// between the two tools — the name, the order, and the separator — are
// pinned for both, from wherever this happens to run.
//
// The order of the four limits is the part worth being exact about. Both
// tools accept a vector with the block and inode pairs transposed, so
// getting it wrong sets an inode limit as a block limit on a filesystem
// somebody is relying on, and nothing refuses it.
func TestEveryPlatformsQuotaCommandIsTheOneItsToolTakes(t *testing.T) {
	for goos, want := range map[string]string{
		// setquota [-u|-g] name bsoft bhard isoft ihard filesystem
		"linux": "setquota -u alice 1024 2048 100 200 /home",
		// edquota [-u|-g] -e fspath:bslim:bhlim:islim:ihlim username
		"freebsd":   "edquota -u -e /home:1024:2048:100:200 alice",
		"openbsd":   "edquota -u -e /home:1024:2048:100:200 alice",
		"netbsd":    "edquota -u -e /home:1024:2048:100:200 alice",
		"dragonfly": "edquota -u -e /home:1024:2048:100:200 alice",
	} {
		argv, err := quotaSetArgv(goos, "user", "alice", "/home", 1024, 2048, 100, 200)
		if err != nil {
			t.Errorf("%s: %v", goos, err)
			continue
		}
		if got := strings.Join(argv, " "); got != want {
			t.Errorf("%s runs\n  %s\nwant\n  %s", goos, got, want)
		}
	}

	// A group quota is the same command with the other flag, and the
	// flag is in the same place on both.
	for goos, want := range map[string]string{
		"linux":   "setquota -g staff 0 0 0 0 /home",
		"freebsd": "edquota -g -e /home:0:0:0:0 staff",
	} {
		argv, err := quotaSetArgv(goos, "group", "staff", "/home", 0, 0, 0, 0)
		if err != nil {
			t.Errorf("%s: %v", goos, err)
			continue
		}
		if got := strings.Join(argv, " "); got != want {
			t.Errorf("%s runs\n  %s\nwant\n  %s", goos, got, want)
		}
	}

	// A platform this build does not know is told so, rather than given
	// one of the two spellings and a coin toss.
	if _, err := quotaSetArgv("windows", "user", "alice", "C:\\", 1, 2, 3, 4); err == nil {
		t.Error("Windows was given a quota command")
	}
	if _, err := quotaSetArgv("linux", "everyone", "alice", "/home", 1, 2, 3, 4); err == nil {
		t.Error("a quota was set on something that is neither a user nor a group")
	}
}

// The running platform's command is the one the table describes, so the
// table is not a second opinion nobody reads.
func TestThisPlatformUsesTheCommandTheTableNames(t *testing.T) {
	fromTable, tableErr := quotaSetArgv(runtime.GOOS, "user", "alice", "/home", 1, 2, 3, 4)

	r := New()
	args := value.NewMap(7)
	args.Set("filesystem", "/home")
	args.Set("name", "alice")
	args.Set("block_soft", int64(1))
	args.Set("block_hard", int64(2))
	args.Set("inode_soft", int64(3))
	args.Set("inode_hard", int64(4))

	// Test mode reports the command it would run and runs nothing. The
	// lookup is answered from the table so the assertion does not depend
	// on whether this machine has the tool installed.
	c := &exec.Context{
		Test:   true,
		Runner: &exec.RecordingRunner{},
		Lookup: func(name string) string { return "/usr/sbin/" + name },
	}
	out, err := r.Exec.Call(c, "quota.set", args)
	if tableErr != nil {
		if err == nil {
			t.Fatalf("this build has no quota command for %s and quota.set accepted the call anyway", runtime.GOOS)
		}
		t.Skipf("%s has no quota command in this build: %v", runtime.GOOS, tableErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	got, _ := out.(*value.Map).GetString("command")
	if want := (exec.Command{Argv: fromTable}).String(); got != want {
		t.Errorf("quota.set would run\n  %v\nand the table for %s says\n  %s", got, runtime.GOOS, want)
	}

	// Nothing that *changes* anything was run, which is what
	// TestReliable promises. A test run does still read -- it asks the
	// mount table what the filesystem is and repquota what the limits
	// already are, because a prediction that skipped both would be a
	// guess rather than a prediction.
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "setquota") || strings.HasPrefix(ran, "edquota") ||
			strings.HasPrefix(ran, "quotaon") || strings.HasPrefix(ran, "quotaoff") {
			t.Errorf("a test run executed %q, which changes the filesystem", ran)
		}
	}
}

// quotaon and quotaoff take the same flags, and `-a` is a filesystem
// rather than a flag on both.
func TestTheQuotaSwitchCommandsAreSpelledTheSameOnBothPlatforms(t *testing.T) {
	for _, c := range []struct {
		on    bool
		kinds []string
		fs    string
		want  string
	}{
		{true, []string{"user"}, "/home", "quotaon -u -v /home"},
		{false, []string{"user"}, "/home", "quotaoff -u -v /home"},
		{true, []string{"user", "group"}, "-a", "quotaon -u -g -v -a"},
		{false, []string{"group"}, "-a", "quotaoff -g -v -a"},
	} {
		if got := strings.Join(quotaSwitchArgv(c.on, c.kinds, c.fs), " "); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

// `repquota` is never asked for a humanised report, and never asked to
// hide names behind numeric ids.
//
// Both are one character, both would parse, and both would be wrong in a
// way nobody notices: `-h` turns a count of kilobytes into `1.5G`, and
// `-n` turns `alice` into `1001`, so a report would name an id that no
// tree refers to.
func TestRepquotaIsNeverAskedToHumaniseOrToHideNames(t *testing.T) {
	for _, argv := range [][]string{
		quotaReportArgv("user", "/home", false),
		quotaReportArgv("user", "-a", false),
		quotaReportArgv("group", "/home", true),
		quotaReportArgv("group", "-a", true),
	} {
		for _, arg := range argv {
			if arg == "-h" || arg == "-n" {
				t.Errorf("%v asks repquota for %s", argv, arg)
			}
		}
	}
	if got := strings.Join(quotaReportArgv("user", "-a", true), " "); got != "repquota -u -v -O csv -a" {
		t.Errorf("the CSV report is asked for as %q", got)
	}
	if got := strings.Join(quotaReportArgv("group", "/home", false), " "); got != "repquota -g -v /home" {
		t.Errorf("the fixed-width report is asked for as %q", got)
	}
}
