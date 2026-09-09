package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A `dscl -plist . -read /Users/<name>` record, captured from a real Mac.
func macUserPlist(name, uid, gid, home, shell, real string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>dsAttrTypeStandard:NFSHomeDirectory</key>
	<array><string>` + home + `</string></array>
	<key>dsAttrTypeStandard:PrimaryGroupID</key>
	<array><string>` + gid + `</string></array>
	<key>dsAttrTypeStandard:RealName</key>
	<array><string>` + real + `</string></array>
	<key>dsAttrTypeStandard:UniqueID</key>
	<array><string>` + uid + `</string></array>
	<key>dsAttrTypeStandard:UserShell</key>
	<array><string>` + shell + `</string></array>
</dict>
</plist>
`
}

func macGroupPlist(gid string, members ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>dsAttrTypeStandard:PrimaryGroupID</key>
	<array><string>` + gid + `</string></array>
`)
	if len(members) > 0 {
		b.WriteString("\t<key>dsAttrTypeStandard:GroupMembership</key>\n\t<array>")
		for _, m := range members {
			b.WriteString("<string>" + m + "</string>")
		}
		b.WriteString("</array>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// notFound is dscl's answer for a record that is not there.
var dsclNotFound = exec.Result{Code: 56, Stderr: "<dscl_cmd> DS Error: -14136 (eDSRecordNotFound)\n"}

// ranArgv reports whether the recorder saw a command whose argument
// vector exactly matches want. It compares the vectors, not the rendered
// string, so an argument with a space in it is not a quoting question.
func ranArgv(runner *exec.RecordingRunner, want ...string) bool {
	for _, cmd := range runner.Ran {
		if len(cmd.Argv) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if cmd.Argv[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func ranPrefix(runner *exec.RecordingRunner, prefix ...string) bool {
	for _, cmd := range runner.Ran {
		if len(cmd.Argv) < len(prefix) {
			continue
		}
		match := true
		for i := range prefix {
			if cmd.Argv[i] != prefix[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func macAccountCtx(t *testing.T, responses map[string]exec.Result) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	runner := &exec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		switch name {
		case "dscl", "dseditgroup", "createhomedir", "rm":
			return "/usr/bin/" + name
		}
		return ""
	}
	return c, runner
}

func TestDsclReadParsesARecordAndReportsMissing(t *testing.T) {
	c, _ := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/alice": {Stdout: macUserPlist("alice", "501", "20", "/Users/alice", "/bin/zsh", "Alice Q")},
		"dscl -plist . -read /Users/ghost": dsclNotFound,
	})

	rec, ok, err := dsclRead(c, "/Users/alice")
	if err != nil || !ok {
		t.Fatalf("reading alice: ok=%v err=%v", ok, err)
	}
	if dsFirst(rec, "UniqueID") != "501" || dsFirst(rec, "RealName") != "Alice Q" || dsFirst(rec, "UserShell") != "/bin/zsh" {
		t.Errorf("alice read as %v", rec)
	}

	_, ok, err = dsclRead(c, "/Users/ghost")
	if err != nil {
		t.Fatalf("reading a missing record errored: %v", err)
	}
	if ok {
		t.Error("a missing record reported as present")
	}
}

func TestMacUserInfoShape(t *testing.T) {
	c, _ := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/alice": {Stdout: macUserPlist("alice", "501", "20", "/Users/alice", "/bin/zsh", "Alice Q")},
	})
	m, err := macUserInfo(c, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"name": "alice", "uid": int64(501), "gid": int64(20),
		"home": "/Users/alice", "shell": "/bin/zsh", "fullname": "Alice Q",
	} {
		if v, _ := m.Get(k); v != want {
			t.Errorf("info[%q] = %#v, want %#v", k, v, want)
		}
	}
}

func TestMacGroupInfoShape(t *testing.T) {
	c, _ := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Groups/devs": {Stdout: macGroupPlist("700", "alice", "bob")},
	})
	m, err := macGroupInfo(c, "devs")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := m.Get("gid"); v != int64(700) {
		t.Errorf("gid = %#v", v)
	}
	mem, _ := m.Get("members")
	list, ok := mem.([]any)
	if !ok || len(list) != 2 || list[0] != "alice" || list[1] != "bob" {
		t.Errorf("members = %#v", mem)
	}
}

func TestMacUserPresentCreatesWhenAbsent(t *testing.T) {
	c, runner := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/newbie": dsclNotFound,
		"dscl . -list /Users UniqueID":      {Stdout: "root 0\nalice 501\n"},
	})
	res, err := macUserPresentState(c, value.MapOf(
		"name", "newbie", "fullname", "New Bie", "shell", "/bin/bash"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("create reported %+v", res)
	}
	for _, want := range [][]string{
		{"dscl", ".", "-create", "/Users/newbie", "RealName", "New Bie"},
		{"dscl", ".", "-create", "/Users/newbie", "UniqueID", "502"},
		{"dscl", ".", "-create", "/Users/newbie", "PrimaryGroupID", "20"},
		{"dscl", ".", "-create", "/Users/newbie", "UserShell", "/bin/bash"},
		{"dscl", ".", "-create", "/Users/newbie", "NFSHomeDirectory", "/Users/newbie"},
	} {
		if !ranArgv(runner, want...) {
			t.Errorf("create did not run %v\nran:\n%s", want, strings.Join(runner.RanCommands(), "\n"))
		}
	}
}

func TestMacUserPresentConvergedAndDiff(t *testing.T) {
	rec := macUserPlist("alice", "501", "20", "/Users/alice", "/bin/zsh", "Alice Q")
	base := map[string]exec.Result{"dscl -plist . -read /Users/alice": {Stdout: rec}}

	// Converged: same shell, no change, no dscl -create.
	c, runner := macAccountCtx(t, base)
	got, err := macUserPresentState(c, value.MapOf("name", "alice", "shell", "/bin/zsh"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Succeeded() || got.HasChanges() {
		t.Errorf("converged reported %+v", got)
	}
	if ranPrefix(runner, "dscl", ".", "-create") {
		t.Error("converged state still ran a dscl -create")
	}

	// Different shell: one change, one create.
	c, runner = macAccountCtx(t, base)
	got, err = macUserPresentState(c, value.MapOf("name", "alice", "shell", "/bin/bash"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasChanges() {
		t.Fatalf("a shell change was not seen: %+v", got)
	}
	if !ranArgv(runner, "dscl", ".", "-create", "/Users/alice", "UserShell", "/bin/bash") {
		t.Errorf("the shell was not written: %v", runner.RanCommands())
	}
}

func TestMacUserPresentTestModeWritesNothing(t *testing.T) {
	c, runner := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/alice": dsclNotFound,
		"dscl . -list /Users UniqueID":     {Stdout: "root 0\n"},
	})
	c.Test = true
	res, err := macUserPresentState(c, value.MapOf("name", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode returned a decided result: %+v", res)
	}
	if ranPrefix(runner, "dscl", ".", "-create") {
		t.Error("test mode ran a dscl -create")
	}
}

func TestMacUserPresentRefusesAPasswordHash(t *testing.T) {
	c, _ := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/alice": dsclNotFound,
	})
	res, err := macUserPresentState(c, value.MapOf("name", "alice", "password", "$6$abc$def"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Errorf("a password hash was accepted on macOS: %+v", res)
	}
	if !strings.Contains(res.Comment, "mac_shadow.set_password") {
		t.Errorf("the refusal does not point at mac_shadow: %q", res.Comment)
	}
}

func TestMacUserAbsentState(t *testing.T) {
	c, _ := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/ghost": dsclNotFound,
	})
	gone, err := macUserAbsentState(c, value.MapOf("name", "ghost"))
	if err != nil {
		t.Fatal(err)
	}
	if !gone.Succeeded() || gone.HasChanges() {
		t.Errorf("absent on a missing account reported %+v", gone)
	}

	c, runner := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Users/alice": {Stdout: macUserPlist("alice", "501", "20", "/Users/alice", "/bin/zsh", "Alice")},
	})
	hit, err := macUserAbsentState(c, value.MapOf("name", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Succeeded() || !hit.HasChanges() {
		t.Fatalf("absent on a present account reported %+v", hit)
	}
	if !ranArgv(runner, "dscl", ".", "-delete", "/Users/alice") {
		t.Errorf("absent did not run the delete: %v", runner.RanCommands())
	}
}

func TestMacGroupPresentAndAbsent(t *testing.T) {
	c, runner := macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Groups/devs": dsclNotFound,
	})
	res, err := macGroupPresentState(c, value.MapOf("name", "devs", "gid", int64(700)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("group create reported %+v", res)
	}
	if !ranArgv(runner, "dseditgroup", "-o", "create", "-i", "700", "devs") {
		t.Errorf("group create ran %v", runner.RanCommands())
	}

	// Existing group, different gid: refused, not renumbered.
	c, _ = macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Groups/devs": {Stdout: macGroupPlist("700")},
	})
	bad, err := macGroupPresentState(c, value.MapOf("name", "devs", "gid", int64(800)))
	if err != nil {
		t.Fatal(err)
	}
	if bad.Succeeded() {
		t.Errorf("a gid change was accepted: %+v", bad)
	}

	// Absent.
	c, runner = macAccountCtx(t, map[string]exec.Result{
		"dscl -plist . -read /Groups/devs": {Stdout: macGroupPlist("700")},
	})
	rm, err := macGroupAbsentState(c, value.MapOf("name", "devs"))
	if err != nil {
		t.Fatal(err)
	}
	if !rm.HasChanges() || !ranArgv(runner, "dseditgroup", "-o", "delete", "devs") {
		t.Errorf("group absent: %+v / %v", rm, runner.RanCommands())
	}
}

func TestMacUserSetGroupsConverges(t *testing.T) {
	// alice is in staff and admin; wants dev and staff.
	search := exec.Result{Stdout: "staff\t\tGroupMembership\nadmin\t\tGroupMembership\n"}
	c, runner := macAccountCtx(t, map[string]exec.Result{
		"dscl . -search /Groups GroupMembership alice": search,
	})
	if err := macUserSetGroups(c, "alice", []string{"dev", "staff"}, false); err != nil {
		t.Fatal(err)
	}
	if !ranArgv(runner, "dseditgroup", "-o", "edit", "-a", "alice", "-t", "user", "dev") {
		t.Errorf("dev was not added: %v", runner.RanCommands())
	}
	if !ranArgv(runner, "dseditgroup", "-o", "edit", "-d", "alice", "-t", "user", "admin") {
		t.Errorf("admin was not removed: %v", runner.RanCommands())
	}
	if ranArgv(runner, "dseditgroup", "-o", "edit", "-a", "alice", "-t", "user", "staff") {
		t.Errorf("staff was re-added though alice is already in it: %v", runner.RanCommands())
	}

	// append: never removes.
	c, runner = macAccountCtx(t, map[string]exec.Result{
		"dscl . -search /Groups GroupMembership alice": search,
	})
	if err := macUserSetGroups(c, "alice", []string{"dev"}, true); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range runner.Ran {
		for _, a := range cmd.Argv {
			if a == "-d" {
				t.Errorf("append removed a group: %v", runner.RanCommands())
			}
		}
	}
}

func TestMacSafeHomeGuardsTheDelete(t *testing.T) {
	for _, tc := range []struct {
		home string
		ok   bool
	}{
		{"/Users/alice", true},
		{"/Users/alice/", true},
		{"/private/var/empty", true},
		{"/Users", false},
		{"/", false},
		{"", false},
		{"/etc", false},
		{"/Users/", false},
	} {
		if got := macSafeHome(tc.home); got != tc.ok {
			t.Errorf("macSafeHome(%q) = %v, want %v", tc.home, got, tc.ok)
		}
	}
}

func TestMacAccountModulesAreRegisteredAndRestricted(t *testing.T) {
	r := New()
	want := map[string]bool{
		"mac_user.add": false, "mac_user.delete": false, "mac_user.info": false,
		"mac_user.list_users": false, "mac_user.chuid": false, "mac_user.chgid": false,
		"mac_user.chshell": false, "mac_user.chhome": false, "mac_user.chfullname": false,
		"mac_user.chgroups": false,
		"mac_group.add":     false, "mac_group.delete": false, "mac_group.info": false,
		"mac_group.list_groups": false, "mac_group.members": false, "mac_group.chgid": false,
		"mac_group.adduser": false, "mac_group.deluser": false,
		"mac_shadow.info": false, "mac_shadow.set_password": false, "mac_shadow.del_password": false,
	}
	for name := range want {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		want[name] = true
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "darwin" {
			t.Errorf("%s platforms = %v, want [darwin]", name, sig.Platforms)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%s missing", name)
		}
	}
}
