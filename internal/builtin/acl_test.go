package builtin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// aclRealCapture is a real `getfacl` answer, captured verbatim against a
// ZFS path on a FreeBSD 15.1-RELEASE-p3 host with:
//
//	touch testfile
//	setfacl -m u:games:r::allow,u:operator:rwp::allow,g:wheel:rp::allow testfile
//	setfacl -m u:games:w::deny testfile
//	getfacl testfile
//
// It carries one thing no invented fixture would have gotten right on
// the first try: a brand-new tag/qualifier pair setfacl -m adds lands at
// the *front* of the list, ahead of the mandatory owner@/group@/everyone@
// triple, not appended after it.
const aclRealCapture = `# file: /home/ed/aclcapture/testfile
# owner: ed
# group: wheel
        user:games:-w------------:-------:deny
     user:operator:rw-p----------:-------:allow
       group:wheel:r--p----------:-------:allow
            owner@:rw-p--aARWcCos:-------:allow
            group@:r-----a-R-c--s:-------:allow
         everyone@:r-----a-R-c--s:-------:allow
`

func TestParsingARealGetfaclAnswerReadsEveryEntry(t *testing.T) {
	owner, group, entries, err := parseACLOutput(aclRealCapture)
	if err != nil {
		t.Fatalf("a real getfacl answer did not parse: %v", err)
	}
	if owner != "ed" || group != "wheel" {
		t.Errorf("owner/group = %q/%q, want ed/wheel", owner, group)
	}
	if len(entries) != 6 {
		t.Fatalf("parsed %d entries, want 6: %+v", len(entries), entries)
	}

	// The deny entry setfacl -m added last is the one the tool put
	// first, ahead of the entry that was already there for a different
	// qualifier.
	want := []aclEntry{
		{tag: "user", qualifier: "games", permissions: "-w------------", flags: "-------", typ: "deny"},
		{tag: "user", qualifier: "operator", permissions: "rw-p----------", flags: "-------", typ: "allow"},
		{tag: "group", qualifier: "wheel", permissions: "r--p----------", flags: "-------", typ: "allow"},
		{tag: "owner@", permissions: "rw-p--aARWcCos", flags: "-------", typ: "allow"},
		{tag: "group@", permissions: "r-----a-R-c--s", flags: "-------", typ: "allow"},
		{tag: "everyone@", permissions: "r-----a-R-c--s", flags: "-------", typ: "allow"},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], w)
		}
	}
}

// TestAnNFSv4ACLIsNotReadAsAPosixOne is the flip side: a POSIX.1e entry
// has three colon-fields and never ends in "allow" or "deny", and this
// build says so by name rather than misreading it as a malformed NFSv4
// entry or, worse, silently as one with an empty flags and type field.
//
// These three lines are not a captured run: this host mounts nothing
// with POSIX.1e ACLs (every filesystem here is ZFS, which speaks NFSv4
// only), and there was no root to make a UFS filesystem to capture one
// from. They are the three-field grammar `tag:qualifier:perms` quoted
// from setfacl(1)'s own "POSIX.1E ACL ENTRIES" section on this host
// (FreeBSD 15.1-RELEASE-p3, `mandoc -Tascii /usr/share/man/man1/setfacl.1.gz`),
// which is what the parser has to recognise the shape of, not what it
// has to render correctly — recognising the shape is exactly what does
// not need a live filesystem to prove.
func TestAnNFSv4ACLIsNotReadAsAPosixOne(t *testing.T) {
	for _, line := range []string{"user::rwx", "group::r-x", "other::r--", "mask::rwx", "user:games:rw-"} {
		_, err := parseACLEntryLine(line)
		if err == nil {
			t.Errorf("parseACLEntryLine(%q) accepted a POSIX.1e entry as if it were NFSv4", line)
			continue
		}
		if !strings.Contains(err.Error(), "POSIX.1e") {
			t.Errorf("parseACLEntryLine(%q) failed without naming POSIX.1e: %v", line, err)
		}
	}
}

func TestAnUnrecognizedEntryShapeIsAParseErrorNotAGuess(t *testing.T) {
	for _, line := range []string{"", "one", "a:b", "a:b:c:d:e:f:g"} {
		if _, err := parseACLEntryLine(line); err == nil {
			t.Errorf("parseACLEntryLine(%q) should have failed", line)
		}
	}
}

func TestAnACLEntryWithAnUnknownTypeWordIsRejected(t *testing.T) {
	for _, line := range []string{"owner@:rwxp--aARWcCos:-------:maybe", "user:games:rw-p----------:-------:sometimes"} {
		if _, err := parseACLEntryLine(line); err == nil {
			t.Errorf("parseACLEntryLine(%q) accepted a type that is neither allow nor deny", line)
		}
	}
}

// The single-letter fixtures below were each captured by setting exactly
// one permission alone against a scratch file and reading getfacl back,
// on the same FreeBSD 15.1-RELEASE-p3 host:
//
//	setfacl -b testfile; setfacl -m u:games:<letter>::allow testfile; getfacl -q testfile
//
// which is the only way to be sure of the column each one occupies
// rather than assume the man page's listing order is the wire order.
func TestCanonicalPermsMatchesGetfaclsRealColumnOrder(t *testing.T) {
	cases := map[string]string{
		"r": "r-------------", "w": "-w------------", "x": "--x-----------",
		"p": "---p----------", "D": "----D---------", "d": "-----d--------",
		"a": "------a-------", "A": "-------A------", "R": "--------R-----",
		"W": "---------W----", "c": "----------c---", "C": "-----------C--",
		"o": "------------o-", "s": "-------------s",
		"":                     "--------------",
		"rw":                   "rw------------",
		"full_set":             "rwxpDdaARWcCos",
		"modify_set":           "rwxpDdaARWc--s",
		"read_set":             "r-----a-R-c---",
		"write_set":            "-w-p---A-W----",
		"read_data/write_data": "rw------------",
	}
	for in, want := range cases {
		got, err := canonicalPerms(in)
		if err != nil {
			t.Errorf("canonicalPerms(%q) failed: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("canonicalPerms(%q) = %q, want %q", in, got, want)
		}
	}
}

// The flags fixtures were captured the same way, against a scratch
// directory (inheritance flags apply only there):
//
//	setfacl -b fdir; setfacl -m g:wheel:r:<letters>:allow fdir; getfacl -q fdir
//
// This is also where inherit_only alone was found to be refused by the
// kernel with EINVAL unless file_inherit or dir_inherit is set beside
// it. That is a kernel constraint on the combination, not a shape
// canonicalFlags itself can see from one flag string in isolation, so
// it is left to setfacl's own exit code and message to report — the
// same path any other setfacl rejection takes.
func TestCanonicalFlagsMatchesGetfaclsRealColumnOrder(t *testing.T) {
	cases := map[string]string{
		"f":   "f------",
		"d":   "-d-----",
		"fd":  "fd-----",
		"fi":  "f-i----",
		"fI":  "f-----I",
		"fnd": "fd-n---",
		"":    "-------",
	}
	for in, want := range cases {
		got, err := canonicalFlags(in)
		if err != nil {
			t.Errorf("canonicalFlags(%q) failed: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("canonicalFlags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnUnknownPermissionLetterIsRejectedRatherThanSilentlyDropped(t *testing.T) {
	if _, err := canonicalPerms("rz"); err == nil {
		t.Fatal("canonicalPerms(\"rz\") accepted a letter this table does not have")
	}
}

func TestAnUnknownFlagWordIsRejectedRatherThanSilentlyDropped(t *testing.T) {
	if _, err := canonicalFlags("sideways"); err == nil {
		t.Fatal("canonicalFlags(\"sideways\") accepted a word this table does not have")
	}
}

func TestOwnerGroupEveryoneTagsTakeNoQualifier(t *testing.T) {
	if err := aclValidateTagQualifier("owner@", "ed"); err == nil {
		t.Error("owner@ with a qualifier was accepted; setfacl itself refuses this")
	}
	if err := aclValidateTagQualifier("owner@", ""); err != nil {
		t.Errorf("owner@ with no qualifier was refused: %v", err)
	}
}

func TestUserAndGroupTagsRequireAQualifier(t *testing.T) {
	if err := aclValidateTagQualifier("user", ""); err == nil {
		t.Error("a user entry with no qualifier was accepted")
	}
	if err := aclValidateTagQualifier("group", "wheel"); err != nil {
		t.Errorf("a group entry with a qualifier was refused: %v", err)
	}
}

// ---- helpers for the functions that call getfacl/setfacl ----

func aclArgs(kv ...any) *value.Map { return value.MapOf(kv...) }

func aclContext(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/bin/" + name },
	}
}

func aclSetfaclCommands(c *exec.Context) []string {
	var out []string
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "setfacl") {
			out = append(out, ran)
		}
	}
	return out
}

const aclTrivialCapture = `# file: /home/ed/aclcapture/testfile
# owner: ed
# group: wheel
            owner@:rw-p--aARWcCos:-------:allow
            group@:r-----a-R-c--s:-------:allow
         everyone@:r-----a-R-c--s:-------:allow
`

func TestSetReportsUnchangedWhenTheExactEntryIsAlreadyThere(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclRealCapture},
	})
	// The capture already has user:operator:allow with rw-p----------.
	args := aclArgs("name", path, "tag", "user", "qualifier", "operator", "perms", "rwp", "type", "allow")
	out, err := aclSetFn(c, args)
	if err != nil {
		t.Fatalf("aclSetFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != false {
		t.Errorf("re-declaring an entry already in place reported changed = %v", changed)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("no setfacl call was needed and one ran anyway: %v", cmds)
	}
}

func TestSetInsertsANewEntryWithDashMWhenNoneMatches(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclTrivialCapture},
		fmt.Sprintf("setfacl -m user:games:rw:%s:allow %s", "", path): {Code: 0},
	})
	args := aclArgs("name", path, "tag", "user", "qualifier", "games", "perms", "rw", "type", "allow")
	out, err := aclSetFn(c, args)
	if err != nil {
		t.Fatalf("aclSetFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != true {
		t.Fatalf("a genuinely new entry reported changed = %v", changed)
	}
	cmds := aclSetfaclCommands(c)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "-m") || strings.Contains(cmds[0], "-a") {
		t.Errorf("a new entry with no position should use -m, got %v", cmds)
	}
}

func TestSetUsesDashAWhenAPositionIsGivenForABrandNewEntry(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclTrivialCapture},
		fmt.Sprintf("setfacl -a 0 user:games:rw::allow %s", path): {Code: 0},
	})
	args := aclArgs("name", path, "tag", "user", "qualifier", "games", "perms", "rw", "type", "allow", "position", int64(0))
	out, err := aclSetFn(c, args)
	if err != nil {
		t.Fatalf("aclSetFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != true {
		t.Fatal("a new entry at a chosen position reported no change")
	}
	cmds := aclSetfaclCommands(c)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "-a 0") {
		t.Errorf("a position was given for a brand-new entry; want -a 0, got %v", cmds)
	}
}

// A position is only a hint for a brand-new entry. setfacl -m itself
// matches an existing (tag, qualifier, type) to update in place, and a
// position given alongside an entry that already exists must not move
// it — verified live: re-running -m on the same tag/qualifier updates
// the entry where it already sits rather than re-inserting it.
func TestSetIgnoresPositionWhenAnEntryAlreadyMatches(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclRealCapture},
		fmt.Sprintf("setfacl -m user:operator:rwx::allow %s", path): {Code: 0},
	})
	args := aclArgs("name", path, "tag", "user", "qualifier", "operator", "perms", "rwx", "type", "allow", "position", int64(3))
	if _, err := aclSetFn(c, args); err != nil {
		t.Fatalf("aclSetFn: %v", err)
	}
	cmds := aclSetfaclCommands(c)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "-m") {
		t.Errorf("an existing entry with a position given should still use -m, got %v", cmds)
	}
}

func TestSetInTestModeNeverRunsSetfacl(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclTrivialCapture},
	})
	c.Test = true
	args := aclArgs("name", path, "tag", "user", "qualifier", "games", "perms", "rw", "type", "allow")
	out, err := aclSetFn(c, args)
	if err != nil {
		t.Fatalf("aclSetFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != true {
		t.Error("test mode did not predict the change a real run would make")
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("test mode ran setfacl: %v", cmds)
	}
}

func TestRemoveTakesMatchingEntriesOutHighestPositionFirst(t *testing.T) {
	// Two entries for user:games (the allow at position 1 the real
	// capture does not have, added here to exercise more than one
	// match) plus the deny at position 0.
	capture := `# file: /home/ed/aclcapture/testfile
# owner: ed
# group: wheel
        user:games:-w------------:-------:deny
        user:games:r-------------:-------:allow
       group:wheel:r--p----------:-------:allow
            owner@:rw-p--aARWcCos:-------:allow
            group@:r-----a-R-c--s:-------:allow
         everyone@:r-----a-R-c--s:-------:allow
`
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path:      {Code: 0, Stdout: capture},
		"setfacl -x 1 " + path: {Code: 0},
		"setfacl -x 0 " + path: {Code: 0},
	})
	args := aclArgs("name", path, "tag", "user", "qualifier", "games")
	out, err := aclRemoveFn(c, args)
	if err != nil {
		t.Fatalf("aclRemoveFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != true {
		t.Fatal("removing two matching entries reported no change")
	}
	ran := c.Runner.(*exec.RecordingRunner).RanCommands()
	var order []string
	for _, r := range ran {
		if strings.HasPrefix(r, "setfacl -x") {
			order = append(order, r)
		}
	}
	if len(order) != 2 || !strings.Contains(order[0], "-x 1") || !strings.Contains(order[1], "-x 0") {
		t.Errorf("positions must be removed highest first so earlier ones do not shift; got %v", order)
	}
}

func TestRemoveReportsUnchangedWhenNothingMatches(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclTrivialCapture},
	})
	args := aclArgs("name", path, "tag", "user", "qualifier", "nobody")
	out, err := aclRemoveFn(c, args)
	if err != nil {
		t.Fatalf("aclRemoveFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != false {
		t.Error("removing an absent entry reported a change")
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("no setfacl call was needed and one ran anyway: %v", cmds)
	}
}

func TestRemoveOnlyTakesOutTheNamedType(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path:      {Code: 0, Stdout: aclRealCapture},
		"setfacl -x 0 " + path: {Code: 0},
	})
	// aclRealCapture has games:deny at 0 and operator:allow at 1; asking
	// to remove only games's deny must not touch operator's allow.
	args := aclArgs("name", path, "tag", "user", "qualifier", "games", "type", "deny")
	out, err := aclRemoveFn(c, args)
	if err != nil {
		t.Fatalf("aclRemoveFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != true {
		t.Fatal("removing the matching type reported no change")
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || !strings.Contains(cmds[0], "-x 0") {
		t.Errorf("want exactly one -x 0, got %v", cmds)
	}
}

func TestWipeIsIdempotentOnAnAlreadyTrivialACL(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl -sq " + path: {Code: 0, Stdout: ""},
	})
	out, err := aclWipeFn(c, aclArgs("name", path))
	if err != nil {
		t.Fatalf("aclWipeFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != false {
		t.Error("wiping an already-trivial ACL reported a change")
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 0 {
		t.Errorf("no setfacl -b call was needed and one ran anyway: %v", cmds)
	}
}

func TestWipeRunsSetfaclDashBOnAnExtendedACL(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl -sq " + path: {Code: 0, Stdout: aclRealCapture},
		"setfacl -b " + path:  {Code: 0},
	})
	out, err := aclWipeFn(c, aclArgs("name", path))
	if err != nil {
		t.Fatalf("aclWipeFn: %v", err)
	}
	changed, _ := out.(*value.Map).GetString("changed")
	if changed != true {
		t.Fatal("wiping an extended ACL reported no change")
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || cmds[0] != "setfacl -b "+path {
		t.Errorf("want exactly `setfacl -b %s`, got %v", path, cmds)
	}
}

func TestWipeAddsDashRWhenRecursive(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl -sq " + path:   {Code: 0, Stdout: aclRealCapture},
		"setfacl -R -b " + path: {Code: 0},
	})
	if _, err := aclWipeFn(c, aclArgs("name", path, "recursive", true)); err != nil {
		t.Fatalf("aclWipeFn: %v", err)
	}
	if cmds := aclSetfaclCommands(c); len(cmds) != 1 || !strings.Contains(cmds[0], "-R") {
		t.Errorf("recursive=true should add -R, got %v", cmds)
	}
}

func TestGetReturnsOwnerGroupAndEveryEntry(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl " + path: {Code: 0, Stdout: aclRealCapture},
	})
	out, err := aclGetFn(c, aclArgs("name", path))
	if err != nil {
		t.Fatalf("aclGetFn: %v", err)
	}
	m := out.(*value.Map)
	if owner, _ := m.GetString("owner"); owner != "ed" {
		t.Errorf("owner = %v, want ed", owner)
	}
	entries, _ := m.GetString("entries")
	if got := len(entries.([]any)); got != 6 {
		t.Errorf("got %d entries, want 6", got)
	}
}

func TestIsExtendedReadsAnEmptyAnswerAsTrivial(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl -sq " + path: {Code: 0, Stdout: "\n"},
	})
	out, err := aclIsExtendedFn(c, aclArgs("name", path))
	if err != nil {
		t.Fatalf("aclIsExtendedFn: %v", err)
	}
	if out != false {
		t.Errorf("a trivial ACL was reported extended: %v", out)
	}
}

func TestIsExtendedReadsAnyOutputAsExtended(t *testing.T) {
	path := "/home/ed/aclcapture/testfile"
	c := aclContext(map[string]exec.Result{
		"getfacl -sq " + path: {Code: 0, Stdout: aclRealCapture},
	})
	out, err := aclIsExtendedFn(c, aclArgs("name", path))
	if err != nil {
		t.Fatalf("aclIsExtendedFn: %v", err)
	}
	if out != true {
		t.Errorf("an extended ACL was reported trivial: %v", out)
	}
}

func TestAMissingGetfaclIsReportedByName(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := aclGetFn(c, aclArgs("name", "/x")); err == nil || !strings.Contains(err.Error(), "getfacl") {
		t.Errorf("a missing getfacl was not reported by name: %v", err)
	}
}

func TestSignaturesAreScopedToFreeBSDAndCarrySection15Point2(t *testing.T) {
	r := &Registries{Exec: exec.NewRegistry(), States: states.NewRegistry()}
	registerACL(r)
	for _, name := range []string{"acl.get", "acl.is_extended", "acl.set", "acl.remove", "acl.wipe"} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Fatalf("%s was not registered", name)
		}
		if sig.Section != "15.2" {
			t.Errorf("%s.Section = %q, want 15.2", name, sig.Section)
		}
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "freebsd" {
			t.Errorf("%s.Platforms = %v, want [freebsd]", name, sig.Platforms)
		}
	}
	for _, name := range []string{"acl.set", "acl.remove", "acl.wipe"} {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if !sig.Mutates {
			t.Errorf("%s.Mutates = false, want true", name)
		}
	}
	for _, name := range []string{"acl.get", "acl.is_extended"} {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if sig.Mutates {
			t.Errorf("%s.Mutates = true; it only reads", name)
		}
	}
}
