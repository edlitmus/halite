package builtin

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerAuthselect installs the `authselect` module of SPEC 15.3's
// Common Linux row.
//
// # What it is for
//
// On Fedora and RHEL 8 and later, the files that decide how a login is
// authenticated -- /etc/pam.d/system-auth, password-auth, postlogin,
// fingerprint-auth, smartcard-auth, and /etc/nsswitch.conf -- are not
// meant to be edited. They are generated from a *profile* (`minimal`,
// `sssd`, `winbind`, `nis` on EL8) and a set of *features*
// (`with-mkhomedir`, `with-faillock`, ...), written under
// /etc/authselect, and symlinked into place. The question an estate has
// is therefore not "what does system-auth say" but "which profile and
// which features is this node on, and has anybody edited the result by
// hand", and `authselect` is the only thing that can answer either.
//
// # What was learnt from the real tool rather than from its manual
//
// Everything below was taken from authselect 1.2.6 on Rocky Linux 9.8
// and AlmaLinux 8.10 (DIVERGENCE 5.173), which are the versions those
// releases actually ship -- neither has the 1.3+ `opt-out` subcommand
// or a `--version` flag, so this module offers neither.
//
//   - Exit codes, not text, carry the answer. `authselect current`
//     exits 2 on a node with no configuration and prints "No existing
//     configuration detected." to *stdout* -- including under `--raw`,
//     so a parser that read the first word of `--raw` as the profile
//     would report a profile called "No". `check` exits 0 for valid, 2
//     for "not configured" and 3 for "modified outside authselect".
//     The messages are translated and the codes are not, so the codes
//     are what this reads.
//   - `enable-feature` of a feature that is already on exits 0 and
//     rewrites every generated file anyway (their mtimes move). The tool
//     is not idempotent; this module is, by reading `current` first.
//   - `disable-feature` of a feature that does not exist, or is not on,
//     exits 0 and says nothing, while `enable-feature` of an unknown one
//     exits 1. So a disable's "changed" comes from `current` before and
//     after, never from the exit code.
//   - `current --raw` lists features in the order they were enabled,
//     not sorted, so "the same selection" is compared as a set.
//
// # How it relates to `pam`
//
// The two are two paths to the same files and must not fight. `pam`
// reads through authselect's symlinks without noticing them, which is
// right: `pam.rules sshd` on an authselect node resolves `include
// password-auth` into the generated file, and that is the chain that
// runs. `pam`'s *edits* are the other matter: `pam.set_module` on a
// file authselect generated used to replace authselect's symlink with a
// regular file, `authselect check` then failed, and the next forced
// select threw the edit away (DIVERGENCE 5.173). pamRefuseLinked now
// refuses, and names this module as the way to make the change.
//
// There is no `authselect` state: SPEC 15.5 names none, and
// `authselect.select` is idempotent on its own, so `module.run` is how a
// tree asserts a profile (plan.md §7 item 14 has the example).
func registerAuthselect(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "current",
				Doc: "Return the selected profile and its enabled features, and whether this node " +
					"is configured by authselect at all.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cur, err := authselectCurrent(c)
				if err != nil {
					return nil, err
				}
				return cur.value(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "check",
				Doc: "Report whether the generated configuration is still what authselect wrote, " +
					"and what it found changed if not.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				chk, err := authselectCheck(c)
				if err != nil {
					return nil, err
				}
				return chk.value(), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "list",
				Doc:       "List the profiles this node can select, each with its description.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: authselectListFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "list_features",
				Doc: "List the optional features a profile offers.",
				Params: []signature.Param{
					req("profile", signature.String, "The profile, such as minimal or sssd."),
				},
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: authselectListFeaturesFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "backup_list",
				Doc:       "List the backups authselect has taken of the files it replaced.",
				TestMode:  signature.TestNotApplicable,
				Platforms: linuxOnly,
				Section:   "15.3",
			},
			Fn: authselectBackupListFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "select",
				Doc: "Select a profile and exactly this set of features. Does nothing when that is " +
					"already the selection and the generated files are unmodified.",
				Params: []signature.Param{
					req("profile", signature.String, "The profile, such as minimal or sssd."),
					opt("features", signature.List, nil,
						"The features to enable, such as with-mkhomedir. Any not listed are turned off."),
					opt("force", signature.Bool, false,
						"Overwrite files authselect did not write, or that were edited since. "+
							"authselect backs them up under /var/lib/authselect/backups first."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: authselectSelectFn,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "enable_feature",
				Doc: "Turn one feature on in the selected profile.",
				Params: []signature.Param{
					req("feature", signature.String, "The feature, such as with-faillock."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return authselectToggle(c, states.Str(args, "feature", ""), true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "authselect", Function: "disable_feature",
				Doc: "Turn one feature off in the selected profile.",
				Params: []signature.Param{
					req("feature", signature.String, "The feature, such as with-faillock."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  linuxOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return authselectToggle(c, states.Str(args, "feature", ""), false)
			},
		},
	)
}

// The exit codes authselect 1.2.6 was seen to use on both hosts. They
// are what this module reads, rather than the messages beside them,
// because authselect ships translations of its messages
// (/usr/share/locale/*/LC_MESSAGES/authselect.mo on both hosts) and a
// node with a non-English locale would print something else.
const (
	authselectExitNotConfigured = 2
	authselectExitModified      = 3
	authselectExitRefused       = 4
)

// authselectToolPresent refuses by the tool's name. A Debian node has
// no authselect to be missing, and saying so is more use than "this is
// not RHEL": the operator's next question is what does the job there.
func authselectToolPresent(c *exec.Context) error {
	if c.Which("authselect") == "" {
		return errors.New("this node has no `authselect`; it ships on Fedora and RHEL 8 and later, " +
			"and Debian and Ubuntu manage PAM with pam-auth-update, which `pam` reads")
	}
	return nil
}

// authselectRun runs one subcommand with its exit code kept, because
// every answer this module wants from authselect is carried by the
// code. Without IgnoreExitCode the OS runner turns exit 2 into an error
// and the "not configured" branch is never reached on a real machine --
// while a fake runner happily reaches it (DIVERGENCE 5.113).
func authselectRun(c *exec.Context, args ...string) (exec.Result, error) {
	if err := authselectToolPresent(c); err != nil {
		return exec.Result{}, err
	}
	argv := append([]string{"authselect"}, args...)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return res, fmt.Errorf("`%s` could not be run on this node: %w", exec.Command{Argv: argv}.String(), err)
	}
	return res, nil
}

// authselectFailure words a non-zero exit in authselect's own words,
// which arrive on stderr prefixed "[error] " and are worth keeping whole.
func authselectFailure(what string, res exec.Result) error {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	if res.Code == authselectExitRefused {
		return fmt.Errorf("%s was refused (exit %d): files authselect manages were changed outside it, "+
			"or were never its own; authselect.select with `force: true` overwrites them after a backup: %s",
			what, res.Code, msg)
	}
	return fmt.Errorf("%s failed (exit %d): %s", what, res.Code, msg)
}

// ---- reading ----

// authselectSelection is what `current --raw` says.
type authselectSelection struct {
	Configured bool
	Profile    string
	Features   []string
}

func (s authselectSelection) value() *value.Map {
	out := value.NewMap(3)
	out.Set("configured", s.Configured)
	out.Set("profile", s.Profile)
	out.Set("features", toAnyList(s.Features))
	return out
}

// sameAs compares as a set: authselect keeps features in the order they
// were enabled, so `[a, b]` and `[b, a]` are one selection written twice.
func (s authselectSelection) sameAs(profile string, features []string) bool {
	if !s.Configured || s.Profile != profile {
		return false
	}
	return slices.Equal(sortedUnique(s.Features), sortedUnique(features))
}

func (s authselectSelection) has(feature string) bool {
	for _, f := range s.Features {
		if f == feature {
			return true
		}
	}
	return false
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func authselectCurrent(c *exec.Context) (authselectSelection, error) {
	res, err := authselectRun(c, "current", "--raw")
	if err != nil {
		return authselectSelection{}, err
	}
	return authselectParseCurrent(res)
}

// authselectParseCurrent reads `current --raw`: the profile id and then
// each feature, space-separated, on one line. Exit 2 is "nothing
// selected", and its stdout is a sentence, not a profile.
func authselectParseCurrent(res exec.Result) (authselectSelection, error) {
	switch res.Code {
	case 0:
	case authselectExitNotConfigured:
		return authselectSelection{Features: []string{}}, nil
	default:
		return authselectSelection{}, authselectFailure("`authselect current`", res)
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) == 0 {
		return authselectSelection{}, errors.New("`authselect current --raw` exited 0 and named no profile")
	}
	return authselectSelection{Configured: true, Profile: fields[0], Features: append([]string{}, fields[1:]...)}, nil
}

// authselectCheckResult is what `check` says.
type authselectCheckResult struct {
	Configured bool
	Valid      bool
	Comment    string
	// Problems are authselect's own per-file findings, such as
	// "[/etc/pam.d/password-auth] is not a symbolic link!", with the
	// "[error] " prefix taken off.
	Problems []string
}

func (r authselectCheckResult) value() *value.Map {
	out := value.NewMap(4)
	out.Set("configured", r.Configured)
	out.Set("valid", r.Valid)
	out.Set("comment", r.Comment)
	out.Set("problems", toAnyList(r.Problems))
	return out
}

func authselectCheck(c *exec.Context) (authselectCheckResult, error) {
	res, err := authselectRun(c, "check")
	if err != nil {
		return authselectCheckResult{}, err
	}
	return authselectParseCheck(res)
}

func authselectParseCheck(res exec.Result) (authselectCheckResult, error) {
	out := authselectCheckResult{Comment: strings.TrimSpace(res.Stdout), Problems: []string{}}
	switch res.Code {
	case 0:
		out.Configured, out.Valid = true, true
	case authselectExitNotConfigured:
	case authselectExitModified:
		out.Configured = true
		for _, line := range strings.Split(res.Stderr, "\n") {
			if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "[error]")); line != "" {
				out.Problems = append(out.Problems, line)
			}
		}
	default:
		return authselectCheckResult{}, authselectFailure("`authselect check`", res)
	}
	return out, nil
}

// authselectListFn reads `authselect list`, whose lines are
// "- <id><padding>\t <description>" -- the id is space-padded to the
// longest one before the tab, so the split is on the tab and both
// halves are trimmed.
func authselectListFn(c *exec.Context, args *value.Map) (any, error) {
	res, err := authselectRun(c, "list")
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, authselectFailure("`authselect list`", res)
	}
	return authselectParseList(res.Stdout), nil
}

func authselectParseList(out string) []any {
	profiles := []any{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		id, description, _ := strings.Cut(strings.TrimPrefix(line, "- "), "\t")
		entry := value.NewMap(2)
		entry.Set("id", strings.TrimSpace(id))
		entry.Set("description", strings.TrimSpace(description))
		profiles = append(profiles, entry)
	}
	return profiles
}

func authselectListFeaturesFn(c *exec.Context, args *value.Map) (any, error) {
	profile := strings.TrimSpace(states.Str(args, "profile", ""))
	if profile == "" {
		return nil, errors.New("a profile must be named")
	}
	res, err := authselectRun(c, "list-features", profile)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, authselectFailure(fmt.Sprintf("`authselect list-features %s`", profile), res)
	}
	return authselectLines(res.Stdout), nil
}

func authselectBackupListFn(c *exec.Context, args *value.Map) (any, error) {
	res, err := authselectRun(c, "backup-list", "--raw")
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, authselectFailure("`authselect backup-list`", res)
	}
	return authselectLines(res.Stdout), nil
}

func authselectLines(out string) []any {
	lines := []any{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// ---- changing ----

// authselectSelectArgv is `authselect select PROFILE [FEATURE...]
// [--force]`. `--quiet` is deliberately absent: without it authselect
// prints the profile's REQUIREMENTS -- "make sure pam_oddjob_mkhomedir
// module is present and oddjobd service is enabled" -- and a caller who
// selected with-mkhomedir on a node without oddjobd needs to see that.
func authselectSelectArgv(profile string, features []string, force bool) []string {
	argv := append([]string{"select", profile}, features...)
	if force {
		argv = append(argv, "--force")
	}
	return argv
}

func authselectSelectFn(c *exec.Context, args *value.Map) (any, error) {
	profile := strings.TrimSpace(states.Str(args, "profile", ""))
	if profile == "" {
		return nil, errors.New("a profile must be named")
	}
	features := uniqueInOrder(states.Strings(args, "features"))
	force := states.Bool(args, "force", false)

	before, err := authselectCurrent(c)
	if err != nil {
		return nil, err
	}
	// The same selection is only "no change" if the files are still the
	// ones authselect wrote. A node whose password-auth was hand-edited
	// reports the right profile from `current` and is not on it.
	if before.sameAs(profile, features) {
		chk, err := authselectCheck(c)
		if err != nil {
			return nil, err
		}
		if chk.Valid {
			return authselectResult(c, false, fmt.Sprintf("Profile %q is already selected with exactly these features.",
				profile), before, before, ""), nil
		}
	}

	want := authselectSelection{Configured: true, Profile: profile, Features: features}
	if c.Test {
		return authselectResult(c, true, fmt.Sprintf("Profile %q would be selected.", profile), before, want, ""), nil
	}
	res, err := authselectRun(c, authselectSelectArgv(profile, features, force)...)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, authselectFailure(fmt.Sprintf("selecting profile %q", profile), res)
	}
	after, err := authselectConfirm(c, func(s authselectSelection) bool { return s.sameAs(profile, features) })
	if err != nil {
		return nil, err
	}
	return authselectResult(c, true, fmt.Sprintf("Profile %q was selected.", profile), before, after,
		strings.TrimSpace(res.Stdout)), nil
}

// uniqueInOrder drops blanks and repeats but keeps the caller's
// order, which is the order authselect will record them in.
func uniqueInOrder(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// authselectToggle is enable_feature and disable_feature. The decision
// comes from `current`, not from authselect's exit code: enable of an
// enabled feature rewrites everything and exits 0, and disable of a
// feature that is not on -- or does not exist -- also exits 0.
func authselectToggle(c *exec.Context, feature string, enable bool) (any, error) {
	feature = strings.TrimSpace(feature)
	if feature == "" {
		return nil, errors.New("a feature must be named")
	}
	before, err := authselectCurrent(c)
	if err != nil {
		return nil, err
	}
	if !before.Configured {
		return nil, fmt.Errorf("this node has no authselect profile selected, so there is nothing to %s %s in; "+
			"select one first with authselect.select", map[bool]string{true: "enable", false: "disable"}[enable], feature)
	}
	if before.has(feature) == enable {
		state := map[bool]string{true: "already enabled", false: "not enabled"}[enable]
		return authselectResult(c, false, fmt.Sprintf("Feature %q is %s in profile %q.", feature, state, before.Profile),
			before, before, ""), nil
	}

	want := before
	verb, subcommand := "disabled", "disable-feature"
	if enable {
		want.Features = append(append([]string{}, before.Features...), feature)
		verb, subcommand = "enabled", "enable-feature"
	} else {
		want.Features = nil
		for _, f := range before.Features {
			if f != feature {
				want.Features = append(want.Features, f)
			}
		}
	}
	if c.Test {
		return authselectResult(c, true, fmt.Sprintf("Feature %q would be %s.", feature, verb), before, want, ""), nil
	}
	res, err := authselectRun(c, subcommand, feature)
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, authselectFailure(fmt.Sprintf("`authselect %s %s`", subcommand, feature), res)
	}
	after, err := authselectConfirm(c, func(s authselectSelection) bool { return s.Configured && s.has(feature) == enable })
	if err != nil {
		return nil, err
	}
	return authselectResult(c, true, fmt.Sprintf("Feature %q was %s.", feature, verb), before, after,
		strings.TrimSpace(res.Stdout)), nil
}

// authselectConfirm re-reads `current` after a change and refuses to
// report success unless it says what was asked for. authselect's own
// exit code has already been seen to be 0 for a request it ignored.
func authselectConfirm(c *exec.Context, ok func(authselectSelection) bool) (authselectSelection, error) {
	after, err := authselectCurrent(c)
	if err != nil {
		return after, fmt.Errorf("authselect reported success and its selection could not be read back: %w", err)
	}
	if !ok(after) {
		return after, fmt.Errorf("authselect reported success, and now reports profile %q with features %v",
			after.Profile, after.Features)
	}
	return after, nil
}

func authselectResult(c *exec.Context, changed bool, comment string, before, after authselectSelection, output string) *value.Map {
	out := value.NewMap(5)
	out.Set("changed", changed)
	if c.Test && changed {
		comment += " Nothing was changed: this was a test run."
	}
	out.Set("comment", comment)
	out.Set("old", before.value())
	out.Set("new", after.value())
	if output != "" {
		out.Set("output", output)
	}
	return out
}
