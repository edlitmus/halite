package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A domain exported by `defaults export`, with one key of each type this
// module reads. Captured from real `defaults` on macOS 26.
const macDefaultsExportFixture = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AutoHide</key>
	<true/>
	<key>Magnification</key>
	<false/>
	<key>tilesize</key>
	<integer>48</integer>
	<key>largesize</key>
	<real>64.5</real>
	<key>orientation</key>
	<string>left</string>
	<key>persistent-apps</key>
	<array>
		<string>Safari</string>
		<string>Mail</string>
	</array>
	<key>size-immutable</key>
	<dict>
		<key>locked</key>
		<true/>
	</dict>
</dict>
</plist>
`

func macDefaultsCtx(t *testing.T, exports map[string]string) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	responses := map[string]exec.Result{}
	for domain, xml := range exports {
		responses["defaults export "+domain+" -"] = exec.Result{Stdout: xml}
	}
	runner := &exec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "defaults" {
			return "/usr/bin/defaults"
		}
		return ""
	}
	return c, runner
}

// The plist reader keeps every scalar's type, and nests.
func TestMacDefaultsPlistReaderKeepsTypes(t *testing.T) {
	v, err := parsePlist([]byte(macDefaultsExportFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, ok := v.(*value.Map)
	if !ok {
		t.Fatalf("top level is %T, want *value.Map", v)
	}

	check := func(key string, want any) {
		got, _ := m.Get(key)
		if got != want {
			t.Errorf("%s read as %#v (%T), want %#v", key, got, got, want)
		}
	}
	check("AutoHide", true)
	check("Magnification", false)
	check("tilesize", int64(48))
	check("largesize", 64.5)
	check("orientation", "left")

	apps, _ := m.Get("persistent-apps")
	list, ok := apps.([]any)
	if !ok || len(list) != 2 || list[0] != "Safari" || list[1] != "Mail" {
		t.Errorf("persistent-apps read as %#v", apps)
	}

	nested, _ := m.Get("size-immutable")
	nm, ok := nested.(*value.Map)
	if !ok {
		t.Fatalf("size-immutable read as %T", nested)
	}
	if locked, _ := nm.Get("locked"); locked != true {
		t.Errorf("size-immutable.locked read as %#v", locked)
	}
}

// An empty domain — what `defaults export` prints for a domain that does
// not exist — parses to an empty mapping, not an error.
func TestMacDefaultsPlistReaderEmptyDomain(t *testing.T) {
	for _, in := range []string{
		`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict/></plist>`,
		`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
</dict></plist>`,
	} {
		v, err := parsePlist([]byte(in))
		if err != nil {
			t.Fatalf("parse %q: %v", in, err)
		}
		m, ok := v.(*value.Map)
		if !ok || m.Len() != 0 {
			t.Errorf("parsed %q as %#v, want an empty map", in, v)
		}
	}
}

// What the writer produces, the reader reads back unchanged.
func TestMacDefaultsPlistRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"string", "hello world"},
		{"string with markup", `a & b < c > "d"`},
		{"int", int64(42)},
		{"float", 3.5},
		{"bool true", true},
		{"bool false", false},
		{"array", []any{"a", int64(1), true}},
		{"nested", value.MapOf("k", "v", "n", []any{int64(1), int64(2)})},
		{"empty array", []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := parsePlist([]byte(plistXML(tc.in)))
			if err != nil {
				t.Fatalf("round trip: %v\n%s", err, plistXML(tc.in))
			}
			if !macDefaultsEqual(out, tc.in) {
				t.Errorf("round trip changed the value: %#v -> %#v", tc.in, out)
			}
		})
	}
}

// macDefaultsEqual keeps the plist type kinds distinct.
func TestMacDefaultsEqualIsTyped(t *testing.T) {
	cases := []struct {
		a, b any
		want bool
	}{
		{int64(1), int64(1), true},
		{int64(1), true, false}, // integer 1 is not boolean true
		{int64(1), "1", false},  // integer 1 is not the string "1"
		{int64(1), 1.0, false},  // integer is not real
		{"left", "left", true},
		{[]any{"a"}, []any{"a"}, true},
		{[]any{"a"}, []any{"a", "b"}, false},
		{nil, nil, true},
	}
	for _, tc := range cases {
		if got := macDefaultsEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("macDefaultsEqual(%#v, %#v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// The `defaults write` argument vector matches the declared type.
func TestMacDefaultsWriteArgv(t *testing.T) {
	cases := []struct {
		vtype string
		v     any
		tail  []string
	}{
		{"string", "left", []string{"-string", "left"}},
		{"int", int64(48), []string{"-int", "48"}},
		{"integer", "48", []string{"-int", "48"}},
		{"float", 3.5, []string{"-float", "3.5"}},
		{"bool", true, []string{"-bool", "true"}},
		{"boolean", 0, []string{"-bool", "false"}},
	}
	for _, tc := range cases {
		argv, err := macDefaultsWriteArgv("com.apple.dock", "k", tc.vtype, tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.vtype, err)
		}
		want := append([]string{"defaults", "write", "com.apple.dock", "k"}, tc.tail...)
		if strings.Join(argv, " ") != strings.Join(want, " ") {
			t.Errorf("%s: argv = %v, want %v", tc.vtype, argv, want)
		}
	}

	// array and dict pass one plist string.
	argv, err := macDefaultsWriteArgv("com.apple.dock", "persistent-apps", "array", []any{"Safari"})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 5 || !strings.Contains(argv[4], "<string>Safari</string>") {
		t.Errorf("array write argv = %v", argv)
	}
}

// A key already at the wanted value is a no-op with no `defaults write`.
func TestMacDefaultsWriteStateConverged(t *testing.T) {
	c, runner := macDefaultsCtx(t, map[string]string{"com.apple.dock": macDefaultsExportFixture})
	res, err := macDefaultsWriteState(c, value.MapOf(
		"domain", "com.apple.dock", "key", "orientation", "value", "left", "vtype", "string"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("converged write reported %+v", res)
	}
	for _, ran := range runner.RanCommands() {
		if strings.HasPrefix(ran, "defaults write") {
			t.Errorf("a converged state still ran %q", ran)
		}
	}
}

// A key at the wrong value, and at the wrong type, both change.
func TestMacDefaultsWriteStateChanges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value any
		vtype string
	}{
		{"different value", "orientation", "bottom", "string"},
		{"different type", "tilesize", 48, "string"}, // stored as integer, wanted as string
		{"missing key", "autohide-delay", 0.5, "float"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, runner := macDefaultsCtx(t, map[string]string{"com.apple.dock": macDefaultsExportFixture})
			res, err := macDefaultsWriteState(c, value.MapOf(
				"domain", "com.apple.dock", "key", tc.key, "value", tc.value, "vtype", tc.vtype))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Succeeded() || !res.HasChanges() {
				t.Fatalf("expected a change, got %+v", res)
			}
			wrote := false
			for _, ran := range runner.RanCommands() {
				if strings.HasPrefix(ran, "defaults write com.apple.dock "+tc.key) {
					wrote = true
				}
			}
			if !wrote {
				t.Errorf("no `defaults write` for %s: %v", tc.key, runner.RanCommands())
			}
		})
	}
}

// Test mode predicts the change and runs no `defaults write`.
func TestMacDefaultsWriteStateTestMode(t *testing.T) {
	c, runner := macDefaultsCtx(t, map[string]string{"com.apple.dock": macDefaultsExportFixture})
	c.Test = true
	res, err := macDefaultsWriteState(c, value.MapOf(
		"domain", "com.apple.dock", "key", "orientation", "value", "bottom", "vtype", "string"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode returned a decided result: %+v", res)
	}
	if !res.HasChanges() {
		t.Error("test mode predicted no change")
	}
	for _, ran := range runner.RanCommands() {
		if strings.HasPrefix(ran, "defaults write") {
			t.Errorf("test mode ran %q", ran)
		}
	}
}

// absent is converged when the key is already gone, and changes when it
// is there.
func TestMacDefaultsAbsentState(t *testing.T) {
	c, _ := macDefaultsCtx(t, map[string]string{"com.apple.dock": macDefaultsExportFixture})
	gone, err := macDefaultsAbsentState(c, value.MapOf("domain", "com.apple.dock", "key", "not-a-key"))
	if err != nil {
		t.Fatal(err)
	}
	if !gone.Succeeded() || gone.HasChanges() {
		t.Errorf("absent on a missing key reported %+v", gone)
	}

	c, runner := macDefaultsCtx(t, map[string]string{"com.apple.dock": macDefaultsExportFixture})
	hit, err := macDefaultsAbsentState(c, value.MapOf("domain", "com.apple.dock", "key", "AutoHide"))
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Succeeded() || !hit.HasChanges() {
		t.Fatalf("absent on a set key reported %+v", hit)
	}
	deleted := false
	for _, ran := range runner.RanCommands() {
		if ran == "defaults delete com.apple.dock AutoHide" {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("absent did not run the delete: %v", runner.RanCommands())
	}
}

// A domain that cannot be read is a failure, not a silent no-op.
func TestMacDefaultsWriteStateNeedsDomainAndKey(t *testing.T) {
	c, _ := macDefaultsCtx(t, nil)
	res, err := macDefaultsWriteState(c, value.MapOf("domain", "", "key", "", "value", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Errorf("a state with no domain or key succeeded: %+v", res)
	}
}

// Off a Mac the signature refuses the call before `defaults` is looked
// for, so the module still registers everywhere.
func TestMacDefaultsIsRegisteredAndRestricted(t *testing.T) {
	r := New()
	for _, name := range []string{"mac_defaults.read", "mac_defaults.write", "mac_defaults.delete"} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "darwin" {
			t.Errorf("%s platforms = %v, want [darwin]", name, sig.Platforms)
		}
	}
	for _, name := range []string{"mac_defaults.write", "mac_defaults.absent"} {
		if _, ok := r.States.Signatures().Lookup(name); !ok {
			t.Errorf("state %s is not registered", name)
		}
	}
}

// What `defaults delete` prints when there was nothing to delete.
//
// Captured from the real `defaults` on macOS 27.0 (build 26A5425a),
// stderr, exit 1. The strings are the fixture: this is the branch that
// was written from belief and never run, and the point of holding the
// tool's own words here is that the next person changing the check has
// to change what the tool said too.
const (
	macDefaultsDomainGone = "Error: Domain 'com.halite.selftest.gone' not found.\n" +
		"Defaults have not been changed.\n"
	macDefaultsKeyGone = "Error: Could not find key 'NoSuchKey' in domain " +
		"'com.halite.selftest.here'.\nDefaults have not been changed.\n"
)

// Deleting something already gone is the state that was wanted, not an
// error -- which is what `mac_defaults.delete` has always documented and
// did not do.
func TestMacDefaultsDeleteToleratesWhatIsAlreadyGone(t *testing.T) {
	cases := []struct {
		name, domain, key, stderr string
	}{
		{"domain", "com.halite.selftest.gone", "", macDefaultsDomainGone},
		{"key", "com.halite.selftest.here", "NoSuchKey", macDefaultsKeyGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := "defaults delete " + tc.domain
			if tc.key != "" {
				argv += " " + tc.key
			}
			c, _ := macDefaultsCtx(t, nil)
			c.Runner = &exec.RecordingRunner{Responses: map[string]exec.Result{
				argv: {Code: 1, Stderr: tc.stderr},
			}}
			if err := macDefaultsDelete(c, tc.domain, tc.key, ""); err != nil {
				t.Errorf("delete of an absent %s was an error: %v", tc.name, err)
			}
		})
	}
}

// A delete that failed for any other reason is still an error.
func TestMacDefaultsDeleteReportsARealFailure(t *testing.T) {
	c, _ := macDefaultsCtx(t, nil)
	c.Runner = &exec.RecordingRunner{Responses: map[string]exec.Result{
		"defaults delete /Library/Preferences/com.halite.selftest": {
			Code: 1, Stderr: "Permission denied\n",
		},
	}}
	err := macDefaultsDelete(c, "/Library/Preferences/com.halite.selftest", "", "")
	if err == nil {
		t.Fatal("a delete that failed on permissions was reported as success")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("the error lost what `defaults` said: %v", err)
	}
}

// Every `defaults` call that reads its own exit code has to ask for it.
//
// # Why this test exists at all
//
// `OSRunner` turns a non-zero exit into a Go error unless the command
// sets `IgnoreExitCode`, so a function that inspects `res.Code` without
// it never reaches its own branch on a real Mac -- `c.Run` has already
// returned. `RecordingRunner` does not do that: it hands back the
// scripted `Result` with a nil error, exit code and all. So a module
// that forgets the flag passes every unit test and fails on hardware,
// which is what `mac_defaults` did on all four of these until a live
// test under sudo found it.
//
// Asserting on the flag rather than on the behaviour is deliberate. The
// behaviour cannot be reproduced through the fake, and a test that
// cannot fail for the real reason is the thing being guarded against.
func TestMacDefaultsCallsAskForTheirExitCode(t *testing.T) {
	c, runner := macDefaultsCtx(t, map[string]string{"com.apple.dock": macDefaultsExportFixture})

	if _, err := macDefaultsExport(c, "com.apple.dock", ""); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := macDefaultsReadType(c, "com.apple.dock", "AutoHide", ""); err != nil {
		t.Fatalf("read-type: %v", err)
	}
	if err := macDefaultsWrite(c, "com.apple.dock", "AutoHide", "bool", true, ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := macDefaultsDelete(c, "com.apple.dock", "AutoHide", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(runner.Ran) != 4 {
		t.Fatalf("ran %d commands, want 4: %v", len(runner.Ran), runner.RanCommands())
	}
	for _, ran := range runner.Ran {
		if !ran.IgnoreExitCode {
			t.Errorf("%q does not set IgnoreExitCode, so its own exit-code branch "+
				"is unreachable against the real `defaults`", ran.String())
		}
	}
}
