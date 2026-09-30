package builtin

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every fixture here was captured from authselect 1.2.6 on 2026-09-30:
// Rocky Linux 9.8 (authselect-1.2.6-3.el9) and AlmaLinux 8.10
// (authselect-1.2.6-2.el8), stdout and stderr captured separately with
// the exit code beside them. Where the two hosts differ it says so; they
// differed only in `list`, where EL8 still ships the `nis` profile.

const (
	// Both hosts, stdout, exit 2 -- with or without --raw.
	authselectNoConfigStdout = "No existing configuration detected.\n"
	// Rocky 9.8, after `select minimal` and two enable-feature calls, in
	// that order: the features are in the order they were enabled.
	authselectRawTwoFeatures = "minimal with-silent-lastlog with-pwhistory\n"
	authselectRawMinimal     = "minimal\n"

	authselectCheckValidStdout      = "Current configuration is valid.\n"
	authselectCheckNotConfigStdout  = "System was not configured with authselect.\n"
	authselectCheckModifiedStdout   = "Current configuration is not valid. It was probably modified outside authselect.\n"
	authselectCheckNotSymlinkStderr = "[error] [/etc/pam.d/password-auth] is not a symbolic link!\n" +
		"[error] [/etc/pam.d/password-auth] was not created by authselect!\n"

	// AlmaLinux 8.10. The id is space-padded to the longest before the tab.
	authselectListEL8 = "- minimal\t Local users only for minimal installations\n" +
		"- nis    \t Enable NIS for system authentication\n" +
		"- sssd   \t Enable SSSD for system authentication (also for local users only)\n" +
		"- winbind\t Enable winbind for system authentication\n"

	authselectNoSuchProfileStderr = "[error] Unable to get profile information [2]: No such file or directory\n"

	// Rocky 9.8, `select minimal` on a node that had never been configured.
	authselectSelectRefusedStderr = "[error] File [/etc/pam.d/system-auth] exists but it needs to be overwritten!\n" +
		"[error] File [/etc/pam.d/password-auth] exists but it needs to be overwritten!\n" +
		"[error] File [/etc/pam.d/fingerprint-auth] exists but it needs to be overwritten!\n" +
		"[error] File [/etc/pam.d/smartcard-auth] exists but it needs to be overwritten!\n" +
		"[error] File [/etc/pam.d/postlogin] exists but it needs to be overwritten!\n" +
		"[error] File [/etc/nsswitch.conf] exists but it needs to be overwritten!\n" +
		"[error] File that needs to be overwritten was found\n" +
		"[error] Refusing to activate profile unless this file is removed or overwrite is requested.\n" +
		"\nSome unexpected changes to the configuration were detected.\n" +
		"Use --force parameter if you want to overwrite these changes.\n"

	authselectSelectedMinimalStdout = "Profile \"minimal\" was selected.\n" +
		"The following nsswitch maps are overwritten by the profile:\n" +
		"- aliases\n- automount\n- ethers\n- group\n- hosts\n- initgroups\n- netgroup\n" +
		"- networks\n- passwd\n- protocols\n- publickey\n- rpc\n- services\n- shadow\n"

	authselectUnknownFeatureStderr = "[error] Unknown profile feature [no-such-feature]\n" +
		"[error] Unable to activate profile [minimal] [22]: Invalid argument\n" +
		"Unable to enable feature [22]: Invalid argument\n"
)

// authselectScript is a fake runner that answers each command from a
// queue, so `current` can say one thing before a change and another
// after it -- which RecordingRunner, one answer per command, cannot.
//
// It also refuses the way exec.OSRunner does: a non-zero exit on a
// command that did not ask for its code is an error. RecordingRunner
// does not, which is how mac_defaults shipped branches no real machine
// could reach (DIVERGENCE 5.113); a fake that forgives it here would
// make every "not configured" test below meaningless.
type authselectScript struct {
	answers map[string][]exec.Result
	ran     []string
}

func (s *authselectScript) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	key := cmd.String()
	s.ran = append(s.ran, key)
	queue := s.answers[key]
	if len(queue) == 0 {
		return exec.Result{}, fmt.Errorf("unscripted command %q", key)
	}
	res := queue[0]
	if len(queue) > 1 {
		s.answers[key] = queue[1:]
	}
	if res.Code != 0 && !cmd.IgnoreExitCode {
		return res, fmt.Errorf("%s exited %d", key, res.Code)
	}
	return res, nil
}

func authselectKey(args ...string) string {
	return exec.Command{Argv: append([]string{"authselect"}, args...)}.String()
}

func authselectContext(answers map[string][]exec.Result) (*exec.Context, *authselectScript) {
	script := &authselectScript{answers: answers}
	return &exec.Context{Runner: script, Lookup: func(name string) string { return "/usr/bin/" + name }}, script
}

func TestAuthselectRefusesByToolNameWhereItIsMissing(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	_, err := authselectCurrent(c)
	if err == nil || !strings.Contains(err.Error(), "authselect") || !strings.Contains(err.Error(), "pam-auth-update") {
		t.Fatalf("err = %v, want it to name authselect and what Debian uses instead", err)
	}
}

func TestAuthselectCurrentOnANodeNeverConfigured(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectNoConfigStdout, Code: 2}},
	})
	cur, err := authselectCurrent(c)
	if err != nil {
		t.Fatal(err)
	}
	// The trap: stdout is a sentence, and its first word is not a profile.
	if cur.Configured || cur.Profile != "" || len(cur.Features) != 0 {
		t.Errorf("current = %+v, want unconfigured with no profile", cur)
	}
}

func TestAuthselectCurrentKeepsTheToolsFeatureOrder(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectRawTwoFeatures}},
	})
	cur, err := authselectCurrent(c)
	if err != nil {
		t.Fatal(err)
	}
	if !cur.Configured || cur.Profile != "minimal" ||
		!slices.Equal(cur.Features, []string{"with-silent-lastlog", "with-pwhistory"}) {
		t.Errorf("current = %+v", cur)
	}
	if !cur.sameAs("minimal", []string{"with-pwhistory", "with-silent-lastlog"}) {
		t.Error("the same two features in the other order were read as a different selection")
	}
	if cur.sameAs("minimal", []string{"with-pwhistory"}) || cur.sameAs("sssd", cur.Features) {
		t.Error("a different selection was read as the same one")
	}
}

func TestAuthselectCheckReadsAllThreeAnswers(t *testing.T) {
	cases := []struct {
		name               string
		res                exec.Result
		configured, valid  bool
		problemsContaining string
	}{
		{"valid", exec.Result{Stdout: authselectCheckValidStdout}, true, true, ""},
		{"never configured", exec.Result{Stdout: authselectCheckNotConfigStdout, Code: 2}, false, false, ""},
		{"symlink replaced by a file",
			exec.Result{Stdout: authselectCheckModifiedStdout, Stderr: authselectCheckNotSymlinkStderr, Code: 3},
			true, false, "[/etc/pam.d/password-auth] is not a symbolic link!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := authselectContext(map[string][]exec.Result{authselectKey("check"): {tc.res}})
			got, err := authselectCheck(c)
			if err != nil {
				t.Fatal(err)
			}
			if got.Configured != tc.configured || got.Valid != tc.valid {
				t.Errorf("check = %+v", got)
			}
			if tc.problemsContaining != "" && !slices.Contains(got.Problems, tc.problemsContaining) {
				t.Errorf("problems = %q, want %q among them with the [error] prefix gone", got.Problems, tc.problemsContaining)
			}
		})
	}
}

func TestAuthselectListSplitsOnTheTabNotThePadding(t *testing.T) {
	got := authselectParseList(authselectListEL8)
	if len(got) != 4 {
		t.Fatalf("list = %#v", got)
	}
	nis := got[1].(*value.Map)
	if id, _ := nis.GetString("id"); id != "nis" {
		t.Errorf("id = %#v, want the padding trimmed", id)
	}
	if d, _ := nis.GetString("description"); d != "Enable NIS for system authentication" {
		t.Errorf("description = %#v", d)
	}
}

func TestAuthselectListFeaturesOfAnUnknownProfileSaysWhy(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("list-features", "nosuch"): {{Stderr: authselectNoSuchProfileStderr, Code: 2}},
	})
	_, err := authselectListFeaturesFn(c, value.MapOf("profile", "nosuch"))
	if err == nil || !strings.Contains(err.Error(), "Unable to get profile information") {
		t.Fatalf("err = %v, want authselect's own words", err)
	}
}

func TestAuthselectSelectOfTheCurrentSelectionChangesNothing(t *testing.T) {
	c, script := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectRawTwoFeatures}},
		authselectKey("check"):            {{Stdout: authselectCheckValidStdout}},
	})
	got, err := authselectSelectFn(c, value.MapOf("profile", "minimal",
		"features", []any{"with-pwhistory", "with-silent-lastlog"}))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != false {
		t.Errorf("result = %#v, want no change", got)
	}
	for _, ran := range script.ran {
		if strings.Contains(ran, " select ") {
			t.Errorf("ran %q; authselect rewrites every file on a repeat select and should not have been asked", ran)
		}
	}
}

func TestAuthselectSelectReappliesOverAHandEdit(t *testing.T) {
	c, script := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectRawMinimal}, {Stdout: authselectRawMinimal}},
		authselectKey("check"): {{Stdout: authselectCheckModifiedStdout,
			Stderr: authselectCheckNotSymlinkStderr, Code: 3}},
		authselectKey("select", "minimal", "--force"): {{Stdout: authselectSelectedMinimalStdout}},
	})
	got, err := authselectSelectFn(c, value.MapOf("profile", "minimal", "force", true))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != true {
		t.Errorf("result = %#v, want a change: the files were not authselect's", got)
	}
	if !slices.Contains(script.ran, authselectKey("select", "minimal", "--force")) {
		t.Errorf("ran %q", script.ran)
	}
}

func TestAuthselectSelectCarriesTheRefusalAndNamesForce(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"):  {{Stdout: authselectNoConfigStdout, Code: 2}},
		authselectKey("select", "minimal"): {{Stderr: authselectSelectRefusedStderr, Code: 4}},
	})
	_, err := authselectSelectFn(c, value.MapOf("profile", "minimal"))
	if err == nil || !strings.Contains(err.Error(), "force: true") ||
		!strings.Contains(err.Error(), "[/etc/nsswitch.conf] exists but it needs to be overwritten") {
		t.Fatalf("err = %v, want the refusal in authselect's words and the way past it", err)
	}
}

func TestAuthselectSelectRefusesASuccessItCannotReadBack(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectNoConfigStdout, Code: 2},
			{Stdout: authselectNoConfigStdout, Code: 2}},
		authselectKey("select", "minimal", "with-mkhomedir"): {{Stdout: authselectSelectedMinimalStdout}},
	})
	_, err := authselectSelectFn(c, value.MapOf("profile", "minimal", "features", []any{"with-mkhomedir"}))
	if err == nil || !strings.Contains(err.Error(), "reported success") {
		t.Fatalf("err = %v, want exit 0 with an unchanged selection refused", err)
	}
}

func TestAuthselectSelectInTestModeRunsNothing(t *testing.T) {
	c, script := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectNoConfigStdout, Code: 2}},
	})
	c.Test = true
	got, err := authselectSelectFn(c, value.MapOf("profile", "sssd", "features", []any{"with-mkhomedir"}))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != true {
		t.Errorf("result = %#v", got)
	}
	if len(script.ran) != 1 {
		t.Errorf("ran %q, want only the read", script.ran)
	}
}

func TestAuthselectEnableFeatureAlreadyOnRunsNothing(t *testing.T) {
	c, script := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectRawTwoFeatures}},
	})
	got, err := authselectToggle(c, "with-pwhistory", true)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != false {
		t.Errorf("result = %#v", got)
	}
	if len(script.ran) != 1 {
		t.Errorf("ran %q; a repeat enable-feature rewrites every file and should not be run", script.ran)
	}
}

// TestAuthselectDisableFeatureNotOnIsNoChange is the case authselect
// itself gets wrong in the other direction: `disable-feature` of a
// feature that is not on, or does not exist, exits 0 silently.
func TestAuthselectDisableFeatureNotOnIsNoChange(t *testing.T) {
	c, script := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectRawMinimal}},
	})
	got, err := authselectToggle(c, "no-such-feature", false)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != false {
		t.Errorf("result = %#v", got)
	}
	if len(script.ran) != 1 {
		t.Errorf("ran %q", script.ran)
	}
}

func TestAuthselectEnableUnknownFeatureCarriesTheToolsWords(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"):                  {{Stdout: authselectRawMinimal}},
		authselectKey("enable-feature", "no-such-feature"): {{Stderr: authselectUnknownFeatureStderr, Code: 1}},
	})
	_, err := authselectToggle(c, "no-such-feature", true)
	if err == nil || !strings.Contains(err.Error(), "Unknown profile feature [no-such-feature]") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthselectToggleOnAnUnconfiguredNodeSaysSelectFirst(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectNoConfigStdout, Code: 2}},
	})
	_, err := authselectToggle(c, "with-faillock", true)
	if err == nil || !strings.Contains(err.Error(), "authselect.select") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthselectDisableFeatureConfirmsItWentAway(t *testing.T) {
	c, _ := authselectContext(map[string][]exec.Result{
		authselectKey("current", "--raw"): {{Stdout: authselectRawTwoFeatures},
			{Stdout: "minimal with-silent-lastlog\n"}},
		authselectKey("disable-feature", "with-pwhistory"): {{}},
	})
	got, err := authselectToggle(c, "with-pwhistory", false)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(*value.Map)
	if changed, _ := m.Get("changed"); changed != true {
		t.Errorf("result = %#v", got)
	}
	after, _ := m.Get("new")
	features, _ := after.(*value.Map).Get("features")
	if !slices.Equal(anyStrings(features), []string{"with-silent-lastlog"}) {
		t.Errorf("new features = %#v", features)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}
