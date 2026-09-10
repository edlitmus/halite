package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The fixtures below are real files, not written from memory.
//
// `freebsdSu` and `freebsdSystem` are /etc/pam.d/su and /etc/pam.d/system
// as FreeBSD 15 ships them, copied off the host this project is
// developed on, tabs and commented-out krb5 lines included.
// `debianCommonAuth` and `debianSshd` are Debian 12's, which matter here
// because they use the *other* include mechanism and the bracketed
// control flag that FreeBSD's files have no example of.
//
// This is the discipline plan.md §1.4 and DIVERGENCE 5.31 were both
// written about: a fixture in the module's own spelling tests the
// module's own spelling. Every hazard asserted below is one a real file
// contains.

const freebsdSu = `#
#
# PAM configuration for the "su" service
#

# auth
auth		sufficient	pam_rootok.so		no_warn
auth		sufficient	pam_self.so		no_warn
auth		requisite	pam_group.so		no_warn group=wheel root_only fail_safe ruser
auth		include		system

# account
account		include		system

# session
session		required	pam_permit.so
`

const freebsdSystem = `#
#
# System-wide defaults
#

# auth
#auth		sufficient	pam_krb5.so		no_warn try_first_pass
auth		required	pam_unix.so		no_warn try_first_pass nullok

# account
#account	required	pam_krb5.so
account		required	pam_login_access.so
account		required	pam_unix.so

# session
session		required	pam_lastlog.so		no_fail
session         required        pam_xdg.so

# password
password	required	pam_unix.so		no_warn try_first_pass
`

const debianCommonAuth = `# here are the per-package modules (the "Primary" block)
auth	[success=1 default=ignore]	pam_unix.so nullok
# here's the fallback if no module succeeds
auth	requisite			pam_deny.so
# prime the stack with a positive return value if there isn't one already;
auth	required			pam_permit.so
`

const debianSshd = `# PAM configuration for the Secure Shell service
@include common-auth
account    required     pam_nologin.so
@include common-account
session [success=ok ignore=ignore module_unknown=ignore default=bad]        pam_selinux.so close
session    required     pam_loginuid.so
@include common-session
@include common-password
`

// pamTree lays out a throwaway /etc/pam.d and points the module at it.
func pamTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := PamDir
	PamDir = dir
	t.Cleanup(func() { PamDir = old })
	return dir
}

// Both helpers go through the registry rather than reaching for the
// function, so that the declared signature — its defaults, its types and
// its refusals — is exercised by every test in this file.
func pamCall(t *testing.T, fn string, args *value.Map, test bool) any {
	t.Helper()
	out, err := pamTry(fn, args, test)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func pamCallErr(t *testing.T, fn string, args *value.Map, test bool) error {
	t.Helper()
	_, err := pamTry(fn, args, test)
	return err
}

func pamTry(fn string, args *value.Map, test bool) (any, error) {
	r := New()
	return r.Exec.Call(&exec.Context{Test: test}, fn, args)
}

// modules renders a rule list as "type:module" so an assertion reads as
// the chain it is about.
func pamModules(t *testing.T, out any) []string {
	t.Helper()
	list, ok := out.([]any)
	if !ok {
		t.Fatalf("expected a list of rules, got %T", out)
	}
	names := make([]string, 0, len(list))
	for _, r := range list {
		m, ok := r.(*value.Map)
		if !ok {
			t.Fatalf("expected a rule map, got %T", r)
		}
		typ, _ := m.GetString("type")
		mod, _ := m.GetString("module")
		names = append(names, typ.(string)+":"+mod.(string))
	}
	return names
}

// A typed include pulls in one chain, not the whole of the service it
// names.
//
// This is the assertion the module exists for. FreeBSD's `su` has three
// auth rules of its own and then `auth include system`; `system` also
// has an account chain, a session chain and a password chain. A resolver
// that spliced the whole file in would report `su` as running
// pam_lastlog at session time, which it does not, and an operator
// reading that would go looking for a login record that is never
// written.
func TestATypedIncludePullsInOnlyItsOwnChain(t *testing.T) {
	pamTree(t, map[string]string{"su": freebsdSu, "system": freebsdSystem})

	args := value.NewMap(1)
	args.Set("service", "su")
	got := pamModules(t, pamCall(t, "pam.rules", args, false))

	want := []string{
		"auth:pam_rootok.so",
		"auth:pam_self.so",
		"auth:pam_group.so",
		"auth:pam_unix.so", // from system's auth chain, and only that chain
		"account:pam_login_access.so",
		"account:pam_unix.so",
		"session:pam_permit.so",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("su resolves to\n  %v\nwant\n  %v", got, want)
	}
}

// Debian's `@include` has no type and pulls in every chain of the file
// it names.
//
// The two mechanisms are not spellings of each other, and this is the
// half a reader written on FreeBSD would get wrong: `@include
// common-auth` contributes auth rules to sshd because common-auth
// happens to contain only auth rules, not because the include was typed.
func TestAnAtIncludePullsInEveryChainOfTheFileItNames(t *testing.T) {
	pamTree(t, map[string]string{"sshd": debianSshd, "common-auth": debianCommonAuth})

	args := value.NewMap(1)
	args.Set("service", "sshd")
	got := pamModules(t, pamCall(t, "pam.rules", args, false))

	// common-account, common-session and common-password are absent from
	// the tree on purpose: an include naming a service that is not there
	// is kept as written rather than dropped, which is the next
	// assertion, and here it keeps this list to what can be resolved.
	want := []string{
		"auth:pam_unix.so",
		"auth:pam_deny.so",
		"auth:pam_permit.so",
		"account:pam_nologin.so",
		":common-account",
		"session:pam_selinux.so",
		"session:pam_loginuid.so",
		":common-session",
		":common-password",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("sshd resolves to\n  %v\nwant\n  %v", got, want)
	}
}

// A bracketed control flag stays one field.
//
// `[success=1 default=ignore]` contains a space, so splitting the line on
// whitespace files `default=ignore]` as the module path — and the module
// path is what every other function in this module matches on. The rule
// would then be invisible to `has_module` and to `services_using`, which
// is the wrong answer to "is pam_unix in Debian's auth chain": it is the
// only thing in it.
func TestABracketedControlFlagIsNotTornIntoFields(t *testing.T) {
	pamTree(t, map[string]string{"common-auth": debianCommonAuth})

	args := value.NewMap(1)
	args.Set("service", "common-auth")
	list := pamCall(t, "pam.rules", args, false).([]any)
	first := list[0].(*value.Map)

	control, _ := first.GetString("control")
	if control != "[success=1 default=ignore]" {
		t.Errorf("control flag read as %q, want the whole bracketed form", control)
	}
	module, _ := first.GetString("module")
	if module != "pam_unix.so" {
		t.Errorf("module read as %q, want pam_unix.so", module)
	}
	args2 := value.NewMap(2)
	args2.Set("service", "common-auth")
	args2.Set("module", "pam_unix")
	if has := pamCall(t, "pam.has_module", args2, false); has != true {
		t.Error("pam_unix.so is the whole of Debian's auth chain and has_module says it is absent")
	}
}

// The four-key bracketed form Debian uses on a session line parses too,
// and its arguments survive.
func TestALongBracketedFlagKeepsTheModuleArguments(t *testing.T) {
	pamTree(t, map[string]string{"sshd": debianSshd})

	args := value.NewMap(2)
	args.Set("service", "sshd")
	args.Set("resolve_includes", false)
	list := pamCall(t, "pam.rules", args, false).([]any)

	var found bool
	for _, r := range list {
		m := r.(*value.Map)
		mod, _ := m.GetString("module")
		if mod != "pam_selinux.so" {
			continue
		}
		found = true
		control, _ := m.GetString("control")
		if !strings.HasPrefix(control.(string), "[") || !strings.HasSuffix(control.(string), "]") {
			t.Errorf("pam_selinux control read as %q", control)
		}
		list, _ := m.GetString("args")
		if got := list.([]any); len(got) != 1 || got[0] != "close" {
			t.Errorf("pam_selinux arguments read as %v, want [close]", got)
		}
	}
	if !found {
		t.Error("the pam_selinux rule was not parsed at all")
	}
}

// A line continued with a backslash is one rule, and it reports the line
// it started on.
//
// The line number is what `pam.rules` gives an operator to edit, so a
// continuation that shifted every later number by one would send them to
// the wrong line in a file they are changing under pressure.
func TestABackslashContinuesALine(t *testing.T) {
	pamTree(t, map[string]string{"svc": "" +
		"auth\trequired\tpam_unix.so\tnullok \\\n" +
		"\ttry_first_pass\n" +
		"account\trequired\tpam_unix.so\n"})

	args := value.NewMap(1)
	args.Set("service", "svc")
	list := pamCall(t, "pam.rules", args, false).([]any)
	if len(list) != 2 {
		t.Fatalf("a continued line parsed as %d rules, want 2", len(list))
	}
	first := list[0].(*value.Map)
	got, _ := first.GetString("args")
	if a := got.([]any); len(a) != 2 || a[0] != "nullok" || a[1] != "try_first_pass" {
		t.Errorf("continued arguments read as %v, want [nullok try_first_pass]", a)
	}
	second := list[1].(*value.Map)
	line, _ := second.GetString("line")
	if line != int64(3) {
		t.Errorf("the rule after a continuation is reported at line %v, want 3", line)
	}
}

// A service that includes itself is answered, not hung on.
func TestAnIncludeCycleTerminates(t *testing.T) {
	pamTree(t, map[string]string{
		"a": "auth\tinclude\tb\n",
		"b": "auth\tinclude\ta\nauth\trequired\tpam_deny.so\n",
	})

	args := value.NewMap(1)
	args.Set("service", "a")
	got := pamModules(t, pamCall(t, "pam.rules", args, false))
	// The cycle's own rule is kept as written rather than dropped, so
	// the answer still shows where the loop is.
	want := []string{"auth:a", "auth:pam_deny.so"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("a cyclic include resolves to %v, want %v", got, want)
	}
}

// The sweep finds a module that only ever arrives through an include.
//
// This is the case `grep -r pam_unix /etc/pam.d` gets wrong in both
// directions: it misses `su`, whose own file does not name pam_unix at
// all, and it would count the commented-out pam_krb5 lines that every
// stock FreeBSD file carries.
func TestTheSweepFindsAModuleReachedOnlyThroughAnInclude(t *testing.T) {
	pamTree(t, map[string]string{"su": freebsdSu, "system": freebsdSystem})

	args := value.NewMap(1)
	args.Set("module", "pam_unix")
	out := pamCall(t, "pam.services_using", args, false).(*value.Map)

	if _, ok := out.GetString("su"); !ok {
		t.Error("su reaches pam_unix through `auth include system` and the sweep missed it")
	}
	if _, ok := out.GetString("system"); !ok {
		t.Error("system names pam_unix directly and the sweep missed it")
	}

	krb := value.NewMap(1)
	krb.Set("module", "pam_krb5")
	if got := pamCall(t, "pam.services_using", krb, false).(*value.Map); got.Len() != 0 {
		t.Errorf("pam_krb5 is commented out in every file and the sweep found it in %v", got.SortedKeys())
	}
}

// A new rule goes into its own chain rather than at the end of the file.
//
// FreeBSD's `su` ends with its session chain. A rule appended to the file
// would be an auth rule sitting after the session rules, which PAM
// evaluates correctly and no operator reading the file would expect —
// and the next person to edit it by hand would put theirs in the auth
// block, leaving two auth rules in two places.
func TestANewRuleIsPlacedWithinItsOwnChain(t *testing.T) {
	dir := pamTree(t, map[string]string{"su": freebsdSu, "system": freebsdSystem})

	args := value.NewMap(5)
	args.Set("service", "su")
	args.Set("type", "auth")
	args.Set("module", "pam_faillock.so")
	args.Set("control", "required")
	args.Set("position", "end")
	pamCall(t, "pam.set_module", args, false)

	body, err := os.ReadFile(filepath.Join(dir, "su"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	var faillock, lastAuth, firstAccount int
	for i, l := range lines {
		switch {
		case strings.Contains(l, "pam_faillock.so"):
			faillock = i
		case strings.HasPrefix(l, "auth\t"):
			lastAuth = i
		case strings.HasPrefix(l, "account\t") && firstAccount == 0:
			firstAccount = i
		}
	}
	if faillock == 0 {
		t.Fatal("the rule was not written at all")
	}
	if faillock < lastAuth || faillock > firstAccount {
		t.Errorf("pam_faillock landed on line %d; the auth chain ends at %d and the account chain begins at %d",
			faillock, lastAuth, firstAccount)
	}
}

// Setting a module that is already there in the same spelling changes
// nothing, which is what makes this callable from a tree that runs
// nightly.
func TestSettingARuleThatIsAlreadyThereChangesNothing(t *testing.T) {
	dir := pamTree(t, map[string]string{"system": freebsdSystem})
	before, err := os.ReadFile(filepath.Join(dir, "system"))
	if err != nil {
		t.Fatal(err)
	}

	args := value.NewMap(5)
	args.Set("service", "system")
	args.Set("type", "account")
	args.Set("module", "pam_login_access.so")
	args.Set("control", "required")
	out := pamCall(t, "pam.set_module", args, false).(*value.Map)

	changed, _ := out.GetString("changed")
	if changed != false {
		t.Error("a rule that is already written was reported as a change")
	}
	after, err := os.ReadFile(filepath.Join(dir, "system"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the file was rewritten for a rule that was already there")
	}
}

// Changing an existing rule keeps its position.
//
// Position is the whole meaning of a PAM rule. A change that appended
// the new spelling and left the old one would run both, and one that
// moved pam_deny.so to the front of a chain would deny every request the
// service handles.
func TestChangingARuleKeepsItsPositionInTheChain(t *testing.T) {
	dir := pamTree(t, map[string]string{"common-auth": debianCommonAuth})

	args := value.NewMap(4)
	args.Set("service", "common-auth")
	args.Set("type", "auth")
	args.Set("module", "pam_deny.so")
	args.Set("control", "required") // was requisite
	pamCall(t, "pam.set_module", args, false)

	body, err := os.ReadFile(filepath.Join(dir, "common-auth"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "pam_deny.so"); n != 1 {
		t.Fatalf("pam_deny.so appears %d times after a change, want 1", n)
	}
	lines := strings.Split(string(body), "\n")
	var deny, permit int
	for i, l := range lines {
		if strings.Contains(l, "pam_deny.so") {
			deny = i
		}
		if strings.Contains(l, "pam_permit.so") {
			permit = i
		}
	}
	if deny > permit {
		t.Errorf("pam_deny.so moved below pam_permit.so, which makes the chain always succeed")
	}
	if !strings.Contains(string(body), "auth\trequired\tpam_deny.so") {
		t.Errorf("the control flag was not changed; the file reads:\n%s", body)
	}
}

// Removing the last rule of a chain is refused.
//
// An empty chain is not "no policy": PAM fails a service whose chain has
// no modules, so this is the call that locks a node out, and it is the
// one thing in the module that refuses rather than reporting.
func TestRemovingTheLastRuleOfAChainIsRefused(t *testing.T) {
	dir := pamTree(t, map[string]string{"svc": "auth\trequired\tpam_unix.so\naccount\trequired\tpam_unix.so\n"})

	args := value.NewMap(3)
	args.Set("service", "svc")
	args.Set("type", "auth")
	args.Set("module", "pam_unix.so")
	err := pamCallErr(t, "pam.remove_module", args, false)
	if err == nil {
		t.Fatal("emptying a service's auth chain was allowed")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "svc"))
	if !strings.Contains(string(body), "pam_unix.so") {
		t.Error("the rule was removed despite the refusal")
	}
}

// Test mode predicts and writes nothing, which SPEC 11.6 requires of
// every function that declares TestReliable.
func TestPamEditsInTestModeWriteNothing(t *testing.T) {
	dir := pamTree(t, map[string]string{"su": freebsdSu, "system": freebsdSystem})
	before, err := os.ReadFile(filepath.Join(dir, "su"))
	if err != nil {
		t.Fatal(err)
	}

	args := value.NewMap(4)
	args.Set("service", "su")
	args.Set("type", "auth")
	args.Set("module", "pam_faillock.so")
	args.Set("control", "required")
	out := pamCall(t, "pam.set_module", args, true).(*value.Map)

	changed, _ := out.GetString("changed")
	if changed != true {
		t.Error("test mode did not predict the change it would make")
	}
	after, err := os.ReadFile(filepath.Join(dir, "su"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a test run wrote to the file")
	}
}

// An edit takes a service, not a path.
//
// Without this a caller could reach any file on the node through an
// argument that reads like a service name, and the module would rewrite
// it in PAM's format.
func TestAnEditRefusesAPath(t *testing.T) {
	pamTree(t, map[string]string{"svc": "auth\trequired\tpam_unix.so\n"})

	args := value.NewMap(4)
	args.Set("service", "../../etc/passwd")
	args.Set("type", "auth")
	args.Set("module", "pam_unix.so")
	args.Set("control", "required")
	err := pamCallErr(t, "pam.set_module", args, false)
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("a path was accepted as a service name: %v", err)
	}
}

// An unclosed bracket is a line PAM will not act on, so it is not
// reported as a rule.
func TestAnUnclosedBracketIsNotReportedAsARule(t *testing.T) {
	pamTree(t, map[string]string{"svc": "auth\t[success=1 default=ignore\tpam_unix.so\nauth\trequired\tpam_deny.so\n"})

	args := value.NewMap(1)
	args.Set("service", "svc")
	got := pamModules(t, pamCall(t, "pam.rules", args, false))
	if len(got) != 1 || got[0] != "auth:pam_deny.so" {
		t.Errorf("a malformed line was read as %v, want only the pam_deny rule", got)
	}
}

// A chain filter answers about one chain.
func TestAChainFilterNarrowsTheAnswer(t *testing.T) {
	pamTree(t, map[string]string{"su": freebsdSu, "system": freebsdSystem})

	args := value.NewMap(2)
	args.Set("service", "su")
	args.Set("type", "account")
	got := pamModules(t, pamCall(t, "pam.rules", args, false))
	want := []string{"account:pam_login_access.so", "account:pam_unix.so"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("su's account chain is %v, want %v", got, want)
	}
}

// A chain that is not one of the four is refused rather than answered
// with an empty list, which would read as "there are no rules".
func TestAnUnknownChainIsRefused(t *testing.T) {
	pamTree(t, map[string]string{"su": freebsdSu})

	args := value.NewMap(2)
	args.Set("service", "su")
	args.Set("type", "sessions")
	if err := pamCallErr(t, "pam.rules", args, false); err == nil {
		t.Error("`sessions` was accepted as a PAM chain")
	}
}
