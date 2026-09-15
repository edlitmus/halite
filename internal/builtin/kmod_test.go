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

// `mods` is the real argument and `name` is a placeholder when it is
// present. The estate's own tree says so in a comment beside the state,
// and getting it backwards would unload a module called
// "modules_to_unload" and nothing else -- silently, because no such
// module is loaded.
func TestKmodModsIsTheArgumentAndNameIsAPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name string
		args *value.Map
		want []string
	}{
		{
			name: "mods wins and name is ignored",
			args: value.MapOf("name", "modules_to_unload", "mods", []any{"dccp", "sctp"}),
			want: []string{"dccp", "sctp"},
		},
		{
			name: "name alone is the module, which is Salt's single form",
			args: value.MapOf("name", "dccp"),
			want: []string{"dccp"},
		},
		{
			name: "an empty mods list falls back to name",
			args: value.MapOf("name", "dccp", "mods", []any{}),
			want: []string{"dccp"},
		},
		{
			name: "hyphens and underscores are the same module to the kernel",
			args: value.MapOf("name", "x", "mods", []any{"amba-pl011"}),
			want: []string{"amba_pl011"},
		},
		{
			name: "a modules line may carry parameters, and the module is the first word",
			args: value.MapOf("name", "bonding mode=4 miimon=1000"),
			want: []string{"bonding"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := requestedModules(tc.args)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("requestedModules = %v, want %v", got, tc.want)
			}
		})
	}
}

// withModulesConf points the persistence file at a temporary one.
func withModulesConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "modules")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := ModulesPath
	ModulesPath = path
	t.Cleanup(func() { ModulesPath = old })
	return path
}

// A context with no systemd grain, so modulesConf picks /etc/modules --
// the path the test has just redirected.
func kmodCtx(test bool) *exec.Context {
	c := newCtx(test)
	c.Grains = value.MapOf("os", "Ubuntu", "os_family", "Debian")
	return c
}

// Persisting is what makes the control survive a reboot: a module
// unloaded but left in the configuration passes a scan today and fails
// the same scan after the next restart.
func TestKmodPersistence(t *testing.T) {
	t.Run("a module is written, and reads back", func(t *testing.T) {
		path := withModulesConf(t, "# a comment\nbonding\n")
		c := kmodCtx(false)

		if _, err := persistModule(c, "dccp"); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if !strings.Contains(string(body), "dccp") {
			t.Errorf("the module was not written: %q", body)
		}
		mods, err := persistedModules(c)
		if err != nil {
			t.Fatal(err)
		}
		if !containsString(mods, "dccp") || !containsString(mods, "bonding") {
			t.Errorf("persisted modules = %v", mods)
		}
	})

	t.Run("a commented module is uncommented rather than added twice", func(t *testing.T) {
		path := withModulesConf(t, "# dccp\n")
		c := kmodCtx(false)

		if _, err := persistModule(c, "dccp"); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if n := strings.Count(string(body), "dccp"); n != 1 {
			t.Errorf("dccp appears %d times:\n%s", n, body)
		}
		if strings.Contains(string(body), "# dccp") {
			t.Errorf("it was left commented out:\n%s", body)
		}
	})

	t.Run("removing comments the line out by default", func(t *testing.T) {
		path := withModulesConf(t, "bonding\ndccp\n")
		c := kmodCtx(false)

		if _, err := unpersistModule(c, "dccp", true); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if !strings.Contains(string(body), "# dccp") {
			t.Errorf("the line was not commented:\n%s", body)
		}
		// Commented is not persisted.
		mods, _ := persistedModules(c)
		if containsString(mods, "dccp") {
			t.Errorf("a commented module still counts as persisted: %v", mods)
		}
		if !containsString(mods, "bonding") {
			t.Errorf("an unrelated module was lost: %v", mods)
		}
	})

	t.Run("comment false deletes the line", func(t *testing.T) {
		path := withModulesConf(t, "bonding\ndccp\n")
		c := kmodCtx(false)

		if _, err := unpersistModule(c, "dccp", false); err != nil {
			t.Fatal(err)
		}
		body, _ := os.ReadFile(path)
		if strings.Contains(string(body), "dccp") {
			t.Errorf("the line survived:\n%s", body)
		}
		if !strings.Contains(string(body), "bonding") {
			t.Errorf("an unrelated module was lost:\n%s", body)
		}
	})

	t.Run("a missing configuration file is not an error", func(t *testing.T) {
		withModulesConf(t, "")
		c := kmodCtx(false)
		mods, err := persistedModules(c)
		if err != nil {
			t.Fatalf("a file that is not there should read as empty: %v", err)
		}
		if len(mods) != 0 {
			t.Errorf("persisted modules = %v", mods)
		}
	})
}

// The state converges and does not touch a module that is already in the
// state asked for.
func TestKmodAbsentIsANoOpForAModuleThatIsNotLoaded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kmod is Linux only")
	}
	withModulesConf(t, "")
	r := New()

	// A name no kernel has, so it cannot be loaded and cannot be
	// persisted.
	args := value.MapOf("name", "placeholder", "mods", []any{"halite_no_such_module"})
	res, err := r.States.Call(kmodCtx(false), "kmod.absent", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Errorf("it failed for a module that is already absent: %q", res.Comment)
	}
	if res.HasChanges() {
		t.Errorf("it reported a change: %v", res.Changes.StringKeys())
	}
	if !strings.Contains(res.Comment, "already absent") {
		t.Errorf("comment = %q", res.Comment)
	}
}

// And it refuses by name where it cannot work, rather than reporting
// success on a platform whose modules it never touched.
func TestKmodRefusesOnPlatformsWithoutIt(t *testing.T) {
	res, stop := kmodUnsupported()
	if runtime.GOOS == "linux" {
		if stop {
			t.Fatal("kmod refused itself on Linux")
		}
		return
	}
	if !stop || res.Succeeded() {
		t.Error("kmod reported success on a platform it does not manage")
	}
}

// Persisting is part of "absent" when the tree asks for it: the estate's
// CIS control writes `persist: True`, and a module removed from the
// running kernel but left in /etc/modules comes back at the next boot.
func TestKmodAbsentWithPersistTakesItOutOfTheFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kmod is Linux only")
	}
	path := withModulesConf(t, "bonding\nhalite_no_such_module\n")
	r := New()

	args := value.MapOf("name", "placeholder",
		"mods", []any{"halite_no_such_module"}, "persist", true)
	res, err := r.States.Call(kmodCtx(false), "kmod.absent", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("%q", res.Comment)
	}
	if !res.HasChanges() {
		t.Error("a module written in the configuration was reported as already absent")
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "# halite_no_such_module") {
		t.Errorf("it was not commented out:\n%s", body)
	}

	// And now it converges.
	res, err = r.States.Call(kmodCtx(false), "kmod.absent", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %q", res.Comment)
	}
}

// Test mode says what it would do and does none of it.
func TestKmodAbsentTestModeChangesNothing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kmod is Linux only")
	}
	path := withModulesConf(t, "halite_no_such_module\n")
	r := New()

	args := value.MapOf("name", "placeholder",
		"mods", []any{"halite_no_such_module"}, "persist", true)
	res, err := r.States.Call(kmodCtx(true), "kmod.absent", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Error("test mode predicted no change for a persisted module")
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "#") {
		t.Errorf("test mode edited the file:\n%s", body)
	}
}
