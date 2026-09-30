package builtin

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// lsattrTool reads a path's flag column with lsattr directly, bypassing
// the module, so each change is checked against the filesystem rather
// than against the module's own reading of it.
func lsattrTool(t *testing.T, path string) string {
	t.Helper()
	out, err := osexec.Command("lsattr", "-d", "--", path).CombinedOutput()
	if err != nil {
		t.Fatalf("lsattr -d %s: %v: %s", path, err, out)
	}
	flags, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return flags
}

func letterList(raw any) string {
	var s strings.Builder
	for _, l := range raw.([]any) {
		s.WriteString(value.KeyString(l))
	}
	return s.String()
}

// TestLiveChattrSetsAndClearsImmutableAndAppend drives chattr.add and
// chattr.remove as root against a real file in a temporary directory,
// setting and clearing `i` and `a` and checking each step with lsattr
// itself and with what the kernel then refuses to let a write do.
//
// A file left immutable cannot be removed, not even by root, so the
// directory's own cleanup would fail and leave it behind. The first
// cleanup registered therefore clears `i` and `a` from everything this
// test made, by running chattr directly -- not through the module under
// test, which may be what broke.
func TestLiveChattrSetsAndClearsImmutableAndAppend(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("chattr is e2fsprogs on Linux; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this set and clear real file attributes")
	}
	if os.Geteuid() != 0 {
		t.Skip("setting `i` and `a` needs CAP_LINUX_IMMUTABLE, which means root")
	}
	for _, tool := range []string{"lsattr", "chattr"} {
		if _, err := osexec.LookPath(tool); err != nil {
			t.Skipf("this host has no %s (e2fsprogs)", tool)
		}
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "target file")
	inner := filepath.Join(dir, "sub", "inner")
	if err := os.MkdirAll(filepath.Dir(inner), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, inner} {
		if err := os.WriteFile(p, []byte("first\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, p := range []string{file, inner, filepath.Dir(inner)} {
			_ = osexec.Command("chattr", "-ia", "--", p).Run()
		}
	})
	if out, err := osexec.Command("lsattr", "-d", "--", file).CombinedOutput(); err != nil {
		t.Skipf("this filesystem has no attributes to set (%s)", strings.TrimSpace(string(out)))
	}

	r := New()
	c := &exec.Context{}
	call := func(fn string, args *value.Map) *value.Map {
		t.Helper()
		out, err := r.Exec.Call(c, fn, args)
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		return out.(*value.Map)
	}
	get := func(path string) string {
		t.Helper()
		m := call("chattr.get", value.MapOf("paths", []any{path}))
		raw, _ := m.Get(path)
		return letterList(raw)
	}

	if got := get(file); strings.ContainsAny(got, "ia") {
		t.Fatalf("a fresh file already has %q", got)
	}

	// Test mode predicts, and changes nothing.
	c.Test = true
	predicted := call("chattr.add", value.MapOf("paths", []any{file}, "attributes", "i"))
	c.Test = false
	if !predicted.Has(file) {
		t.Errorf("test mode predicted no change: %v", predicted)
	}
	if flags := lsattrTool(t, file); strings.Contains(flags, "i") {
		t.Fatalf("test mode set i for real: %s", flags)
	}

	// +a: an append succeeds and a truncating write does not.
	changes := call("chattr.add", value.MapOf("paths", []any{file}, "attributes", "a"))
	if !changes.Has(file) {
		t.Fatalf("chattr.add a reported no change")
	}
	if flags := lsattrTool(t, file); !strings.Contains(flags, "a") {
		t.Fatalf("after chattr.add a, lsattr says %s", flags)
	}
	if f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0); err != nil {
		t.Errorf("appending to an append-only file: %v", err)
	} else {
		_, _ = f.WriteString("appended\n")
		_ = f.Close()
	}
	if err := os.WriteFile(file, []byte("clobbered\n"), 0o644); err == nil {
		t.Error("a truncating write to an append-only file succeeded")
	}

	// +i on top: now nothing may write or remove it.
	changes = call("chattr.add", value.MapOf("paths", []any{file}, "attributes", "ia"))
	raw, _ := changes.Get(file)
	if raw == nil {
		t.Fatal("chattr.add ia reported no change although i was not set")
	}
	if old, _ := raw.(*value.Map).Get("old"); !strings.Contains(letterList(old), "a") {
		t.Errorf("reported old = %v, want it to include the a already set", old)
	}
	if flags := lsattrTool(t, file); !strings.Contains(flags, "i") || !strings.Contains(flags, "a") {
		t.Fatalf("after chattr.add ia, lsattr says %s", flags)
	}
	if f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Error("an immutable file was opened for append")
	}
	if err := os.Remove(file); err == nil {
		t.Fatal("an immutable file was removed")
	}

	// Idempotent: asking again changes nothing and says so.
	if again := call("chattr.add", value.MapOf("paths", []any{file}, "attributes", "i")); again.Len() != 0 {
		t.Errorf("a second chattr.add i reported %v", again)
	}

	// A directory reports its own attributes, not its contents'.
	call("chattr.add", value.MapOf("paths", []any{inner}, "attributes", "a"))
	if got := get(filepath.Dir(inner)); strings.Contains(got, "a") {
		t.Errorf("chattr.get on a directory reported %q, its child's attribute", got)
	}

	// Clear both, and the file is ordinary again.
	changes = call("chattr.remove", value.MapOf("paths", []any{file, inner}, "attributes", "ia"))
	if !changes.Has(file) || !changes.Has(inner) {
		t.Errorf("chattr.remove ia reported %v", changes.StringKeys())
	}
	for _, p := range []string{file, inner} {
		if flags := lsattrTool(t, p); strings.ContainsAny(flags, "ia") {
			t.Errorf("after chattr.remove, lsattr on %s says %s", p, flags)
		}
	}
	if err := os.WriteFile(file, []byte("ordinary\n"), 0o644); err != nil {
		t.Errorf("writing after clearing: %v", err)
	}
	if again := call("chattr.remove", value.MapOf("paths", []any{file}, "attributes", "ia")); again.Len() != 0 {
		t.Errorf("a second chattr.remove reported %v", again)
	}

	// `e` is refused before anything runs.
	before := lsattrTool(t, file)
	if _, err := r.Exec.Call(c, "chattr.remove", value.MapOf("paths", []any{file}, "attributes", "e")); err == nil {
		t.Error("chattr.remove e was accepted")
	}
	if after := lsattrTool(t, file); after != before {
		t.Errorf("a refused call changed %s from %s to %s", file, before, after)
	}
}
