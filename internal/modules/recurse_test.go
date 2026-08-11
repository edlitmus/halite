package modules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// recurseFixture writes a source tree and returns its directory plus an
// empty destination.
func recurseFixture(t *testing.T, files map[string]string) (src, dest string) {
	t.Helper()
	root := t.TempDir()
	src = filepath.Join(root, "source")
	dest = filepath.Join(root, "dest")
	for name, body := range files {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src, dest
}

func TestTreeIsCopiedThenLeftAlone(t *testing.T) {
	src, dest := recurseFixture(t, map[string]string{
		"nginx.conf":       "worker_processes 4;\n",
		"conf.d/site.conf": "server {}\n",
	})
	args := map[string]any{"name": dest, "source": src}

	r := fileRecurse(&Ctx{}, "tree", args)
	if !r.Ok || !r.Changed {
		t.Fatalf("first run should copy the tree: %+v", r)
	}
	body, err := os.ReadFile(filepath.Join(dest, "conf.d", "site.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "server {}\n" {
		t.Fatalf("want the nested file copied, got %q", string(body))
	}
	if r := fileRecurse(&Ctx{}, "tree", args); !r.Ok || r.Changed {
		t.Fatalf("second run should be a no-op: %+v", r)
	}
}

func TestChangedFileIsRewritten(t *testing.T) {
	src, dest := recurseFixture(t, map[string]string{"app.conf": "one\n"})
	args := map[string]any{"name": dest, "source": src}
	fileRecurse(&Ctx{}, "tree", args)

	if err := os.WriteFile(filepath.Join(dest, "app.conf"), []byte("drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := fileRecurse(&Ctx{}, "tree", args)
	if !r.Changed || !strings.Contains(r.Changes["written"], "app.conf") {
		t.Fatalf("drift should be repaired: %+v", r)
	}
}

func TestUnmanagedFilesSurviveWithoutClean(t *testing.T) {
	src, dest := recurseFixture(t, map[string]string{"app.conf": "one\n"})
	args := map[string]any{"name": dest, "source": src}
	fileRecurse(&Ctx{}, "tree", args)

	stray := filepath.Join(dest, "stray.conf")
	if err := os.WriteFile(stray, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := fileRecurse(&Ctx{}, "tree", args); r.Changed {
		t.Fatalf("an unmanaged file is not drift: %+v", r)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("the file should still be there")
	}

	args["clean"] = "true"
	r := fileRecurse(&Ctx{}, "tree", args)
	if !r.Changed || !strings.Contains(r.Changes["removed"], "stray.conf") {
		t.Fatalf("clean should remove it: %+v", r)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("clean should have removed the file")
	}
}

func TestModesAreApplied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	src, dest := recurseFixture(t, map[string]string{"conf.d/site.conf": "x\n"})
	args := map[string]any{"name": dest, "source": src, "file_mode": "0640", "dir_mode": "0750"}

	if r := fileRecurse(&Ctx{}, "tree", args); !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	file, err := os.Stat(filepath.Join(dest, "conf.d", "site.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := file.Mode().Perm(); perm != 0o640 {
		t.Fatalf("want file_mode 0640, got %04o", perm)
	}
	dir, err := os.Stat(filepath.Join(dest, "conf.d"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o750 {
		t.Fatalf("want dir_mode 0750, got %04o", perm)
	}
	if r := fileRecurse(&Ctx{}, "tree", args); r.Changed {
		t.Fatalf("modes already applied should not report drift: %+v", r)
	}
}

func TestModeDriftIsRepaired(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	src, dest := recurseFixture(t, map[string]string{"app.conf": "x\n"})
	args := map[string]any{"name": dest, "source": src, "file_mode": "0600"}
	fileRecurse(&Ctx{}, "tree", args)

	target := filepath.Join(dest, "app.conf")
	if err := os.Chmod(target, 0o666); err != nil {
		t.Fatal(err)
	}
	if r := fileRecurse(&Ctx{}, "tree", args); !r.Changed {
		t.Fatalf("a loosened mode is drift: %+v", r)
	}
	st, _ := os.Stat(target)
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want 0600 restored, got %04o", perm)
	}
}

func TestTemplatedTreeIsRendered(t *testing.T) {
	src, dest := recurseFixture(t, map[string]string{"motd": "welcome to {{ .Grains.host }}\n"})
	args := map[string]any{"name": dest, "source": src, "template": "true"}
	c := &Ctx{Grains: map[string]any{"host": "web1"}}

	if r := fileRecurse(c, "tree", args); !r.Changed {
		t.Fatalf("want a change: %+v", r)
	}
	body, _ := os.ReadFile(filepath.Join(dest, "motd"))
	if string(body) != "welcome to web1\n" {
		t.Fatalf("want the template rendered, got %q", string(body))
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	src, dest := recurseFixture(t, map[string]string{"app.conf": "x\n"})
	r := fileRecurse(&Ctx{Test: true}, "tree", map[string]any{"name": dest, "source": src})
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending copy: %+v", r)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a dry run must not create the destination")
	}
}

func TestMissingSourceFails(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest")
	r := fileRecurse(&Ctx{}, "tree", map[string]any{"name": dest, "source": "/nonexistent/tree"})
	if r.Ok {
		t.Fatal("a missing source directory should fail the state")
	}
}
