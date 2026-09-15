package builtin

import (
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// An estate's tree writes `saltutil.sync_all` as a state, and this build
// had the execution functions without the states -- so a tree carrying
// one did not compile. It was written down here as a refusal, on the
// reasoning that sync_all ships Python to a node and this model has no
// such step. That described Salt's meaning rather than this build's:
// SPEC 24.5 maps these names onto fetching signed, pinned bundles, and
// the execution half has done that all along.
func TestSaltutilSyncStatesExist(t *testing.T) {
	r := New()
	for _, name := range []string{
		"saltutil.sync_all", "saltutil.sync_modules", "saltutil.sync_states",
		"saltutil.sync_grains", "saltutil.sync_beacons", "saltutil.sync_returners",
		"saltutil.sync_renderers",
	} {
		if !r.States.Has(name) {
			t.Errorf("%s is not a state; an estate's tree writes it", name)
		}
		if !r.Exec.Has(name) {
			t.Errorf("%s is not an execution function either", name)
		}
	}
}

// syncCtx is a context whose extension synchronisation is scripted, so
// these assert what the state does with a report rather than fetching
// anything.
func syncCtx(t *testing.T, test bool, report any, err error) *exec.Context {
	t.Helper()
	c := newCtx(test)
	c.SyncExtensions = func([]string) (any, error) { return report, err }
	return c
}

func TestSaltutilSyncAllReportsWhatWasFetched(t *testing.T) {
	r := New()

	t.Run("nothing to fetch is a success with no change", func(t *testing.T) {
		report := value.MapOf("changed", false, "extensions", []any{})
		res, err := r.States.Call(syncCtx(t, false, report, nil), "saltutil.sync_all", value.MapOf("name", "sync all"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() {
			t.Fatalf("%q", res.Comment)
		}
		if res.HasChanges() {
			t.Errorf("it reported a change: %v", res.Changes.StringKeys())
		}
	})

	// The `onchanges` in the estate's own extmods.sls hangs off this
	// state, so a fetch that changed something has to say so or the
	// requisite never fires.
	t.Run("a fetch is a change, which is what onchanges needs", func(t *testing.T) {
		entry := value.MapOf("name", "aws_secrets_manager", "version", "1.0.0", "status", "fetched")
		report := value.MapOf("changed", true, "extensions", []any{entry})
		res, err := r.States.Call(syncCtx(t, false, report, nil), "saltutil.sync_all", value.MapOf("name", "sync all"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("ok=%v changes=%v comment=%q", res.Succeeded(), res.HasChanges(), res.Comment)
		}
		if _, ok := res.Changes.Get("extensions"); !ok {
			t.Errorf("the change does not name what was fetched: %v", res.Changes.StringKeys())
		}
		if !strings.Contains(res.Comment, "restart") {
			t.Errorf("the comment does not say the bundles are not loaded yet: %q", res.Comment)
		}
	})

	t.Run("a node with no file server says so rather than succeeding", func(t *testing.T) {
		c := newCtx(false)
		c.SyncExtensions = nil
		res, err := r.States.Call(c, "saltutil.sync_all", value.MapOf("name", "sync all"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Error("it reported success with nowhere to fetch from")
		}
	})

	t.Run("a failed fetch fails the state", func(t *testing.T) {
		res, err := r.States.Call(syncCtx(t, false, nil, errors.New("the hub refused")),
			"saltutil.sync_all", value.MapOf("name", "sync all"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Error("a refused fetch reported success")
		}
		if !strings.Contains(res.Comment, "refused") {
			t.Errorf("the comment does not carry the reason: %q", res.Comment)
		}
	})

	// Fetching is the change, so test mode cannot find out what would
	// happen without doing it. It says so rather than guessing.
	t.Run("test mode fetches nothing", func(t *testing.T) {
		fetched := false
		c := newCtx(true)
		c.SyncExtensions = func([]string) (any, error) {
			fetched = true
			return value.MapOf("changed", false, "extensions", []any{}), nil
		}
		res, err := r.States.Call(c, "saltutil.sync_all", value.MapOf("name", "sync all"))
		if err != nil {
			t.Fatal(err)
		}
		if fetched {
			t.Error("test mode fetched")
		}
		if !res.HasChanges() {
			t.Error("test mode predicted no change; it cannot know that without fetching")
		}
		if !strings.Contains(res.Comment, "without fetching") {
			t.Errorf("the comment does not say why it cannot tell: %q", res.Comment)
		}
	})
}

// Each sync_* state asks for its own kind, or the whole set for
// sync_all. Getting this wrong would fetch the wrong bundles silently.
func TestSaltutilSyncStatesPassTheirKind(t *testing.T) {
	r := New()
	for _, tc := range []struct {
		state string
		want  []string
	}{
		{"saltutil.sync_all", nil},
		{"saltutil.sync_modules", []string{"module"}},
		{"saltutil.sync_grains", []string{"grain"}},
		{"saltutil.sync_renderers", []string{"renderer"}},
	} {
		t.Run(tc.state, func(t *testing.T) {
			var got []string
			c := newCtx(false)
			c.SyncExtensions = func(kinds []string) (any, error) {
				got = kinds
				return value.MapOf("changed", false, "extensions", []any{}), nil
			}
			if _, err := r.States.Call(c, tc.state, value.MapOf("name", "x")); err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("%s asked for kinds %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}
