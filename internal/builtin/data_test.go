package builtin

import (
	"path/filepath"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// dataRegistry builds a registry holding only the `data` module: the
// three modules this task adds are not wired into builtin.New() yet
// (that wiring, and the ledger entry, belong to whoever integrates
// them), so a test that dispatched through New() would see "unknown
// function" for every one of them.
func dataRegistry() *Registries {
	r := &Registries{Exec: hexec.NewRegistry(), States: states.NewRegistry()}
	registerData(r)
	return r
}

// dataCtx builds a context whose cache_dir is a fresh temporary
// directory, so each test starts from a store that has never been
// written.
func dataCtx(t *testing.T) *hexec.Context {
	t.Helper()
	return fileCtx(t, t.TempDir())
}

func dataCall(t *testing.T, c *hexec.Context, fn string, kv ...any) any {
	t.Helper()
	args := value.NewMap(len(kv) / 2)
	for i := 0; i+1 < len(kv); i += 2 {
		args.Set(kv[i].(string), kv[i+1])
	}
	out, err := dataRegistry().Exec.Call(c, fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func dataCallErr(t *testing.T, c *hexec.Context, fn string, kv ...any) error {
	t.Helper()
	args := value.NewMap(len(kv) / 2)
	for i := 0; i+1 < len(kv); i += 2 {
		args.Set(kv[i].(string), kv[i+1])
	}
	_, err := dataRegistry().Exec.Call(c, fn, args)
	return err
}

func TestAKeyThatWasNeverSetReadsAsAbsent(t *testing.T) {
	c := dataCtx(t)
	if got := dataCall(t, c, "data.has", "key", "colour"); got != false {
		t.Fatalf("has = %v, want false", got)
	}
	if got := dataCall(t, c, "data.get", "key", "colour", "default", "grey"); got != "grey" {
		t.Fatalf("get = %v, want the default", got)
	}
}

func TestASetValueIsReadBackByGet(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "colour", "value", "green")
	if got := dataCall(t, c, "data.get", "key", "colour", "default", nil); got != "green" {
		t.Fatalf("get = %v, want green", got)
	}
	if got := dataCall(t, c, "data.has", "key", "colour"); got != true {
		t.Fatalf("has = %v, want true", got)
	}
}

func TestASetValueSurvivesASecondCallThroughTheRegistry(t *testing.T) {
	// The store is a file, not process memory: a second call, which in
	// the registry test harness is a fresh dataCall but the same
	// cache_dir, must still see what the first call wrote.
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "a", "value", int64(1))
	dataCall(t, c, "data.set", "key", "b", "value", int64(2))
	keys := dataCall(t, c, "data.keys").([]any)
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want 2 entries", keys)
	}
}

func TestSettingAKeyToItsCurrentValueReportsNoChange(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "colour", "value", "green")
	out := dataCall(t, c, "data.set", "key", "colour", "value", "green").(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v, want false for an identical value", changed)
	}
}

func TestSettingAKeyToItsCurrentValueAcrossTypesReportsNoChange(t *testing.T) {
	// A round trip through the JSON store turns an int64 into a
	// json.Number; setting the same number again must not look like a
	// change because of that.
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "count", "value", int64(3))
	out := dataCall(t, c, "data.set", "key", "count", "value", int64(3)).(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v, want false", changed)
	}
}

func TestTestModeSetsNothing(t *testing.T) {
	c := dataCtx(t)
	c.Test = true
	out := dataCall(t, c, "data.set", "key", "colour", "value", "green").(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != true {
		t.Fatalf("changed = %v, want true (predicted)", changed)
	}
	c.Test = false
	if got := dataCall(t, c, "data.has", "key", "colour"); got != false {
		t.Fatalf("has = %v; test mode must not have written anything", got)
	}
}

func TestUpdateMergesWithoutDisturbingOtherKeys(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "a", "value", "1")
	items := value.MapOf("b", "2", "c", "3")
	dataCall(t, c, "data.update", "items", items)
	if got := dataCall(t, c, "data.get", "key", "a", "default", nil); got != "1" {
		t.Fatalf("a = %v, want untouched", got)
	}
	if got := dataCall(t, c, "data.get", "key", "c", "default", nil); got != "3" {
		t.Fatalf("c = %v, want 3", got)
	}
}

func TestUpdateWithNothingNewReportsNoChange(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "a", "value", "1")
	out := dataCall(t, c, "data.update", "items", value.MapOf("a", "1")).(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v, want false", changed)
	}
}

func TestPopRemovesAKeyAndReturnsItsValue(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "colour", "value", "green")
	out := dataCall(t, c, "data.pop", "key", "colour", "default", nil).(*value.Map)
	popped, _ := out.GetString("value")
	if popped != "green" {
		t.Fatalf("popped = %v, want green", popped)
	}
	if got := dataCall(t, c, "data.has", "key", "colour"); got != false {
		t.Fatalf("has = %v after pop, want false", got)
	}
}

func TestPoppingAnAbsentKeyReturnsTheDefaultAndChangesNothing(t *testing.T) {
	c := dataCtx(t)
	out := dataCall(t, c, "data.pop", "key", "missing", "default", "fallback").(*value.Map)
	popped, _ := out.GetString("value")
	if popped != "fallback" {
		t.Fatalf("popped = %v, want fallback", popped)
	}
	changed, _ := out.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v, want false", changed)
	}
}

func TestClearEmptiesEveryKey(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "a", "value", "1")
	dataCall(t, c, "data.set", "key", "b", "value", "2")
	dataCall(t, c, "data.clear")
	keys := dataCall(t, c, "data.keys").([]any)
	if len(keys) != 0 {
		t.Fatalf("keys = %v, want none after clear", keys)
	}
}

func TestClearingAnAlreadyEmptyStoreReportsNoChange(t *testing.T) {
	c := dataCtx(t)
	out := dataCall(t, c, "data.clear").(*value.Map)
	changed, _ := out.GetString("changed")
	if changed != false {
		t.Fatalf("changed = %v, want false", changed)
	}
}

func TestDumpReplacesTheWholeStore(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "old", "value", "gone")
	dataCall(t, c, "data.dump", "items", value.MapOf("fresh", "start"))
	if got := dataCall(t, c, "data.has", "key", "old"); got != false {
		t.Fatalf("old key survived a dump")
	}
	if got := dataCall(t, c, "data.get", "key", "fresh", "default", nil); got != "start" {
		t.Fatalf("fresh = %v, want start", got)
	}
}

func TestLoadAndItemsReturnTheSameWholeStore(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "a", "value", "1")
	load := dataCall(t, c, "data.load").(*value.Map)
	items := dataCall(t, c, "data.items").(*value.Map)
	a1, _ := load.GetString("a")
	a2, _ := items.GetString("a")
	if a1 != "1" || a2 != "1" {
		t.Fatalf("load = %v, items = %v; want both to hold a=1", load, items)
	}
}

func TestANodeWithNoCacheDirRefusesEveryFunction(t *testing.T) {
	c := &hexec.Context{}
	err := dataCallErr(t, c, "data.get", "key", "colour", "default", nil)
	if err == nil {
		t.Fatal("expected an error naming the missing cache_dir")
	}
}

func TestValuesReturnsEveryStoredValue(t *testing.T) {
	c := dataCtx(t)
	dataCall(t, c, "data.set", "key", "a", "value", "1")
	dataCall(t, c, "data.set", "key", "b", "value", "2")
	values := dataCall(t, c, "data.values").([]any)
	if len(values) != 2 {
		t.Fatalf("values = %v, want 2 entries", values)
	}
}

func TestTheStoreFileLivesUnderCacheDir(t *testing.T) {
	dir := t.TempDir()
	c := fileCtx(t, dir)
	dataCall(t, c, "data.set", "key", "a", "value", "1")
	if _, err := dataStorePath(c); err != nil {
		t.Fatal(err)
	}
	path, _ := dataStorePath(c)
	if filepath.Dir(path) != dir {
		t.Fatalf("store path = %s, want it under %s", path, dir)
	}
}
