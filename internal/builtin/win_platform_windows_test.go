package builtin

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winreg"
)

// ---- win_registry ----

// scratchKey is a key under the current user's hive, which needs no
// administrator rights and is removed when the test finishes.
func scratchKey(t *testing.T) string {
	t.Helper()
	const path = `Software\halite-module-test`
	if _, err := winreg.CreateKey("HKCU", path, winreg.Native); err != nil {
		t.Fatalf("creating the scratch key: %v", err)
	}
	t.Cleanup(func() { _ = winreg.DeleteKeyRecursive("HKCU", path, winreg.Native) })
	return path
}

// A value written through the module is read back through the module,
// with its type.
func TestWinRegistryWritesAndReadsThroughTheModule(t *testing.T) {
	r := New()
	c := realCtx(t)
	key := scratchKey(t)

	_, err := r.Exec.Call(c, "win_registry.set_value", value.MapOf(
		"hive", "HKCU", "key", key, "vname", "Port", "vdata", int64(8080), "vtype", "dword"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := r.Exec.Call(c, "win_registry.read_value", value.MapOf(
		"hive", "HKCU", "key", key, "vname", "Port"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(*value.Map)
	if !ok {
		t.Fatalf("read_value returned %#v", got)
	}
	if kind, _ := m.Get("type"); kind != "dword" {
		t.Errorf("type = %#v, want dword", kind)
	}
	if data, _ := m.Get("data"); data != int64(8080) {
		t.Errorf("data = %#v, want 8080", data)
	}

	exists, err := r.Exec.Call(c, "win_registry.value_exists", value.MapOf(
		"hive", "HKCU", "key", key, "vname", "Port"))
	if err != nil {
		t.Fatal(err)
	}
	if exists != true {
		t.Errorf("value_exists = %#v", exists)
	}

	// The listing carries the data, not just the names.
	listed, err := r.Exec.Call(c, "win_registry.list_values", value.MapOf("hive", "HKCU", "key", key))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := listed.([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("list_values returned %#v", listed)
	}
	first, _ := items[0].(*value.Map)
	if first == nil {
		t.Fatalf("a listed value is %#v", items[0])
	}
	if data, _ := first.Get("data"); data != int64(8080) {
		t.Errorf("the listing dropped the data: %#v", data)
	}

	if _, err := r.Exec.Call(c, "win_registry.delete_value", value.MapOf(
		"hive", "HKCU", "key", key, "vname", "Port")); err != nil {
		t.Fatal(err)
	}
	exists, err = r.Exec.Call(c, "win_registry.value_exists", value.MapOf(
		"hive", "HKCU", "key", key, "vname", "Port"))
	if err != nil {
		t.Fatal(err)
	}
	if exists != false {
		t.Errorf("the value survived being deleted")
	}
}

// Test mode changes nothing. An operator runs --test precisely because
// they are not ready to write to the registry.
func TestWinRegistryInTestModeWritesNothing(t *testing.T) {
	r := New()
	key := scratchKey(t)

	_, err := r.Exec.Call(testCtx(t), "win_registry.set_value", value.MapOf(
		"hive", "HKCU", "key", key, "vname", "NotWritten", "vdata", "x"))
	if err != nil {
		t.Fatal(err)
	}
	exists, err := winreg.ValueExists("HKCU", key, "NotWritten", winreg.Native)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("test mode wrote a value")
	}
}

// A hive that is not one is refused by name, listing the real ones.
func TestWinRegistryRefusesAHiveThatIsNotOne(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(realCtx(t), "win_registry.read_value", value.MapOf(
		"hive", "HKEY_INVENTED", "key", "x", "vname", "y"))
	if err == nil {
		t.Fatal("an invented hive was accepted")
	}
	if !strings.Contains(err.Error(), "HKLM") {
		t.Errorf("the error does not list the real hives: %v", err)
	}
}

// ---- win_task ----

// The listing reads the machine's real tasks through the module.
func TestWinTaskListsThroughTheModule(t *testing.T) {
	r := New()
	got, err := r.Exec.Call(realCtx(t), "win_task.list", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	names, ok := got.([]any)
	if !ok || len(names) < 10 {
		t.Fatalf("win_task.list returned %d entries", len(names))
	}
}

// win_task.present registers a task, converges on a second run, and
// win_task.absent removes it.
//
// As the account running the test rather than SYSTEM: registering a
// SYSTEM task needs administrator rights, and a test that skips on every
// developer machine is a test that never runs.
func TestWinTaskPresentRegistersAndThenConverges(t *testing.T) {
	r := New()
	c := realCtx(t)
	const path = `\halite-module-test`

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	args := value.MapOf(
		"name", path,
		"command", filepath.Join(`C:\Windows\System32`, "cmd.exe"),
		"arguments", "/c exit 0",
		"run_as", me.Username,
		"run_level", "limited",
		"trigger", "daily at 04:15",
		"description", "written by halite's tests",
	)
	t.Cleanup(func() {
		_, _ = r.Exec.Call(realCtx(t), "win_task.delete", value.MapOf("name", path))
	})

	// Test mode predicts and registers nothing.
	predicted, err := r.States.Call(testCtx(t), "win_task.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if predicted.Result != nil {
		t.Errorf("test mode should predict, not decide: %+v", predicted)
	}
	if predicted.Changes.Len() == 0 {
		t.Error("test mode predicted no change for a task that is not there")
	}
	present, err := r.Exec.Call(c, "win_task.exists", value.MapOf("name", path))
	if err != nil {
		t.Fatal(err)
	}
	if present == true {
		t.Fatal("test mode registered the task")
	}

	first, err := r.States.Call(c, "win_task.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result == nil || !*first.Result {
		if strings.Contains(first.Comment, "denied") || strings.Contains(first.Comment, "Access") {
			t.Skip("registering a scheduled task needs rights this process does not have")
		}
		t.Fatalf("the state failed: %+v", first)
	}
	if first.Changes.Len() == 0 {
		t.Error("registering a task reported no change")
	}

	// The second run reports nothing, which is what makes it usable in a
	// highstate. The scheduler stores the principal as a SID rather than
	// the name it was given, so this is also what catches a comparison
	// that would never converge.
	second, err := r.States.Call(c, "win_task.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if second.Changes.Len() != 0 {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
	if !strings.Contains(second.Comment, "already") {
		t.Errorf("comment = %q", second.Comment)
	}

	// A declaration that differs is a change.
	changed := value.MapOf(
		"name", path,
		"command", filepath.Join(`C:\Windows\System32`, "cmd.exe"),
		"arguments", "/c exit 1",
		"run_as", me.Username,
		"run_level", "limited",
		"trigger", "daily at 04:15",
	)
	third, err := r.States.Call(c, "win_task.present", changed)
	if err != nil {
		t.Fatal(err)
	}
	if third.Changes.Len() == 0 {
		t.Error("a different declaration reported no change")
	}

	// And absent removes it, then converges.
	gone, err := r.States.Call(c, "win_task.absent", value.MapOf("name", path))
	if err != nil {
		t.Fatal(err)
	}
	if gone.Changes.Len() == 0 {
		t.Error("removing a registered task reported no change")
	}
	again, err := r.States.Call(c, "win_task.absent", value.MapOf("name", path))
	if err != nil {
		t.Fatal(err)
	}
	if again.Changes.Len() != 0 {
		t.Errorf("removing a task that is gone reported changes: %+v", again.Changes)
	}
}

// A state with neither a command nor XML says what it is missing rather
// than registering a task that does nothing.
func TestWinTaskPresentNeedsACommand(t *testing.T) {
	r := New()
	res, err := r.States.Call(realCtx(t), "win_task.present",
		value.MapOf("name", `\halite-nothing`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Fatalf("a task with no command was accepted: %+v", res)
	}
	if !strings.Contains(res.Comment, "command") {
		t.Errorf("the refusal does not say what is missing: %q", res.Comment)
	}
}
