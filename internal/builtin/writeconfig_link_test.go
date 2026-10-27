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
