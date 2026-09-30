package builtin

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// authselectHere skips anywhere authselect cannot be asked, saying which
// of the reasons it was.
func authselectHere(t *testing.T) (*Registries, *exec.Context) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("authselect is Fedora and RHEL 8+; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("authselect") == "" {
		t.Skip("this host has no authselect: it ships on Fedora and RHEL 8+, not on Debian, Ubuntu or SUSE")
	}
	return New(), c
}

func authselectLiveCall(t *testing.T, r *Registries, c *exec.Context, fn string, args *value.Map) *value.Map {
	t.Helper()
	if args == nil {
		args = value.NewMap(0)
	}
	out, err := r.Exec.Call(c, fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("%s returned %T, want *value.Map", fn, out)
	}
	return m
}

func authselectChanged(t *testing.T, result *value.Map, want bool, what string) {
	t.Helper()
	if changed, _ := result.Get("changed"); changed != want {
		comment, _ := result.GetString("comment")
		t.Fatalf("%s: changed = %v, want %v (%v)", what, changed, want, comment)
	}
}

// TestLiveAuthselectReads asks the real tool every read question and
// checks that the two ways of asking "is this node configured" agree.
// No root and no gate: none of these writes anything.
func TestLiveAuthselectReads(t *testing.T) {
	r, c := authselectHere(t)

	current := authselectLiveCall(t, r, c, "authselect.current", nil)
	check := authselectLiveCall(t, r, c, "authselect.check", nil)
	configured, _ := current.Get("configured")
	checkConfigured, _ := check.Get("configured")
	if configured != checkConfigured {
		t.Errorf("current says configured=%v and check says configured=%v; one of the two exit codes is misread",
			configured, checkConfigured)
	}
	if profile, _ := current.GetString("profile"); configured == false && profile != "" {
		t.Errorf("an unconfigured node reported profile %q -- the sentence `current --raw` prints was read as a profile", profile)
	}

	listed, err := r.Exec.Call(c, "authselect.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("authselect.list: %v", err)
	}
	profiles, _ := listed.([]any)
	ids := []string{}
	for _, p := range profiles {
		id, _ := p.(*value.Map).GetString("id")
		ids = append(ids, id.(string))
	}
	if !slices.Contains(ids, "minimal") {
		t.Fatalf("authselect.list = %v, want the stock minimal profile among them", ids)
	}
	features, err := r.Exec.Call(c, "authselect.list_features", value.MapOf("profile", "minimal"))
	if err != nil {
		t.Fatalf("authselect.list_features: %v", err)
	}
	if !slices.Contains(anyStrings(features), "with-silent-lastlog") {
		t.Errorf("minimal's features = %v, want with-silent-lastlog among them", features)
	}
	if _, err := r.Exec.Call(c, "authselect.list_features", value.MapOf("profile", "no-such-profile")); err == nil {
		t.Error("list_features of a profile that does not exist was not refused")
	}
	if _, err := r.Exec.Call(c, "authselect.backup_list", value.NewMap(0)); err != nil {
		t.Errorf("authselect.backup_list: %v", err)
	}
}

// TestLiveAuthselectSelectAndFeatures drives every mutating function
// against the real tool, and rewrites this host's /etc/pam.d and
// nsswitch.conf to do it.
//
// It only uses the stock `minimal` profile and two features that touch
// nothing an ssh login with a key depends on: with-silent-lastlog
// changes one pam_lastlog argument in postlogin, and with-pwhistory adds
// two lines to the password chain, which a key login never reaches.
// Every step was first taken by hand on both lab hosts with a fresh ssh
// login confirmed after it (DIVERGENCE 5.172) before it was put here.
//
// A node authselect has never configured is only taken over when
// HALITE_AUTHSELECT_TAKEOVER=1 is also set, because authselect has no
// way back from that in 1.2.6 -- `opt-out` arrived in 1.3 -- and the
// test would leave the node on `minimal`. A node that is configured is
// put back on the profile and features it started with. A node whose
// generated files were already edited by hand is skipped: `force`
// would discard somebody's edit.
func TestLiveAuthselectSelectAndFeatures(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this rewrite a real /etc/pam.d through authselect")
	}
	if os.Geteuid() != 0 {
		t.Skip("authselect select needs root")
	}
	r, c := authselectHere(t)

	before := authselectLiveCall(t, r, c, "authselect.current", nil)
	wasConfigured, _ := before.Get("configured")
	startProfile, _ := before.GetString("profile")
	startFeaturesRaw, _ := before.Get("features")
	startFeatures := anyStrings(startFeaturesRaw)

	if wasConfigured == true {
		check := authselectLiveCall(t, r, c, "authselect.check", nil)
		if valid, _ := check.Get("valid"); valid != true {
			t.Skipf("this node's authselect files were edited outside it (%v); not overwriting them", check)
		}
		defer func() {
			restore := value.MapOf("profile", startProfile, "features", toAnyList(startFeatures))
			authselectLiveCall(t, r, c, "authselect.select", restore)
			t.Logf("put back on profile %q with features %v", startProfile, startFeatures)
		}()
	} else {
		if os.Getenv("HALITE_AUTHSELECT_TAKEOVER") != "1" {
			t.Skip("authselect has never configured this node, and 1.2.6 has no opt-out to undo it; " +
				"set HALITE_AUTHSELECT_TAKEOVER=1 to let the test leave it on the minimal profile")
		}
		// Without force the real tool refuses, because the files are
		// not its own, and the refusal must reach the caller.
		_, err := r.Exec.Call(c, "authselect.select", value.MapOf("profile", "minimal"))
		if err == nil || !strings.Contains(err.Error(), "force: true") {
			t.Fatalf("select on an unconfigured node without force: err = %v, want the refusal naming force", err)
		}
		if still := authselectLiveCall(t, r, c, "authselect.current", nil); mustGet(still, "configured") != false {
			t.Fatalf("a refused select changed the node: %v", still)
		}
		authselectChanged(t, authselectLiveCall(t, r, c, "authselect.select",
			value.MapOf("profile", "minimal", "force", true)), true, "select minimal --force on an unconfigured node")
		defer t.Log("this node was unconfigured and is left on the minimal profile; " +
			"authselect.backup_list names the backup of what was there")
	}

	minimal := value.MapOf("profile", "minimal")
	authselectLiveCall(t, r, c, "authselect.select", minimal)

	// A repeat select must not run authselect at all: the tool rewrites
	// every file on a repeat, so an unchanged mtime is the proof.
	generated := "/etc/authselect/system-auth"
	stamp := func() time.Time {
		info, err := os.Stat(generated)
		if err != nil {
			t.Fatal(err)
		}
		return info.ModTime()
	}
	first := stamp()
	time.Sleep(1100 * time.Millisecond)
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.select", minimal), false, "repeat select minimal")
	if !stamp().Equal(first) {
		t.Fatalf("a repeat select rewrote %s; it should not have asked authselect at all", generated)
	}

	enable := value.MapOf("feature", "with-silent-lastlog")
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.enable_feature", enable), true, "enable with-silent-lastlog")
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.enable_feature", enable), false, "enable it again")

	both := value.MapOf("profile", "minimal", "features", []any{"with-pwhistory", "with-silent-lastlog"})
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.select", both), true, "select with two features")
	reordered := value.MapOf("profile", "minimal", "features", []any{"with-silent-lastlog", "with-pwhistory"})
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.select", reordered), false,
		"the same two features in the other order")

	disable := value.MapOf("feature", "with-pwhistory")
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.disable_feature", disable), true, "disable with-pwhistory")
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.disable_feature", disable), false, "disable it again")
	// authselect itself exits 0 for this; the module must not call it a change.
	authselectChanged(t, authselectLiveCall(t, r, c, "authselect.disable_feature",
		value.MapOf("feature", "no-such-feature")), false, "disable a feature that does not exist")
	if _, err := r.Exec.Call(c, "authselect.enable_feature", value.MapOf("feature", "no-such-feature")); err == nil {
		t.Error("enabling a feature that does not exist was not refused")
	}

	after := authselectLiveCall(t, r, c, "authselect.current", nil)
	if got := anyStrings(mustGet(after, "features")); !slices.Equal(got, []string{"with-silent-lastlog"}) {
		t.Errorf("features after the run = %v, want [with-silent-lastlog]", got)
	}
	if check := authselectLiveCall(t, r, c, "authselect.check", nil); mustGet(check, "valid") != true {
		t.Errorf("check after the run = %v, want valid", check)
	}
	authselectLiveCall(t, r, c, "authselect.select", minimal)
}

func mustGet(m *value.Map, key string) any {
	v, _ := m.Get(key)
	return v
}
