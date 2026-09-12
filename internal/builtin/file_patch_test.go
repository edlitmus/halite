package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The six `file` functions SPEC 15.2 names that this build did not have.
//
// Every one of them is exercised against a real file on a real
// filesystem, and `patch` against the real `patch` binary, because that
// is what each of them talks to. No root is involved: a temporary
// directory is the whole of the machinery.

func fileCtx(t *testing.T, cacheDir string) *hexec.Context {
	t.Helper()
	c := &hexec.Context{}
	if cacheDir != "" {
		c.Config = value.MapOf("cache_dir", cacheDir)
	}
	c.Dispatch = beaconDispatcher{New().Exec}
	return c
}

func patchCall(t *testing.T, c *hexec.Context, fn string, kv ...any) any {
	t.Helper()
	args := value.NewMap(len(kv) / 2)
	for i := 0; i+1 < len(kv); i += 2 {
		args.Set(kv[i].(string), kv[i+1])
	}
	out, err := New().Exec.Call(c, fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func contentsOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestFilePatchAppliesARealPatch(t *testing.T) {
	c := fileCtx(t, "")
	if c.Which("patch") == "" {
		t.Skip("this host has no patch binary")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "config")
	patch := filepath.Join(dir, "change.diff")

	writeFile(t, target, "one\ntwo\nthree\n")
	writeFile(t, patch, strings.Join([]string{
		"--- config", "+++ config",
		"@@ -1,3 +1,3 @@", " one", "-two", "+TWO", " three", "",
	}, "\n"))

	// Test mode runs `patch --dry-run`, so the contract of SPEC 11.6 is
	// met by the tool rather than by a second opinion about what a patch
	// would do.
	out := patchCall(t, &hexec.Context{Test: true}, "file.patch", "name", target, "source", patch).(*value.Map)
	if applied, _ := out.Get("applied"); applied != false {
		t.Errorf("test mode reported applied=%v", applied)
	}
	if dry, _ := out.Get("dry_run"); dry != true {
		t.Errorf("test mode did not run a dry run: %v", out)
	}
	if got := contentsOf(t, target); got != "one\ntwo\nthree\n" {
		t.Fatalf("test mode changed the file: %q", got)
	}

	out = patchCall(t, c, "file.patch", "name", target, "source", patch).(*value.Map)
	if applied, _ := out.Get("applied"); applied != true {
		t.Errorf("the patch reported applied=%v: %v", applied, out)
	}
	if got := contentsOf(t, target); got != "one\nTWO\nthree\n" {
		t.Errorf("the file reads %q", got)
	}

	// Applying it again must not undo it. Left to itself `patch` asks
	// "Reversed (or previously applied) patch detected! Assume -R?",
	// takes its own default answer with no terminal, reverses the
	// change and exits 0 -- so a state run's second pass would quietly
	// revert the first. `--forward` is what stops that, and this is the
	// assertion that would catch its removal.
	_, err := New().Exec.Call(c, "file.patch", value.MapOf("name", target, "source", patch))
	if err == nil {
		t.Fatal("a patch that was already applied reported success")
	}
	if got := contentsOf(t, target); got != "one\nTWO\nthree\n" {
		t.Fatalf("re-applying the patch changed the file to %q", got)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "previously applied") {
		t.Errorf("the refusal does not carry patch's own words: %v", err)
	}

	// And it leaves no reject file beside a file a tree is managing.
	if _, err := os.Stat(target + ".rej"); !os.IsNotExist(err) {
		t.Errorf("a .rej file was left beside %s", target)
	}
}

func TestFileSedReplacesOncePerLineAndHonoursItsLimit(t *testing.T) {
	c := fileCtx(t, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")

	// The second `old` on the first line is what `g` decides, and the
	// third line is what `limit` decides.
	writeFile(t, path, "alpha old old\nbeta old\ngamma old\n")

	out := patchCall(t, c, "file.sed",
		"path", path, "before", "old", "after", "new", "limit", "^alpha").(*value.Map)
	if changed, _ := out.Get("lines_changed"); changed != int64(1) {
		t.Errorf("the limit let %v lines through", changed)
	}
	if got := contentsOf(t, path); got != "alpha new old\nbeta old\ngamma old\n" {
		t.Fatalf("without `g` and with a limit, the file reads %q", got)
	}

	// `g` takes the rest of the first line.
	patchCall(t, c, "file.sed",
		"path", path, "before", "old", "after", "new", "limit", "^alpha", "flags", []any{"g"})
	if got := contentsOf(t, path); got != "alpha new new\nbeta old\ngamma old\n" {
		t.Fatalf("with `g`, the file reads %q", got)
	}

	// No limit reaches every line, and a replacement that changes
	// nothing says so rather than rewriting the file.
	patchCall(t, c, "file.sed", "path", path, "before", "old", "after", "new")
	if got := contentsOf(t, path); got != "alpha new new\nbeta new\ngamma new\n" {
		t.Fatalf("without a limit, the file reads %q", got)
	}
	out = patchCall(t, c, "file.sed", "path", path, "before", "nothing-matches", "after", "x").(*value.Map)
	if changed, _ := out.Get("changed"); changed != false {
		t.Errorf("a pattern that matches nothing reported %v", changed)
	}
}

func TestFileSedKeepsABackupAndCanBeRehearsed(t *testing.T) {
	c := fileCtx(t, "")
	path := filepath.Join(t.TempDir(), "conf")
	writeFile(t, path, "port 80\n")

	// Test mode says what it would do and does nothing.
	out := patchCall(t, &hexec.Context{Test: true}, "file.sed",
		"path", path, "before", "80", "after", "443").(*value.Map)
	if changed, _ := out.Get("changed"); changed != true {
		t.Errorf("test mode reported changed=%v", changed)
	}
	if got := contentsOf(t, path); got != "port 80\n" {
		t.Fatalf("test mode changed the file: %q", got)
	}

	patchCall(t, c, "file.sed", "path", path, "before", "80", "after", "443", "backup", ".orig")
	if got := contentsOf(t, path); got != "port 443\n" {
		t.Errorf("the file reads %q", got)
	}
	if got := contentsOf(t, path+".orig"); got != "port 80\n" {
		t.Errorf("the backup reads %q", got)
	}
}

func TestSeekReadAndSeekWriteTouchOnlyWhatTheyName(t *testing.T) {
	c := fileCtx(t, "")
	path := filepath.Join(t.TempDir(), "blob")
	writeFile(t, path, "0123456789")

	if got := patchCall(t, c, "file.seek_read", "path", path, "size", int64(3), "offset", int64(4)); got != "456" {
		t.Errorf("seek_read returned %q", got)
	}
	// Asking past the end returns what is there rather than failing: a
	// caller reading a tail does not know the length.
	if got := patchCall(t, c, "file.seek_read", "path", path, "size", int64(100), "offset", int64(8)); got != "89" {
		t.Errorf("a read past the end returned %q", got)
	}

	// Test mode writes nothing.
	patchCall(t, &hexec.Context{Test: true}, "file.seek_write", "path", path, "data", "XX", "offset", int64(0))
	if got := contentsOf(t, path); got != "0123456789" {
		t.Fatalf("test mode wrote: %q", got)
	}

	if n := patchCall(t, c, "file.seek_write", "path", path, "data", "abc", "offset", int64(2)); n != int64(3) {
		t.Errorf("seek_write reported %v bytes", n)
	}
	if got := contentsOf(t, path); got != "01abc56789" {
		t.Errorf("the file reads %q; a seek write must leave the rest alone", got)
	}
}

func TestTheBackupCacheListsAndRestores(t *testing.T) {
	cache := t.TempDir()
	c := fileCtx(t, cache)
	path := filepath.Join(t.TempDir(), "motd")
	writeFile(t, path, "first\n")

	// Nothing kept yet is an empty list, not an error: "has this ever
	// been backed up" is a question a template asks.
	if got := patchCall(t, c, "file.list_backups", "path", path).([]any); len(got) != 0 {
		t.Fatalf("a file with no backups listed %v", got)
	}

	// Two keeps, through the state's own argument.
	for _, contents := range []string{"second\n", "third\n"} {
		args := value.MapOf("name", path, "contents", contents, "backup", "node")
		out, err := runFileManaged(t, c, args)
		if err != nil {
			t.Fatalf("file.managed: %v", err)
		}
		if !out {
			t.Fatalf("file.managed did not change %s", path)
		}
	}

	listed := patchCall(t, c, "file.list_backups", "path", path).([]any)
	if len(listed) != 2 {
		t.Fatalf("listed %d backups, want 2: %v", len(listed), listed)
	}
	newest := listed[0].(*value.Map)
	oldest := listed[1].(*value.Map)
	newestID, _ := newest.Get("id")
	oldestID, _ := oldest.Get("id")
	if value.KeyString(newestID) <= value.KeyString(oldestID) {
		t.Errorf("the list is not newest first: %v then %v", newestID, oldestID)
	}
	if got := contentsOf(t, value.KeyString(patchMustGet(t, oldest, "path"))); got != "first\n" {
		t.Errorf("the oldest backup holds %q", got)
	}

	// Restoring puts the named copy back and keeps the current contents
	// first, so the wrong choice is undoable.
	out := patchCall(t, c, "file.restore_backup",
		"path", path, "backup_id", value.KeyString(oldestID)).(*value.Map)
	if got := contentsOf(t, path); got != "first\n" {
		t.Errorf("after the restore the file reads %q", got)
	}
	if _, has := out.Get("kept"); !has {
		t.Errorf("the restore kept nothing: %v", out)
	}
	if got := patchCall(t, c, "file.list_backups", "path", path).([]any); len(got) != 3 {
		t.Errorf("after a restore there are %d backups, want 3", len(got))
	}

	// A backup identifier is a name in the cache and nothing else.
	for _, bad := range []string{"../../etc/passwd", "..", "sub/dir"} {
		if _, err := New().Exec.Call(c, "file.restore_backup",
			value.MapOf("path", path, "backup_id", bad)); err == nil {
			t.Errorf("%q was accepted as a backup identifier", bad)
		}
	}
}

// A node with no cache directory keeps no backups, and says so rather
// than writing them somewhere relative to whatever the working
// directory happens to be.
func TestTheBackupCacheRefusesWithoutACacheDirectory(t *testing.T) {
	c := fileCtx(t, "")
	_, err := New().Exec.Call(c, "file.list_backups", value.MapOf("path", "/etc/motd"))
	if err == nil {
		t.Fatal("a node with no cache_dir listed backups")
	}
	if !strings.Contains(err.Error(), "cache_dir") {
		t.Errorf("the refusal reads %q", err)
	}
}

func patchMustGet(t *testing.T, m *value.Map, key string) any {
	t.Helper()
	v, ok := m.Get(key)
	if !ok {
		t.Fatalf("no %q in %v", key, m.StringKeys())
	}
	return v
}

// runFileManaged applies the state and reports whether it changed
// anything.
func runFileManaged(t *testing.T, c *hexec.Context, args *value.Map) (bool, error) {
	t.Helper()
	mod, ok := New().States.Lookup("file.managed")
	if !ok {
		t.Fatal("no file.managed state is registered")
	}
	res, err := mod.Fn(c, args)
	if err != nil {
		return false, err
	}
	if res.Result == nil || !*res.Result {
		return false, fmt.Errorf("file.managed failed: %s", res.Comment)
	}
	return res.Changes != nil && res.Changes.Len() > 0, nil
}
