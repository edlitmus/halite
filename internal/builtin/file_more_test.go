package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm/permtest"
	"github.com/edlitmus/halite/internal/value"
)

func fileCall(t *testing.T, r *Registries, fn string, args *value.Map) any {
	t.Helper()
	out, err := r.Exec.Call(newCtx(false), fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	return out
}

func TestFileModeAndOwnership(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	fileCall(t, r, "file.chmod", value.MapOf("path", path, "mode", "0600"))
	assertMode(t, path, 0o600)

	// A mode given as an integer is the YAML parser having read an
	// unquoted 0644 as octal, and accepting it silently is how a file
	// ends up 0644 in the tree and 420 on disk.
	if _, err := r.Exec.Call(newCtx(false), "file.chmod",
		value.MapOf("path", path, "mode", int64(644))); err == nil {
		t.Error("an unquoted mode should be refused")
	}
}

func TestFileDirectories(t *testing.T) {
	r := New()
	dir := t.TempDir()

	deep := filepath.Join(dir, "a", "b", "c")
	fileCall(t, r, "file.mkdir", value.MapOf("path", deep, "mode", "0750"))
	info, err := os.Stat(deep)
	if err != nil || !info.IsDir() {
		t.Fatalf("mkdir did not create the directory: %v", err)
	}
	// The umask must not decide the leaf's mode.
	assertMode(t, deep, 0o750)

	// makedirs creates a path's *parents*, not the path. Creating the
	// path itself as a directory is how a later write fails with "is a
	// directory".
	target := filepath.Join(dir, "x", "y", "config.conf")
	out := fileCall(t, r, "file.makedirs", value.MapOf("path", target))
	if out != filepath.Dir(target) {
		t.Errorf("makedirs returned %v, want the parent", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("makedirs created the path itself; it should create only its parents")
	}
	if info, err := os.Stat(filepath.Dir(target)); err != nil || !info.IsDir() {
		t.Errorf("the parent was not created: %v", err)
	}

	// rmdir removes an empty directory and refuses anything else.
	empty := filepath.Join(dir, "empty")
	os.Mkdir(empty, 0o755)
	if got := fileCall(t, r, "file.rmdir", value.MapOf("path", empty)); got != true {
		t.Errorf("rmdir = %v", got)
	}
	if _, err := r.Exec.Call(newCtx(false), "file.rmdir", value.MapOf("path", deep)); err == nil {
		// deep's parent is not empty
		t.Log("rmdir on a leaf directory succeeds, which is correct")
	}
	f := filepath.Join(dir, "notadir")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, err := r.Exec.Call(newCtx(false), "file.rmdir", value.MapOf("path", f)); err == nil {
		t.Error("rmdir on a file should be refused")
	}
}

func TestFileLinksAndMove(t *testing.T) {
	r := New()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("content"), 0o644)

	link := filepath.Join(dir, "link")
	os.Symlink(src, link)

	if got := fileCall(t, r, "file.is_link", value.MapOf("path", link)); got != true {
		t.Error("the symlink should be reported as one")
	}
	if got := fileCall(t, r, "file.is_link", value.MapOf("path", src)); got != false {
		t.Error("a regular file is not a symlink")
	}
	if got := fileCall(t, r, "file.readlink", value.MapOf("path", link)); got != src {
		t.Errorf("readlink = %v, want %q", got, src)
	}
	if _, err := r.Exec.Call(newCtx(false), "file.readlink", value.MapOf("path", src)); err == nil {
		t.Error("readlink on a regular file should say it is not a symlink")
	}

	hard := filepath.Join(dir, "hard")
	fileCall(t, r, "file.hardlink", value.MapOf("src", src, "path", hard))
	if data, _ := os.ReadFile(hard); string(data) != "content" {
		t.Errorf("the hard link does not carry the content: %q", data)
	}

	moved := filepath.Join(dir, "moved")
	fileCall(t, r, "file.move", value.MapOf("src", src, "dst", moved))
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the source should be gone after a move")
	}
	if data, _ := os.ReadFile(moved); string(data) != "content" {
		t.Errorf("the moved file = %q", data)
	}
}

func TestFileAccess(t *testing.T) {
	r := New()
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("x"), 0o644)
	link := filepath.Join(dir, "l")
	os.Symlink(f, link)

	cases := map[[2]string]bool{
		{f, "e"}:                          true,
		{f, "f"}:                          true,
		{f, "d"}:                          false,
		{f, "r"}:                          true,
		{f, "x"}:                          false,
		{dir, "d"}:                        true,
		{link, "l"}:                       true,
		{f, "l"}:                          false,
		{filepath.Join(dir, "nope"), "e"}: false,
	}
	for in, want := range cases {
		got := fileCall(t, r, "file.access", value.MapOf("path", in[0], "mode", in[1]))
		if got != want {
			t.Errorf("access(%s, %s) = %v, want %v", filepath.Base(in[0]), in[1], got, want)
		}
	}

	// The `l` test does not follow the link, and every other test does,
	// which is what test(1) means.
	if got := fileCall(t, r, "file.access", value.MapOf("path", link, "mode", "f")); got != true {
		t.Error("a symlink to a regular file answers the f test")
	}
}

func TestFileCheckHash(t *testing.T) {
	r := New()
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("abc"), 0o644)

	const sha256abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	cases := map[string]bool{
		sha256abc:                  true,
		"sha256=" + sha256abc:      true,
		strings.ToUpper(sha256abc): true, // a digest copied from a vendor page
		sha256abc[:63] + "0":       false,
	}
	for digest, want := range cases {
		got := fileCall(t, r, "file.check_hash", value.MapOf("path", f, "file_hash", digest))
		if got != want {
			t.Errorf("check_hash(%s...) = %v, want %v", digest[:12], got, want)
		}
	}

	// A digest of an unrecognisable length is an error rather than a
	// silent false, which would look like a corrupt file.
	if _, err := r.Exec.Call(newCtx(false), "file.check_hash",
		value.MapOf("path", f, "file_hash", "abcd")); err == nil {
		t.Error("an unidentifiable digest should be an error")
	}

	// An MD5 or SHA-1 digest is refused by name. A tree carrying one
	// should hear that it verifies nothing, rather than get a comparison
	// that passes and means nothing.
	for digest, want := range map[string]string{
		"900150983cd24fb0d6963f7d28e17f72":         "MD5",
		"a9993e364706816aba3e25717850c26c9cd0d89d": "SHA-1",
	} {
		_, err := r.Exec.Call(newCtx(false), "file.check_hash",
			value.MapOf("path", f, "file_hash", digest))
		if err == nil {
			t.Errorf("a %s digest should be refused", want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %s: %v", want, err)
		}
	}
}

func TestFileGrepAndFind(t *testing.T) {
	r := New()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.conf"), []byte("listen 80\nserver_name x\nLISTEN 443\n"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.conf"), []byte("y\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("z\n"), 0o644)

	lines := fileCall(t, r, "file.grep",
		value.MapOf("path", filepath.Join(dir, "a.conf"), "pattern", "^listen")).([]any)
	if len(lines) != 1 || lines[0] != "listen 80" {
		t.Errorf("grep = %v", lines)
	}
	lines = fileCall(t, r, "file.grep",
		value.MapOf("path", filepath.Join(dir, "a.conf"), "pattern", "^listen", "ignore_case", true)).([]any)
	if len(lines) != 2 {
		t.Errorf("case-insensitive grep = %v", lines)
	}

	// A pattern RE2 cannot express is refused by name, not by matching
	// nothing. SPEC 10.4.
	if _, err := r.Exec.Call(newCtx(false), "file.grep",
		value.MapOf("path", filepath.Join(dir, "a.conf"), "pattern", "(?=listen)")); err == nil {
		t.Error("a lookahead should be refused by name")
	}

	found := fileCall(t, r, "file.find", value.MapOf("path", dir, "name", "*.conf")).([]any)
	if len(found) != 2 {
		t.Errorf("find *.conf = %v", found)
	}
	found = fileCall(t, r, "file.find", value.MapOf("path", dir, "type", "d")).([]any)
	if len(found) != 1 {
		t.Errorf("find -type d = %v", found)
	}
	// maxdepth counts as find(1) counts it: 1 is the root's immediate
	// children, so the nested b.conf is left out.
	found = fileCall(t, r, "file.find", value.MapOf("path", dir, "name", "*.conf", "maxdepth", int64(1))).([]any)
	if len(found) != 1 {
		t.Errorf("maxdepth 1 should stay in the top directory: %v", found)
	}

	// A malformed glob is an error, not a pattern matching nothing.
	if _, err := r.Exec.Call(newCtx(false), "file.find",
		value.MapOf("path", dir, "name", "[")); err == nil {
		t.Error("a malformed name pattern should be refused")
	}
}

// These build a path in the node's own convention, as Salt's do: the
// function runs on the node, and a Windows node wants a Windows path.
// The expectations are therefore written with the host's separator
// rather than a slash, which is what made this read `join =
// \etc\nginx\nginx.conf` there and call it a failure.
func TestFilePathArithmetic(t *testing.T) {
	r := New()
	sep := string(os.PathSeparator)
	if got := fileCall(t, r, "file.join", value.MapOf("parts", []any{"/etc", "nginx", "nginx.conf"})); got != sep+"etc"+sep+"nginx"+sep+"nginx.conf" {
		t.Errorf("join = %v", got)
	}
	// Joining cleans, which is the point of having it rather than a
	// template concatenating strings.
	if got := fileCall(t, r, "file.join", value.MapOf("parts", []any{"/etc/", "/nginx", "../nginx.conf"})); got != sep+"etc"+sep+"nginx.conf" {
		t.Errorf("join = %v", got)
	}
	if got := fileCall(t, r, "file.basename", value.MapOf("path", "/etc/nginx/nginx.conf")); got != "nginx.conf" {
		t.Errorf("basename = %v", got)
	}
	if got := fileCall(t, r, "file.dirname", value.MapOf("path", "/etc/nginx/nginx.conf")); got != sep+"etc"+sep+"nginx" {
		t.Errorf("dirname = %v", got)
	}
}

func TestFileTruncate(t *testing.T) {
	r := New()
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("0123456789"), 0o644)

	fileCall(t, r, "file.truncate", value.MapOf("path", f, "length", int64(4)))
	if data, _ := os.ReadFile(f); string(data) != "0123" {
		t.Errorf("after truncate = %q", data)
	}
	fileCall(t, r, "file.truncate", value.MapOf("path", f))
	if data, _ := os.ReadFile(f); len(data) != 0 {
		t.Errorf("truncate with no length should empty the file: %q", data)
	}
}

func TestFileManagedRendersItsSource(t *testing.T) {
	r := New()
	dir := t.TempDir()
	source := filepath.Join(dir, "conf.jinja")
	os.WriteFile(source, []byte(
		"name = {{ pillar['app']['name'] }}\n"+
			"kernel = {{ grains['kernel'] }}\n"+
			"extra = {{ extra_name }}\n"+
			"fallback = {{ from_defaults }}\n"), 0o644)
	target := filepath.Join(dir, "conf")

	ctx := newCtx(false)
	ctx.Grains = value.MapOf("kernel", "FreeBSD")
	ctx.Pillar = value.MapOf("app", value.MapOf("name", "from-pillar"))

	args := value.MapOf(
		"name", target,
		"source", source,
		"template", "jinja",
		"context", value.MapOf("extra_name", "from-context"),
		"defaults", value.MapOf("from_defaults", "default-value", "extra_name", "overridden"),
	)
	if _, err := r.States.Call(ctx, "file.managed", args); err != nil {
		t.Fatalf("file.managed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "name = from-pillar\nkernel = FreeBSD\nextra = from-context\nfallback = default-value\n"
	if string(got) != want {
		t.Errorf("wrote:\n%s\nwant:\n%s", got, want)
	}

	// Without the argument the source is written as it is, so a file
	// that happens to contain braces is not mangled.
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("literal {{ not_a_name }}\n"), 0o644)
	out := filepath.Join(dir, "out")
	if _, err := r.States.Call(ctx, "file.managed",
		value.MapOf("name", out, "source", plain)); err != nil {
		t.Fatalf("file.managed: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "literal {{ not_a_name }}\n" {
		t.Errorf("an unrendered source was changed: %q", got)
	}

	// An engine this build does not have is named, rather than ignored.
	res, err := r.States.Call(ctx, "file.managed",
		value.MapOf("name", out, "source", source, "template", "mako"))
	if err == nil && res.Succeeded() {
		t.Error("template: mako should fail")
	}
	if err == nil && !strings.Contains(res.Comment, "mako") {
		t.Errorf("the comment should name the engine: %q", res.Comment)
	}
}

func TestFileDirectoryRecurse(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Every mode this exercises — 0750, 0640, 0755 — is one
		// Windows cannot express, so there is nothing here to
		// propagate and no change to predict. What recurse does mean
		// on that platform is covered by
		// TestRecursingAPrivateModeReachesEveryPath.
		t.Skip("mode propagation needs a platform with modes")
	}
	r := New()
	root := t.TempDir()
	deep := filepath.Join(root, "sub", "deeper")
	if err := os.MkdirAll(deep, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		filepath.Join(root, "top.txt"),
		filepath.Join(root, "sub", "mid.txt"),
		filepath.Join(deep, "leaf.txt"),
	} {
		if err := os.WriteFile(f, []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
		// WriteFile's mode goes through the umask, and this test is
		// about modes.
		if err := os.Chmod(f, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	args := func(extra ...any) *value.Map {
		base := []any{
			"name", root,
			"mode", "0755",
			"dir_mode", "0750",
			"file_mode", "0640",
			"recurse", []any{"mode"},
		}
		return value.MapOf(append(base, extra...)...)
	}

	// Test mode reports the whole plan and changes nothing.
	res, err := r.States.Call(newCtx(true), "file.directory", args())
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode should report a pending change, got %v", res.Result)
	}
	if info, _ := os.Stat(filepath.Join(root, "top.txt")); info.Mode().Perm() != 0o666 {
		t.Errorf("test mode changed a file's mode to %04o", info.Mode().Perm())
	}

	// The real run gives directories dir_mode and files file_mode. A
	// directory mode on a file is the mistake the two options exist to
	// prevent.
	if _, err := r.States.Call(newCtx(false), "file.directory", args()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(root, "sub"):      0o750,
		deep:                            0o750,
		filepath.Join(root, "top.txt"):  0o640,
		filepath.Join(deep, "leaf.txt"): 0o640,
	} {
		assertMode(t, path, want)
	}

	// A second run converges.
	res, err = r.States.Call(newCtx(false), "file.directory", args())
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a second run reported changes: %v", res.Changes)
	}

	// max_depth bounds the walk.
	os.Chmod(filepath.Join(deep, "leaf.txt"), 0o666)
	os.Chmod(filepath.Join(root, "top.txt"), 0o666)
	if _, err := r.States.Call(newCtx(false), "file.directory", args("max_depth", int64(1))); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(root, "top.txt")); info.Mode().Perm() != 0o640 {
		t.Errorf("depth 1 should have been reached: %04o", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Join(deep, "leaf.txt")); info.Mode().Perm() != 0o666 {
		t.Errorf("depth 3 should not have been reached: %04o", info.Mode().Perm())
	}

	// Naming mode without saying what mode a file gets is refused, since
	// the alternative is silently giving a file a directory's mode.
	res, _ = r.States.Call(newCtx(false), "file.directory",
		value.MapOf("name", root, "recurse", []any{"mode"}))
	if res.Succeeded() {
		t.Error("recurse: mode with no mode of any kind should fail")
	}
	// So is a word recurse does not take.
	res, _ = r.States.Call(newCtx(false), "file.directory",
		value.MapOf("name", root, "recurse", []any{"modes"}))
	if res.Succeeded() || !strings.Contains(res.Comment, "modes") {
		t.Errorf("an unknown recurse word should be named: %q", res.Comment)
	}
}

func TestFileManagedRefusesAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything, which is the case this is not about")
	}
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.conf")
	if err := os.WriteFile(path, []byte("real contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Made unreadable the way the platform does it: a chmod to 0000
	// returns nil on Windows and changes nothing.
	permtest.DenyRead(t, path)

	// The error from the read was discarded, so an unreadable file
	// compared as empty: the state said the contents differed, showed a
	// diff adding the whole file, and rewrote it — every run, for ever,
	// because it still could not read what it had written.
	res, err := r.States.Call(newCtx(true), "file.managed",
		value.MapOf("name", path, "contents", "real contents"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Errorf("an unreadable file should not be reported as converged or as differing: %q", res.Comment)
	}
	if !strings.Contains(res.Comment, "could not be read") {
		t.Errorf("the comment should say what happened: %q", res.Comment)
	}

	// The file is untouched, and readable again for the cleanup.
	permtest.AllowRead(t, path)
	if data, _ := os.ReadFile(path); string(data) != "real contents\n" {
		t.Errorf("the file was written: %q", data)
	}
}
