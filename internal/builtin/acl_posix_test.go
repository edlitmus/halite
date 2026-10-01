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

// The POSIX.1e half of `acl`, against getfacl output captured on two
// hosts on 2026-09-30, as root, and kept verbatim under testdata/acl:
//
//   - debian13: Debian 13, acl 2.3.2 (getfacl/setfacl 2.3.2), in a
//     directory on /tmp (tmpfs);
//   - freebsd15: FreeBSD 15.1-RELEASE-p3, base getfacl/setfacl, on a
//     32 MiB swap-backed memory disk with `newfs -U` and `mount -o acls`.
//
// Each was made by the same sequence, in a fresh directory holding a
// file f and directories dir and dir2:
//
//	trivial-file  getfacl f
//	named-user    setfacl -m user:nobody:rwx f; getfacl f
//	masked        setfacl -m group:nogroup:r-x,mask::r-- f; getfacl f
//	default-dir   setfacl -d -m user:nobody:r-x dir; getfacl dir
//	              (FreeBSD: -d -m user::rwx,group::r-x,other::r-x,user:nobody:r-x,
//	              because it refuses the shorter call -- see acl.go)
//	default-only  getfacl -d dir
//	default-none  getfacl -d dir2
func aclCapture(t *testing.T, host, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "acl", host, name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var aclCaptureHosts = []string{"debian13", "freebsd15"}

func TestBothPOSIXGrammarsReadTheSameEntriesFromTheirOwnSpelling(t *testing.T) {
	want := map[string][]aclEntry{
		"trivial-file": {
			{tag: "user", permissions: "rw-"}, {tag: "group", permissions: "r--"}, {tag: "other", permissions: "r--"},
		},
		"named-user": {
			{tag: "user", permissions: "rw-"}, {tag: "user", qualifier: "nobody", permissions: "rwx"},
			{tag: "group", permissions: "r--"}, {tag: "mask", permissions: "rwx"}, {tag: "other", permissions: "r--"},
		},
		// The listed permissions, not the effective ones: the comment
		// after each masked entry differs between the two hosts and is
		// read by neither.
		"masked": {
			{tag: "user", permissions: "rw-"}, {tag: "user", qualifier: "nobody", permissions: "rwx"},
			{tag: "group", permissions: "r--"}, {tag: "group", qualifier: "nogroup", permissions: "r-x"},
			{tag: "mask", permissions: "r--"}, {tag: "other", permissions: "r--"},
		},
		// Linux prints `default:` lines here as well; they are skipped,
		// so both hosts read the access ACL alone.
		"default-dir": {
			{tag: "user", permissions: "rwx"}, {tag: "group", permissions: "r-x"}, {tag: "other", permissions: "r-x"},
		},
		"default-only": {
			{tag: "user", permissions: "rwx"}, {tag: "user", qualifier: "nobody", permissions: "r-x"},
			{tag: "group", permissions: "r-x"}, {tag: "mask", permissions: "r-x"}, {tag: "other", permissions: "r-x"},
		},
		"default-none": nil,
	}
	for _, host := range aclCaptureHosts {
		for name, entries := range want {
			t.Run(host+"/"+name, func(t *testing.T) {
				owner, _, got, err := parseACLOutput(aclCapture(t, host, name))
				if err != nil {
					t.Fatalf("a real getfacl answer did not parse: %v", err)
				}
				if owner != "root" {
					t.Errorf("owner = %q, want root", owner)
				}
				if len(got) != len(entries) {
					t.Fatalf("parsed %d entries, want %d: %+v", len(got), len(entries), got)
				}
				for i := range entries {
					if got[i] != entries[i] {
						t.Errorf("entry %d = %+v, want %+v", i, got[i], entries[i])
					}
				}
				if len(got) > 0 && aclFamilyOf(got) != aclFamilyPOSIX {
					t.Errorf("family = %s, want %s", aclFamilyOf(got), aclFamilyPOSIX)
				}
			})
		}
	}
}

// The two spellings of the effective-rights comment, kept in front of a
// reader so that a change to one host's tool is visible here first.
func TestTheEffectiveRightsCommentIsSpelledDifferentlyOnEachHost(t *testing.T) {
	linux, freebsd := aclCapture(t, "debian13", "masked"), aclCapture(t, "freebsd15", "masked")
	if !strings.Contains(linux, "user:nobody:rwx\t#effective:r--") {
		t.Errorf("the Debian capture no longer carries `\\t#effective:`:\n%s", linux)
	}
	if !strings.Contains(freebsd, "user:nobody:rwx\t\t# effective: r--") {
		t.Errorf("the FreeBSD capture no longer carries `\\t\\t# effective: `:\n%s", freebsd)
	}
}

func TestAnACLMixingTheTwoFamiliesIsRefused(t *testing.T) {
	mixed := "user::rw-\n            owner@:rw-p--aARWcCos:-------:allow\n"
	if _, _, _, err := parseACLOutput(mixed); err == nil {
		t.Error("an ACL holding a POSIX.1e and an NFSv4 entry was read as one ACL")
	}
}

func TestPOSIXPermissionsCompareInGetfaclsColumnForm(t *testing.T) {
	for in, want := range map[string]string{
		"rwx": "rwx", "xwr": "rwx", "rx": "r-x", "r-x": "r-x", "r": "r--", "": "---", "w": "-w-", "---": "---",
	} {
		got, err := canonicalPOSIXPerms(in)
		if err != nil || got != want {
			t.Errorf("canonicalPOSIXPerms(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// FreeBSD's setfacl refuses both (measured), so both are refused here
	// on both platforms rather than meaning something on one alone.
	for _, in := range []string{"7", "X", "rwX", "rwp"} {
		if _, err := canonicalPOSIXPerms(in); err == nil {
			t.Errorf("canonicalPOSIXPerms(%q) was accepted", in)
		}
	}
}

func TestPOSIXSetReportsUnchangedWhenTheEntryIsAlreadyThere(t *testing.T) {
	for _, host := range aclCaptureHosts {
		path := "/scratch/f"
		c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, host, "named-user")}})
		out, err := aclSetFn(c, aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "xrw"))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
			t.Errorf("%s: an entry already rwx reported a change: %v", host, out)
		}
		if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
			t.Errorf("%s: setfacl ran with nothing to change: %v", host, cmds)
		}
	}
}

func TestPOSIXSetSendsTheEntryInGetfaclsOwnSpelling(t *testing.T) {
	for _, host := range aclCaptureHosts {
		path := "/scratch/f"
		c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, host, "named-user")}})
		if _, err := aclSetFn(c, aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "rx")); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		want := "setfacl -m user:nobody:r-x " + path
		if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != want {
			t.Errorf("%s: setfacl calls = %v, want [%s]", host, cmds, want)
		}
	}
}

func TestPOSIXSetInTestModeRunsNoSetfacl(t *testing.T) {
	path := "/scratch/f"
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "trivial-file")}})
	c.Test = true
	out, err := aclSetFn(c, aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "r"))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("test mode did not predict the change: %v", out)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("test mode ran setfacl: %v", cmds)
	}
}

// A directory with no default ACL yet gets the three required entries
// along with the one asked for, copied from its access ACL: what Linux
// does by itself, and what FreeBSD refuses to do without being given.
func TestADefaultACLIsSeededFromTheAccessACLWhenThereIsNone(t *testing.T) {
	for _, host := range aclCaptureHosts {
		dir := aclScratchDir(t)
		c := aclContext(map[string]exec.Result{
			"getfacl " + dir:    {Stdout: aclCapture(t, host, "default-dir")},
			"getfacl -d " + dir: {Stdout: aclCapture(t, host, "default-none")},
		})
		args := aclArgs("name", dir, "tag", "user", "qualifier", "nobody", "perms", "rx", "default", true)
		if _, err := aclSetFn(c, args); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		want := "setfacl -d -m user::rwx,group::r-x,other::r-x,user:nobody:r-x " + dir
		if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != want {
			t.Errorf("%s: setfacl calls = %v, want [%s]", host, cmds, want)
		}
	}
}

func TestADefaultEntryAlreadyThereIsReadFromGetfaclDashD(t *testing.T) {
	for _, host := range aclCaptureHosts {
		dir := aclScratchDir(t)
		c := aclContext(map[string]exec.Result{
			"getfacl " + dir:    {Stdout: aclCapture(t, host, "default-dir")},
			"getfacl -d " + dir: {Stdout: aclCapture(t, host, "default-only")},
		})
		out, err := aclSetFn(c, aclArgs("name", dir, "tag", "user", "qualifier", "nobody", "perms", "r-x", "default", true))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
			t.Errorf("%s: a default entry already r-x reported a change: %v", host, out)
		}
		// And a different one is added with -d alone: the default ACL
		// exists, so nothing is seeded.
		if _, err := aclSetFn(c, aclArgs("name", dir, "tag", "group", "qualifier", "nogroup", "perms", "r", "default", true)); err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		want := "setfacl -d -m group:nogroup:r-- " + dir
		if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != want {
			t.Errorf("%s: setfacl calls = %v, want [%s]", host, cmds, want)
		}
	}
}

func TestPOSIXRemoveSendsTheThreeFieldFormBothToolsAccept(t *testing.T) {
	for _, host := range aclCaptureHosts {
		path := "/scratch/f"
		c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, host, "masked")}})
		out, err := aclRemoveFn(c, aclArgs("name", path, "tag", "group", "qualifier", "nogroup"))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
			t.Errorf("%s: removing a present entry reported no change: %v", host, out)
		}
		want := "setfacl -x group:nogroup: " + path
		if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != want {
			t.Errorf("%s: setfacl calls = %v, want [%s]", host, cmds, want)
		}
	}
}

// FreeBSD's setfacl exits 1 removing an entry that is not there, so it
// must never be asked to.
func TestPOSIXRemoveOfAnAbsentEntryRunsNoSetfacl(t *testing.T) {
	path := "/scratch/f"
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "freebsd15", "trivial-file")}})
	out, err := aclRemoveFn(c, aclArgs("name", path, "tag", "user", "qualifier", "nobody"))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("removing an absent entry reported a change: %v", out)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("setfacl ran for an entry that is not there: %v", cmds)
	}
}

// Every argument that only means something to NFSv4 is refused by name
// on a POSIX.1e path rather than dropped.
func TestNFSv4OnlyArgumentsAreRefusedOnAPOSIXPath(t *testing.T) {
	path := "/scratch/f"
	for label, args := range map[string]*value.Map{
		"deny":            aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "r", "type", "deny"),
		"flags":           aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "r", "flags", "fd"),
		"position":        aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "r", "position", int64(0)),
		"an @ tag":        aclArgs("name", path, "tag", "everyone@", "perms", "r"),
		"a named mask":    aclArgs("name", path, "tag", "mask", "qualifier", "nobody", "perms", "r"),
		"an octal digit":  aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "7"),
		"default on file": aclArgs("name", path, "tag", "user", "qualifier", "nobody", "perms", "r", "default", true),
		"recursive default": aclArgs("name", t.TempDir(), "tag", "user", "qualifier", "nobody", "perms", "r",
			"default", true, "recursive", true),
	} {
		c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "trivial-file")}})
		c.Runner.(*exec.RecordingRunner).Default = exec.Result{Stdout: aclCapture(t, "debian13", "default-dir")}
		if _, err := aclSetFn(c, args); err == nil {
			t.Errorf("%s was accepted on a POSIX.1e path", label)
		}
		if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
			t.Errorf("%s ran setfacl: %v", label, cmds)
		}
	}
}

func TestTheRequiredPOSIXEntriesAreNotRemovable(t *testing.T) {
	path := "/scratch/f"
	for _, args := range []*value.Map{
		aclArgs("name", path, "tag", "user"),
		aclArgs("name", path, "tag", "group"),
		aclArgs("name", path, "tag", "user", "qualifier", "nobody", "recursive", true),
	} {
		c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclCapture(t, "debian13", "named-user")}})
		if _, err := aclRemoveFn(c, args); err == nil {
			t.Errorf("acl.remove %v was accepted", args)
		}
	}
}

// A default ACL is NFSv4's to refuse, by name: that family has none.
func TestADefaultACLIsRefusedOnAnNFSv4Path(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{"getfacl " + path: {Stdout: aclRealCapture}})
	_, err := aclSetFn(c, aclArgs("name", path, "tag", "user", "qualifier", "games", "perms", "r", "default", true))
	if err == nil || !strings.Contains(err.Error(), "NFSv4") {
		t.Errorf("a default entry on an NFSv4 path = %v, want a refusal naming NFSv4", err)
	}
}

func TestGetReportsTheFamilyAndAPOSIXDirectorysDefaults(t *testing.T) {
	dir := aclScratchDir(t)
	c := aclContext(map[string]exec.Result{
		"getfacl " + dir:    {Stdout: aclCapture(t, "debian13", "default-dir")},
		"getfacl -d " + dir: {Stdout: aclCapture(t, "debian13", "default-only")},
	})
	out, err := aclGetFn(c, aclArgs("name", dir))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if family, _ := m.GetString("family"); family != aclFamilyPOSIX {
		t.Errorf("family = %v", family)
	}
	defaults, _ := m.GetString("default_entries")
	if list, ok := defaults.([]any); !ok || len(list) != 5 {
		t.Errorf("default_entries = %v, want the five getfacl -d printed", defaults)
	}
}

// Linux's `getfacl -h` is --help, and exits 0.
func TestNotFollowingASymlinkIsRefusedOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the refusal is Linux's; this is %s", runtime.GOOS)
	}
	c := aclContext(nil)
	_, err := aclGetFn(c, aclArgs("name", "/scratch/l", "follow_symlink", false))
	if err == nil {
		t.Fatal("follow_symlink=false was accepted on Linux, where getfacl -h prints the usage")
	}
	if ran := c.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 0 {
		t.Errorf("getfacl ran anyway: %v", ran)
	}
}

// aclScratchDir is a real directory for the tests that need one, because
// set and the states ask the filesystem which paths are directories.
//
// Not on Windows: the recorded getfacl answers are keyed by the command
// line, and Command.String quotes an argument holding a backslash, so a
// Windows path never matches its key. The module does not run there; the
// captures are read on every other platform.
func aclScratchDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the recorded getfacl answers are keyed by a command line, which quotes a Windows path; acl does not run on Windows")
	}
	return t.TempDir()
}
