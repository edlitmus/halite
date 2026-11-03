//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A hard link is not a symbolic link, and every protection against a
// planted entry in a directory another account can write was a protection
// against the second. os.Root refuses to leave its directory and Lchown
// does not follow a link, but a hard link is not followed: it *is* the
// other file. An account that owns a directory a state manages could put a
// hard link to /etc/spwd.db under a name the state touches, and the
// next run changed the mode or the owner of spwd.db itself.
//
// These tests stand in for "another account can write this directory" with
// a directory mode of 0777: the run is not root, so there is no second
// account to make, and a world-writable directory is the same fact as far
// as the check is concerned. The outside file is a stand-in for a
// root-owned one; what is checked is that its mode and group do not move.
// The tests began as the reviewer's probes of 2026-11-03 and failed on
// de26c91b: the outside file went to 0644, and to the other group.

// groupOtherThan is a group this account may chgrp path to and path does
// not already have. otherGroup alone is not enough on the BSDs, where a
// new file takes its directory's group rather than its creator's: under
// /tmp that is wheel, which may be the very group otherGroup returns, and
// then there is no change to refuse and the test proves nothing.
func groupOtherThan(t *testing.T, path string) (int, string) {
	t.Helper()
	_, have := statOf(t, path)
	gids, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range append([]int{os.Getgid()}, gids...) {
		if uint32(g) != have {
			return g, strconv.Itoa(g)
		}
	}
	t.Skip("this account has no group the file is not already in, so no ownership change can be observed without root")
	return 0, ""
}

func statOf(t *testing.T, path string) (os.FileMode, uint32) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm(), fi.Sys().(*syscall.Stat_t).Gid
}

// outsideFile is the file the hard link points at: private, and not in
// the tree the state manages.
func outsideFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "spwd.db")
	if err := os.WriteFile(p, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func plantHardLink(t *testing.T, victim, at string) {
	t.Helper()
	_ = os.Remove(at)
	if err := os.Link(victim, at); err != nil {
		t.Skipf("a hard link could not be made here: %v", err)
	}
}

func openDirToOthers(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveOwnershipRefusesAPlantedHardLink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	dest := filepath.Join(dir, "out")
	makeTarGz(t, archive, map[string]string{"data/conf.txt": "x"})
	victim := outsideFile(t, dir)
	gid, group := groupOtherThan(t, victim)
	r := New()
	if res := run(t, r, "archive.extracted", value.MapOf("name", dest, "source", archive), false); !res.Succeeded() {
		t.Fatal(res.Comment)
	}
	openDirToOthers(t, filepath.Join(dest, "data"))
	plantHardLink(t, victim, filepath.Join(dest, "data", "conf.txt"))

	res := run(t, r, "archive.extracted", value.MapOf("name", dest, "source", archive, "group", group), false)
	if _, got := statOf(t, victim); int(got) == gid {
		t.Fatalf("the file outside the destination was given group %d through a hard link (comment %q)", gid, res.Comment)
	}
	if res.Succeeded() || !strings.Contains(res.Comment, "hard link") {
		t.Errorf("the run should fail and name the hard link; ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
}

func TestFileDirectoryRecurseRefusesAPlantedHardLink(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	victim := outsideFile(t, dir)
	plantHardLink(t, victim, filepath.Join(home, "f"))

	res := run(t, New(), "file.directory", value.MapOf(
		"name", home, "mode", "0777", "file_mode", "0644", "recurse", []any{"mode"}), false)
	if mode, _ := statOf(t, victim); mode != 0o600 {
		t.Fatalf("the file outside the tree went to %o through a hard link (comment %q)", mode, res.Comment)
	}
	if res.Succeeded() || !strings.Contains(res.Comment, "hard link") {
		t.Errorf("the run should fail and name the hard link; ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
}

func TestFileManagedReplaceFalseRefusesAPlantedHardLink(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	victim := outsideFile(t, dir)
	profile := filepath.Join(home, ".profile")
	plantHardLink(t, victim, profile)

	res := run(t, New(), "file.managed", value.MapOf(
		"name", profile, "contents", "x\n", "replace", false, "mode", "0644"), false)
	if mode, _ := statOf(t, victim); mode != 0o600 {
		t.Fatalf("the file outside the directory went to %o through a hard link (comment %q)", mode, res.Comment)
	}
	if b, _ := os.ReadFile(victim); string(b) != "root:secret\n" {
		t.Fatalf("the file outside the directory was rewritten: %q", b)
	}
	if res.Succeeded() || !strings.Contains(res.Comment, "hard link") {
		t.Errorf("the run should fail and name the hard link; ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
}

func TestFileManagedOwnershipRefusesAPlantedHardLink(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	victim := outsideFile(t, dir)
	gid, group := groupOtherThan(t, victim)
	profile := filepath.Join(home, ".profile")
	plantHardLink(t, victim, profile)

	res := run(t, New(), "file.managed", value.MapOf(
		"name", profile, "contents", "x\n", "replace", false, "group", group), false)
	if _, got := statOf(t, victim); int(got) == gid {
		t.Fatalf("the file outside the directory was given group %d through a hard link (comment %q)", gid, res.Comment)
	}
	if res.Succeeded() || !strings.Contains(res.Comment, "hard link") {
		t.Errorf("the run should fail and name the hard link; ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
}

// The other half: a hard link is refused only where another account could
// have made it. A file with two names in a directory nobody else can write
// is the file the state names, and a state that manages /usr/local/bin/perl
// (a hard link to perl5.x on FreeBSD) has to keep working.
func TestFileManagedChangesAHardLinkInAPrivateDirectory(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(conf, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(conf, filepath.Join(dir, "app.conf.other-name")); err != nil {
		t.Skipf("a hard link could not be made here: %v", err)
	}
	res := run(t, New(), "file.managed", value.MapOf(
		"name", conf, "contents", "x\n", "replace", false, "mode", "0644"), false)
	if !res.Succeeded() {
		t.Fatalf("a hard link in a private directory was refused: %q", res.Comment)
	}
	if mode, _ := statOf(t, conf); mode != 0o644 {
		t.Fatalf("mode is %o, want 0644", mode)
	}
}

// The same rule holds for a file with one name: in a directory another
// account can write, the change is made on the file that was opened and
// checked, not on whatever the name means a moment later.
func TestFileManagedChangesASingleLinkFileInAnOpenDirectory(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	profile := filepath.Join(home, ".profile")
	if err := os.WriteFile(profile, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, New(), "file.managed", value.MapOf(
		"name", profile, "contents", "x\n", "replace", false, "mode", "0644"), false)
	if !res.Succeeded() {
		t.Fatalf("an ordinary file in an open directory was refused: %q", res.Comment)
	}
	if mode, _ := statOf(t, profile); mode != 0o644 {
		t.Fatalf("mode is %o, want 0644", mode)
	}
}

// Test mode is asked the same question by the same code, so a dry run
// refuses what the run would refuse instead of promising a change.
func TestHardLinkRefusalIsTheSameInTestMode(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	victim := outsideFile(t, dir)
	plantHardLink(t, victim, filepath.Join(home, "f"))

	res := run(t, New(), "file.directory", value.MapOf(
		"name", home, "mode", "0777", "file_mode", "0644", "recurse", []any{"mode"}), true)
	if res.Succeeded() || !strings.Contains(res.Comment, "hard link") {
		t.Errorf("test mode should refuse as the run does; ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
}

// A FIFO in a directory another account can write cannot be opened and
// changed through a descriptor without risk, nor changed by name without
// the swap, so a recursive state leaves it alone and says so, and does not
// report it as a change -- in test mode or out of it.
func TestFileDirectoryRecurseSkipsAFIFOInAnOpenDirectory(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	fifo := filepath.Join(home, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("a FIFO could not be made here: %v", err)
	}
	if err := os.Chmod(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	args := value.MapOf("name", home, "mode", "0777", "file_mode", "0644", "recurse", []any{"mode"})
	for _, test := range []bool{true, false} {
		res := run(t, New(), "file.directory", args, test)
		if !res.Succeeded() {
			t.Fatalf("test=%v: the state failed: %q", test, res.Comment)
		}
		if res.HasChanges() {
			t.Errorf("test=%v: the FIFO was reported as a change: %q", test, res.Comment)
		}
		if !strings.Contains(strings.Join(res.Warnings, "\n"), "a FIFO") {
			t.Errorf("test=%v: no warning names the FIFO: %q", test, res.Warnings)
		}
	}
	if mode, _ := statOf(t, fifo); mode != 0o600 {
		t.Fatalf("the FIFO went to %o", mode)
	}
}

// A symbolic link an archive ships, in a destination another account can
// write, keeps its owner with a warning rather than failing the state: a
// link's owner grants nothing, and archives ship links.
func TestArchiveOwnershipSkipsALinkInAnOpenDirectory(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	gid, group := groupOtherThan(t, dest)
	openDirToOthers(t, dest)
	archive := filepath.Join(dir, "a.tar")
	makeOrderedTar(t, archive, fileEntry("lib.so.1", "x"), linkEntry("lib.so", "lib.so.1"))

	res := run(t, New(), "archive.extracted", value.MapOf("name", dest, "source", archive, "group", group), false)
	if !res.Succeeded() {
		t.Fatalf("the state failed: %q", res.Comment)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "a symbolic link") {
		t.Errorf("no warning names the link: %q", res.Warnings)
	}
	fi, err := os.Stat(filepath.Join(dest, "lib.so.1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := int(fi.Sys().(*syscall.Stat_t).Gid); got != gid {
		t.Errorf("the regular file has group %d, want %d", got, gid)
	}
	// The second run has nothing to change and says so, with the warning
	// again rather than a change it will never make.
	res = run(t, New(), "archive.extracted", value.MapOf("name", dest, "source", archive, "group", group), false)
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("the second run did not converge: ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
}

// The plan asks the change code whether it would act, in test mode too;
// asking must not act.
func TestRecursePlanDoesNotChangeAnything(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	openDirToOthers(t, home)
	f := filepath.Join(home, "f")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, New(), "file.directory", value.MapOf(
		"name", home, "mode", "0777", "file_mode", "0644", "recurse", []any{"mode"}), true)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("test mode should promise the change: ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
	if mode, _ := statOf(t, f); mode != 0o600 {
		t.Fatalf("a test run changed the file to %o", mode)
	}
}
