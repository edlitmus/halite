package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The modules read /etc/sysctl.conf and friends through a link and used
// to write them by renaming over it, so the real file stayed as it was
// and the link was replaced by a copy.
func TestAConfigFileWrittenThroughALinkEditsTheTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privilege on Windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real.conf")
	link := filepath.Join(dir, "sysctl.conf")
	if err := os.WriteFile(real, []byte("a=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := writeSysctlConf(link, "a=1\n", "a", "2"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(real); string(b) != "a=2\n" {
		t.Errorf("the file the link points to holds %q", b)
	}
}

// A configuration file the modules rewrite keeps the mode it had. Each
// caller passed a constant, 0644 for most, so a sysctl.conf an estate
// had made 0600 was made world-readable by the first sysctl.present.
// Salt opens these files with "w" and truncates them in place, which
// keeps the mode; this keeps it across the rename. A file that does not
// exist yet gets the caller's mode. Issue 321, Low, `atomicfile.Write`;
// DIVERGENCE 5.471.
func TestAConfigFileKeepsItsModeWhenRewritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "sysctl.conf")
	if err := os.WriteFile(conf, []byte("a=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(conf, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeSysctlConf(conf, "a=1\n", "a", "2"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(conf); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("a 0600 sysctl.conf was rewritten as %v (%v)", fi.Mode(), err)
	}

	// Through a link, the target's mode is the one kept.
	real := filepath.Join(dir, "hosts.real")
	link := filepath.Join(dir, "hosts")
	if err := os.WriteFile(real, []byte("127.0.0.1 localhost\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(link, []byte("127.0.0.1 localhost web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(real); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("a 0640 file behind a link was rewritten as %v (%v)", fi.Mode(), err)
	}

	fresh := filepath.Join(dir, "new.conf")
	if err := writeConfigFile(fresh, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("a new file was made %v (%v)", fi.Mode(), err)
	}
}
