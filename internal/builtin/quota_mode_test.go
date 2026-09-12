package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `quotaon -p` is a Linux option, and asking a BSD for it is a defect
// this module carried from the day it was written.
//
// It is the shape plan.md §1.4 keeps finding: a branch that only one
// platform takes, written from the other platform's manual, with no test
// that forces it. Here the module's own comment asserted that "both
// platforms print ... is on or ... is off for -p". FreeBSD 15.1 prints
// neither, because it has no -p at all:
//
//	quotaon: illegal option -- p
//	usage: quotaon [-g] [-u] [-v] -a
//	       quotaon [-g] [-u] [-v] filesystem ...
//
// and exits 1, so `quota.get_mode` could not answer on four of this
// fleet's five hosts. This holds the platform table to the two tools as
// they really are, from any host.
func TestOnlyLinuxIsAskedForAQuotaFlagOnlyLinuxHas(t *testing.T) {
	for _, tc := range []struct {
		goos      string
		wantFlag  bool
		wantKnown bool
	}{
		{"linux", false, true},
		{"freebsd", true, true},
		{"openbsd", true, true},
		{"netbsd", true, true},
		{"dragonfly", true, true},
		{"darwin", false, false},
		{"windows", false, false},
	} {
		flag, why := quotaModePlan(tc.goos)
		if (why != "") != tc.wantKnown {
			t.Errorf("%s: known=%v, want %v (%q)", tc.goos, why != "", tc.wantKnown, why)
		}
		if flag != tc.wantFlag {
			t.Errorf("%s: reads the mount flag=%v, want %v -- a BSD has no `quotaon -p` "+
				"and Linux has no MNT_QUOTA to read", tc.goos, flag, tc.wantFlag)
		}
	}
}

// The BSD mount flag is read out of the flag list and nowhere else.
//
// The fixture lines are this host's own `mount` output with one quota'd
// filesystem added, spelled the way sys/mount.h spells it:
//
//	{ MNT_QUOTA,	"with quotas" },
//
// The traps are deliberate. A mount point *named* `/with quotas` and a
// device with the words in it must not be mistaken for the flag, because
// the whole reason this reads inside the parentheses is that the rest of
// the line is operator-supplied text.
func TestTheQuotaMountFlagIsReadFromTheFlagsAndNotTheRestOfTheLine(t *testing.T) {
	const out = `zroot/ROOT/default on / (zfs, local, noatime, nfsv4acls)
devfs on /dev (devfs)
/dev/md0 on /quota (ufs, local, soft-updates, with quotas)
/dev/md1 on /plain (ufs, local, soft-updates)
/dev/md2 on /with quotas (ufs, local)
with quotas on /tricky (ufs, local)
`
	for _, tc := range []struct {
		fs          string
		on, found   bool
		description string
	}{
		{"/quota", true, true, "the flag is present in the flag list"},
		{"/plain", false, true, "a ufs mount with no quotas is an answer, not a miss"},
		{"/with quotas", false, true, "a mount point that reads like the flag is not the flag"},
		{"/tricky", false, true, "a device that reads like the flag is not the flag"},
		{"/absent", false, false, "a filesystem `mount` does not list cannot be answered for"},
	} {
		on, found := quotaMountFlagSaysQuotas(out, tc.fs)
		if on != tc.on || found != tc.found {
			t.Errorf("%s: on=%v found=%v, want on=%v found=%v -- %s",
				tc.fs, on, found, tc.on, tc.found, tc.description)
		}
	}
}

// A repquota that never looked is refused rather than read as empty.
//
// This is the BSD half of the guard `quotaLooksLikeCSV` already gives
// Linux, and it was missing for the same reason every other gap in this
// module was: the BSD branch was written from repquota.c and never run.
// Asked about a filesystem that is not in fstab, FreeBSD 15.1's repquota
// prints its reason on stderr, prints nothing on stdout, and **exits
// zero** -- captured on this host:
//
//	$ repquota -u -v /; echo $?
//	repquota: / not found in fstab
//	0
//
// Parsed on stdout alone that is a report of no quotas, which is the
// believable kind of wrong: it is exactly what a filesystem without
// quotas really says.
func TestARepquotaThatNeverLookedIsNotAnEmptyReport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     exec.Result
		wantErr string
	}{
		{
			name:    "not in fstab, and it exits zero saying so",
			res:     exec.Result{Code: 0, Stderr: "repquota: / not found in fstab\n"},
			wantErr: "not found in fstab",
		},
		{
			name:    "nothing at all, which is the same failure without the courtesy",
			res:     exec.Result{Code: 0},
			wantErr: "nothing at all",
		},
		{
			name:    "a non-zero exit is refused too",
			res:     exec.Result{Code: 1, Stderr: "repquota: cannot open quota file\n"},
			wantErr: "exit 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := quotaTableLooksLikeAReport(tc.res, "/mnt")
			if err == nil {
				t.Fatalf("a repquota that never looked was accepted as an empty report")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the error does not carry %q, so it sends the operator nowhere: %v",
					tc.wantErr, err)
			}
		})
	}
}

// A banner with no rows under it is a real answer and stays one.
//
// A filesystem with quotas switched on and nobody holding a limit
// reports exactly that, and turning it into an error would trade one
// wrong answer for another.
func TestAFilesystemWithQuotasAndNoLimitsReportsNoLimits(t *testing.T) {
	res := exec.Result{
		Code:   0,
		Stdout: "*** Report for user quotas on /quota (/dev/md0)\n",
	}
	if err := quotaTableLooksLikeAReport(res, "/quota"); err != nil {
		t.Fatalf("a report that looked and found nothing was refused: %v", err)
	}
	rows, err := quotaParseTable(res.Stdout, "/quota")
	if err != nil {
		t.Fatalf("the banner alone did not parse: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a report with no rows produced %d", len(rows))
	}
}
