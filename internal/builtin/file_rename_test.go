package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// file.rename moves something into place once, and every later run has to
// be a no-op. The estate's `shared/salt/pgpkeys.sls` unpacks an archive
// and renames the directory it produced, so a second highstate meets a
// source that is already gone.
//
// Two of Salt's outcomes are successes that change nothing, and both read
// like errors: a source that has already moved, and a destination that
// exists without `force`. They are asserted here as successes because
// getting either wrong makes a highstate fail forever.
func TestFileRenameFollowsSalt(t *testing.T) {
	r := New()

	write := func(t *testing.T, path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("it moves the source onto the destination", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "from"), filepath.Join(dir, "to")
		write(t, src, "contents")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src), false)
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("ok=%v changes=%v comment=%q", res.Succeeded(), res.HasChanges(), res.Comment)
		}
		if body, err := os.ReadFile(dst); err != nil || string(body) != "contents" {
			t.Errorf("destination = %q, %v", body, err)
		}
		if _, err := os.Lstat(src); !os.IsNotExist(err) {
			t.Error("the source is still there")
		}
	})

	t.Run("a source already gone is a success, not a failure", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "gone"), filepath.Join(dir, "to")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src), false)
		if !res.Succeeded() {
			t.Errorf("a second run failed: %q", res.Comment)
		}
		if res.HasChanges() {
			t.Errorf("a second run reported a change: %v", res.Changes.StringKeys())
		}
	})

	t.Run("an existing destination without force is a success that changes nothing", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "from"), filepath.Join(dir, "to")
		write(t, src, "new")
		write(t, dst, "old")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src), false)
		if !res.Succeeded() {
			t.Errorf("it failed rather than declining: %q", res.Comment)
		}
		if res.HasChanges() {
			t.Error("it reported a change")
		}
		if body, _ := os.ReadFile(dst); string(body) != "old" {
			t.Errorf("the destination was overwritten without force: %q", body)
		}
	})

	t.Run("force replaces the destination", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "from"), filepath.Join(dir, "to")
		write(t, src, "new")
		write(t, dst, "old")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src, "force", true), false)
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("ok=%v changes=%v comment=%q", res.Succeeded(), res.HasChanges(), res.Comment)
		}
		if body, _ := os.ReadFile(dst); string(body) != "new" {
			t.Errorf("destination = %q, want the source's contents", body)
		}
	})

	t.Run("a whole directory moves, which is what the estate's tree does", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "keys"), filepath.Join(dir, "gpgkeys")
		if err := os.MkdirAll(filepath.Join(src, "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(src, "inner", "secring.gpg"), "secret")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src), false)
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("ok=%v comment=%q", res.Succeeded(), res.Comment)
		}
		if body, err := os.ReadFile(filepath.Join(dst, "inner", "secring.gpg")); err != nil || string(body) != "secret" {
			t.Errorf("the directory did not arrive whole: %q %v", body, err)
		}
	})

	t.Run("a missing parent needs makedirs", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "from")
		dst := filepath.Join(dir, "a", "b", "to")
		write(t, src, "x")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src), false)
		if res.Succeeded() {
			t.Error("it moved into a directory that does not exist")
		}

		res = run(t, r, "file.rename", value.MapOf("name", dst, "source", src, "makedirs", true), false)
		if !res.Succeeded() {
			t.Fatalf("makedirs did not help: %q", res.Comment)
		}
		if _, err := os.Stat(dst); err != nil {
			t.Errorf("the destination is not there: %v", err)
		}
	})

	t.Run("test mode moves nothing", func(t *testing.T) {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "from"), filepath.Join(dir, "to")
		write(t, src, "x")

		res := run(t, r, "file.rename", value.MapOf("name", dst, "source", src), true)
		if !res.HasChanges() {
			t.Error("test mode predicted no change")
		}
		if _, err := os.Lstat(src); err != nil {
			t.Error("test mode moved the source")
		}
		if _, err := os.Lstat(dst); err == nil {
			t.Error("test mode created the destination")
		}
	})
}
