package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/value"
)

// A mode this platform cannot carry out is said so once and then leaves
// the state converged.
//
// Before this, `mode: '0640'` compared the 0666 that os.Stat synthesises
// against 0640, found them different, ran a chmod that changed nothing,
// and found them different again on the next run. No file state on
// Windows ever converged: every run reported a change, so `state.apply`
// never returned the exit code for a converged run and a highstate could
// not tell drift from noise.
func TestAModeThatCannotBeCarriedOutIsSaidSoOnceAndConverges(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "app.conf")
	args := value.MapOf("name", path, "contents", "x\n", "mode", "0640")

	first := run(t, r, "file.managed", args, false)
	if !first.Succeeded() {
		t.Fatalf("the first run failed: %+v", first)
	}
	second := run(t, r, "file.managed", args, false)
	if second.HasChanges() {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
	// And it says why, rather than applying a mode that does nothing.
	if len(second.Warnings) == 0 {
		t.Fatal("nothing was said about a mode that was not applied")
	}
	joined := strings.Join(second.Warnings, "\n")
	for _, want := range []string{"0640", "win_dacl", path} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning does not mention %q: %s", want, joined)
		}
	}

	// Test mode agrees with what an apply would do.
	predicted := run(t, r, "file.managed", args, true)
	if predicted.HasChanges() {
		t.Errorf("test mode predicted a change against a converged system: %+v", predicted.Changes)
	}
}

// The part of a mode that does mean something here is carried out: a
// mode denying group and other says no other account may read the file,
// and that is an access control list.
func TestAPrivateModeIsCarriedOutAsAnAccessControlList(t *testing.T) {
	r := New()
	path := filepath.Join(t.TempDir(), "key.pem")
	args := value.MapOf("name", path, "contents", "secret\n", "mode", "0600")

	if res := run(t, r, "file.managed", args, false); !res.Succeeded() {
		t.Fatalf("the state failed: %+v", res)
	}
	others, err := fileperm.Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("a file written with mode 0600 can be read by %v", others)
	}
	// A private mode is a request this platform can meet, so nothing is
	// warned about and the state converges.
	second := run(t, r, "file.managed", args, false)
	if second.HasChanges() {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
	if len(second.Warnings) != 0 {
		t.Errorf("a mode that was carried out should not warn: %v", second.Warnings)
	}
}

// What `recurse: [mode]` means on a platform without modes.
//
// A mode denying group and other is expressible here, so it propagates
// the way it does anywhere: every path under the directory ends up
// reachable by its owner, SYSTEM and Administrators and nobody else. A
// mode that is not expressible propagates nothing and, crucially,
// reports nothing — before this, every path under the directory went
// into the plan on every run, for ever.
func TestRecursingAPrivateModeReachesEveryPath(t *testing.T) {
	r := New()
	root := t.TempDir()
	deep := filepath.Join(root, "sub", "deeper")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []string{filepath.Join(root, "top.txt"), filepath.Join(deep, "leaf.txt")}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
	}

	args := value.MapOf(
		"name", root,
		"mode", "0700",
		"dir_mode", "0700",
		"file_mode", "0600",
		"recurse", []any{"mode"},
	)
	if _, err := r.States.Call(newCtx(false), "file.directory", args); err != nil {
		t.Fatal(err)
	}
	for _, p := range append(files, deep, filepath.Join(root, "sub")) {
		others, err := fileperm.Others(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(others) != 0 {
			t.Errorf("%s can be read by %v after a private mode was recursed", p, others)
		}
	}

	// And it converges.
	res, err := r.States.Call(newCtx(false), "file.directory", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a second run reported changes: %v", res.Changes)
	}
}

// A mode this platform cannot express puts nothing in the plan, rather
// than every path under the directory on every run.
func TestRecursingAModeWithNoMeaningHerePlansNothing(t *testing.T) {
	r := New()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	args := value.MapOf(
		"name", root,
		"mode", "0755",
		"dir_mode", "0750",
		"file_mode", "0640",
		"recurse", []any{"mode"},
	)
	if _, err := r.States.Call(newCtx(false), "file.directory", args); err != nil {
		t.Fatal(err)
	}
	res, err := r.States.Call(newCtx(false), "file.directory", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a mode with no meaning here was planned anyway: %v", res.Changes)
	}
}
