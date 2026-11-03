//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The planted-link cases need an account other than the one running the
// test, and a test that is not root cannot make a file another account
// owns. There are two ways round that, and the tests use both:
//
//   - a directory anyone can write (0777, no sticky bit), which is the
//     same hazard without a second account: whoever can write the
//     directory can put a link in it;
//   - linkTrusted told that what the test made under one path is another
//     account's, which is what it is on a node.
//
// The second is the shape of the finding (a home directory, an app
// directory handed over with `user:`); the first is the one that needs no
// substitution at all. Only the named path is distrusted, not the account
// running the test: that would fail every case at the first directory of
// the temporary directory the account owns, before the link is reached,
// and the tests would pass for a reason none of them names.

// plantedLink makes victim (0600, outside the account's directory) and
// home/.profile pointing at it.
func plantedLink(t *testing.T, homeMode os.FileMode) (link, victim string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, homeMode); err != nil {
		t.Fatal(err)
	}
	// os.Chmod takes Go's ModeSticky, not 01000; a test that wrote 01777
	// once set 0777 and passed for the wrong reason.
	if fi, err := os.Stat(home); err != nil || fi.Mode()&(os.ModePerm|os.ModeSticky) != homeMode {
		t.Fatalf("%s was made %v, not %v (%v)", home, fi.Mode(), homeMode, err)
	}
	victim = filepath.Join(dir, "etc-shadow")
	if err := os.WriteFile(victim, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(home, ".profile")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	return link, victim
}

// distrust makes path, and everything under it, look like another
// account's for the rest of t.
func distrust(t *testing.T, path string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, filepath.Base(path))
	was := linkTrusted
	linkTrusted = func(p string, uid uint32) bool {
		if p == path || strings.HasPrefix(p, path+"/") {
			return false
		}
		return was(p, uid)
	}
	t.Cleanup(func() { linkTrusted = was })
}

func assertVictimUntouched(t *testing.T, link, victim string) {
	t.Helper()
	b, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "root:secret\n" {
		t.Errorf("the file the planted link names now holds %q", b)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Errorf("the file the planted link names was made %o", fi.Mode().Perm())
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the planted link was replaced: %v, %v", fi, err)
	}
}

// plantedCases are every writer that comes through followLink, each in the
// shape the reviewer ran it.
var plantedCases = []struct {
	fn    string
	state bool
	args  func(string) *value.Map
}{
	{"file.managed", true, func(p string) *value.Map {
		return value.MapOf("name", p, "contents", "tree-chosen\n", "mode", "0644")
	}},
	{"file.append", true, func(p string) *value.Map { return value.MapOf("name", p, "text", "appended\n") }},
	{"file.line", true, func(p string) *value.Map {
		return value.MapOf("name", p, "content", "added", "mode", "ensure", "location", "end")
	}},
	{"file.replace", true, func(p string) *value.Map { return value.MapOf("name", p, "pattern", "secret", "repl", "x") }},
	{"file.serialize", true, func(p string) *value.Map {
		return value.MapOf("name", p, "dataset", value.MapOf("a", 1), "formatter", "json")
	}},
	{"file.sed", false, func(p string) *value.Map { return value.MapOf("path", p, "before", "secret", "after", "x") }},
}

// **A root file state does not follow a link an account planted** (the
// M-19 fix, DIVERGENCE 5.346, followed every link; 5.558). Before this,
// `file.managed` with `mode: 0644` wrote the tree's contents into a 0600
// file outside the account's directory and made it 0644, and every edit
// wrote through the link the same way.
func TestAPlantedLinkIsNotFollowed(t *testing.T) {
	for _, how := range []struct {
		name     string
		homeMode os.FileMode
		distrust bool
		why      string
	}{
		{"in a directory anyone can write", 0o777, false, "which accounts other than its owner can write"},
		{"in a directory its group can write", 0o775, false, "which accounts other than its owner can write"},
		{"in a directory the account owns", 0o755, true, "home-deploy, a directory on the way to it, belongs to uid"},
	} {
		for _, tc := range plantedCases {
			t.Run(how.name+"/"+tc.fn, func(t *testing.T) {
				link, victim := plantedLink(t, how.homeMode)
				// Group write is tolerated for group 0, and as root the
				// temporary directory's group is 0 (wheel, on FreeBSD).
				// Only the group case cares, and 0777 has the group bit
				// too: testing for the bit alone once skipped the
				// world-writable case as root, on beastie.
				if fi, _ := os.Stat(filepath.Dir(link)); how.homeMode&0o022 == 0o020 && fi.Sys().(*syscall.Stat_t).Gid == 0 {
					if err := os.Chown(filepath.Dir(link), -1, 65534); err != nil {
						t.Skipf("the temporary directory's group is group 0, which is trusted, and could not be changed: %v", err)
					}
				}
				if how.distrust {
					distrust(t, filepath.Dir(link))
				}
				r := New()
				if tc.state {
					res, err := r.States.Call(newCtx(false), tc.fn, tc.args(link))
					if err != nil {
						t.Fatal(err)
					}
					if res.Result == nil || *res.Result {
						t.Errorf("%s through a planted link: result %v, %q", tc.fn, res.Result, res.Comment)
					} else if !strings.Contains(res.Comment, link) || !strings.Contains(res.Comment, "not followed") ||
						!strings.Contains(res.Comment, how.why) {
						t.Errorf("the refusal does not name the link and why: %q", res.Comment)
					}
				} else if _, err := r.Exec.Call(newCtx(false), tc.fn, tc.args(link)); err == nil ||
					!strings.Contains(err.Error(), how.why) {
					t.Errorf("%s through a planted link: %v", tc.fn, err)
				}
				assertVictimUntouched(t, link, victim)
			})
		}
	}
}

// **A directory above the link counts, not only the one it is in.** The
// account that owns /home/deploy can rename a root-owned subdirectory
// away and put its own in its place, links and all.
func TestALinkBelowAnAccountsDirectoryIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-deploy")
	sub := filepath.Join(home, ".config")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "etc-shadow")
	if err := os.WriteFile(victim, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sub, "app.conf")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	// Only home-deploy itself: .config and the link stay the test's own.
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	was := linkTrusted
	linkTrusted = func(p string, uid uint32) bool { return p != resolved && was(p, uid) }
	t.Cleanup(func() { linkTrusted = was })

	res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "home-deploy, a directory on the way to it, belongs to uid") {
		t.Errorf("a link below an account's directory: %v, %q", res.Result, res.Comment)
	}
	assertVictimUntouched(t, link, victim)
}

// The modules' configuration writers come through followLink too.
func TestAConfigWriterDoesNotFollowAPlantedLink(t *testing.T) {
	link, victim := plantedLink(t, 0o777)
	err := writeConfigFile(link, []byte("x\n"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "not followed") {
		t.Errorf("writeConfigFile through a planted link: %v", err)
	}
	if err := writeConfigFileMode(link, []byte("x\n"), 0o644); err == nil {
		t.Error("writeConfigFileMode through a planted link was not refused")
	}
	assertVictimUntouched(t, link, victim)
}

// **Every link on the way is checked, not only the first.** A trusted
// link into a directory an account controls is a planted link one step
// removed: the account replaces the second link, and the first one leads
// root to wherever it now points.
func TestALinkChainThroughAnAccountsDirectoryIsNotFollowed(t *testing.T) {
	// Resolved, because the refusal names the second link as it was
	// reached, and on macOS the temporary directory is under /var, which
	// is itself a link to /private/var.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "srv-app")
	etc := filepath.Join(dir, "etc")
	for _, d := range []string{app, etc} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(app, 0o777); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "etc-shadow")
	if err := os.WriteFile(victim, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(app, "app.conf")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(etc, "app.conf")
	if err := os.Symlink(filepath.Join(app, "app.conf"), link); err != nil {
		t.Fatal(err)
	}
	res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "leads through "+filepath.Join(app, "app.conf")) ||
		!strings.Contains(res.Comment, "which accounts other than its owner can write") {
		t.Errorf("a chain through a writable directory: result %v, %q", res.Result, res.Comment)
	}
	assertVictimUntouched(t, link, victim)
}

// A sticky directory anyone can write, /tmp's shape, is Linux's own
// protected_symlinks case: a link there is followed when its owner is
// trusted, because nobody else can rename or remove it, and refused when
// it is not.
func TestALinkInAStickyDirectoryFollowsItsOwner(t *testing.T) {
	t.Run("the owner is trusted", func(t *testing.T) {
		link, victim := plantedLink(t, 0o777|os.ModeSticky)
		res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "new\n"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || !*res.Result {
			t.Fatalf("a trusted link in a sticky directory: %q", res.Comment)
		}
		if b, _ := os.ReadFile(victim); string(b) != "new\n" {
			t.Errorf("the target holds %q", b)
		}
	})
	t.Run("the owner is not", func(t *testing.T) {
		link, victim := plantedLink(t, 0o777|os.ModeSticky)
		distrust(t, link)
		res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "new\n"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "it belongs to uid") {
			t.Errorf("an untrusted link in a sticky directory: %v, %q", res.Result, res.Comment)
		}
		assertVictimUntouched(t, link, victim)
	})
}

// A link after which the path runs through a directory an account can
// write is followed: the link was trusted, so following it is the same as
// the operator having named the file it points to, and writing a file
// named directly in such a directory is what file.managed has always done.
func TestATrustedLinkIntoAWritableDirectoryIsFollowed(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "srv-app")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(app, 0o777); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(app, "app.conf")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "app.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", link, "contents", "new\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("a trusted link into a writable directory: %q", res.Comment)
	}
	if b, _ := os.ReadFile(target); string(b) != "new\n" {
		t.Errorf("the target holds %q", b)
	}
}
