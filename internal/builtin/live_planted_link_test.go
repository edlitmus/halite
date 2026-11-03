//go:build unix

package builtin

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The planted-link rule of DIVERGENCE 5.558, as root, with a real second
// account. file_symlink_planted_unix_test.go runs everywhere but cannot
// make another account's file, so it stands one in (a directory anyone
// can write, or linkTrusted told a path is someone else's). Here nothing
// is stood in: the directory and the link belong to `nobody`, and the
// writer is root, which is how a node runs. It is also the only test of
// the group-0 allowance, because only root can make a directory whose
// group is 0.

type liveLinkWorld struct {
	base           string
	otherUID       int
	otherGID       int
	victim         string
	victimContents string
}

func liveLinkGate(t *testing.T) *liveLinkWorld {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to give files to another account as root")
	}
	if os.Geteuid() != 0 {
		t.Skip("only root can give a directory and a link to another account")
	}
	w := &liveLinkWorld{otherUID: 65534, otherGID: 65534, victimContents: "root:secret\n"}
	if u, err := user.Lookup("nobody"); err == nil {
		w.otherUID, _ = strconv.Atoi(u.Uid)
		w.otherGID, _ = strconv.Atoi(u.Gid)
	}
	if w.otherUID == 0 || w.otherGID == 0 {
		t.Skipf("`nobody` is uid %d gid %d here, which is root's", w.otherUID, w.otherGID)
	}
	// Not t.TempDir: TMPDIR under sudo can be the invoking user's own
	// directory, and then every case is refused at it, for a reason none
	// of them is about. /var/tmp is root's and sticky on every platform
	// this runs on.
	base, err := os.MkdirTemp("/var/tmp", "halite-planted-link-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if base, err = filepath.EvalSymlinks(base); err != nil {
		t.Fatal(err)
	}
	w.base = base
	w.victim = filepath.Join(base, "etc-shadow")
	if err := os.WriteFile(w.victim, []byte(w.victimContents), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

// dir makes base/name with the mode, owner and group given. os.Chmod
// after Mkdir, because the umask applies to Mkdir and not to Chmod.
func (w *liveLinkWorld) dir(t *testing.T, name string, mode os.FileMode, uid, gid int) string {
	t.Helper()
	d := filepath.Join(w.base, name)
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(d, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, mode); err != nil {
		t.Fatal(err)
	}
	return d
}

// link makes a link at path to target, owned by uid.
func (w *liveLinkWorld) link(t *testing.T, target, path string, uid int) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Lchown(path, uid, -1); err != nil {
		t.Fatal(err)
	}
}

func (w *liveLinkWorld) untouched(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(w.victim)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(w.victim)
	if string(b) != w.victimContents || fi.Mode().Perm() != 0o600 {
		t.Errorf("the victim was changed: %q, %o", b, fi.Mode().Perm())
	}
}

func liveManaged(t *testing.T, path, contents string) (ok bool, comment string) {
	t.Helper()
	res, err := New().States.Call(newCtx(false), "file.managed", value.MapOf("name", path, "contents", contents, "mode", "0644"))
	if err != nil {
		t.Fatal(err)
	}
	return res.Result != nil && *res.Result, res.Comment
}

// The finding's own shape: an account's home, an account's link, and a
// root state with a mode. Every writer that comes through followLink.
func TestLivePlantedLinkIsNotFollowedAsRoot(t *testing.T) {
	w := liveLinkGate(t)
	home := w.dir(t, "home-deploy", 0o755, w.otherUID, w.otherGID)
	link := filepath.Join(home, ".profile")
	w.link(t, w.victim, link, w.otherUID)
	why := "belongs to uid " + strconv.Itoa(w.otherUID)

	r := New()
	for _, tc := range plantedCases {
		if tc.state {
			res, err := r.States.Call(newCtx(false), tc.fn, tc.args(link))
			if err != nil {
				t.Fatal(err)
			}
			if res.Result == nil || *res.Result || !strings.Contains(res.Comment, why) {
				t.Errorf("%s: result %v, %q", tc.fn, res.Result, res.Comment)
			}
		} else if _, err := r.Exec.Call(newCtx(false), tc.fn, tc.args(link)); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v", tc.fn, err)
		}
		w.untouched(t)
	}
	if err := writeConfigFile(link, []byte("x\n"), 0o644); err == nil || !strings.Contains(err.Error(), why) {
		t.Errorf("writeConfigFile: %v", err)
	}
	w.untouched(t)
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the planted link was replaced: %v", err)
	}
}

// A root link into an account's directory, where the account's own link
// leads on to the victim.
func TestLivePlantedLinkChainIsNotFollowedAsRoot(t *testing.T) {
	w := liveLinkGate(t)
	app := w.dir(t, "srv-app", 0o755, w.otherUID, w.otherGID)
	etc := w.dir(t, "etc", 0o755, 0, 0)
	w.link(t, w.victim, filepath.Join(app, "app.conf"), w.otherUID)
	w.link(t, filepath.Join(app, "app.conf"), filepath.Join(etc, "app.conf"), 0)
	ok, comment := liveManaged(t, filepath.Join(etc, "app.conf"), "x\n")
	if ok || !strings.Contains(comment, "leads through "+filepath.Join(app, "app.conf")) {
		t.Errorf("a chain through an account's directory: %v, %q", ok, comment)
	}
	w.untouched(t)
}

// What must keep working, as root: an operator's link, in a root
// directory, in a sticky one, and in one whose group is 0; and a link in
// a sticky directory that an account made, which must not.
func TestLiveOperatorLinksAreStillFollowedAsRoot(t *testing.T) {
	w := liveLinkGate(t)
	available := w.dir(t, "sites-available", 0o755, 0, 0)
	target := filepath.Join(available, "app.conf")
	for _, tc := range []struct {
		name      string
		mode      os.FileMode
		gid       int
		linkOwner func() int
		followed  bool
	}{
		{"sites-enabled", 0o755, 0, func() int { return 0 }, true},
		{"sticky-root-link", 0o777 | os.ModeSticky, 0, func() int { return 0 }, true},
		{"group-0-writable", 0o775, 0, func() int { return 0 }, true},
		{"sticky-account-link", 0o777 | os.ModeSticky, 0, func() int { return w.otherUID }, false},
		{"group-writable-other-group", 0o775, w.otherGID, func() int { return 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(target, []byte("old\n"), 0o640); err != nil {
				t.Fatal(err)
			}
			d := w.dir(t, tc.name, tc.mode, 0, tc.gid)
			if fi, _ := os.Stat(d); fi.Mode()&(os.ModePerm|os.ModeSticky) != tc.mode {
				t.Fatalf("%s was made %v, not %v", d, fi.Mode(), tc.mode)
			}
			link := filepath.Join(d, "app.conf")
			w.link(t, "../sites-available/app.conf", link, tc.linkOwner())
			ok, comment := liveManaged(t, link, "new\n")
			b, _ := os.ReadFile(target)
			if tc.followed {
				if !ok || string(b) != "new\n" {
					t.Errorf("an operator's link was not followed: %v, %q, target %q", ok, comment, b)
				}
			} else if ok || string(b) != "old\n" || !strings.Contains(comment, "not followed") {
				t.Errorf("a link another account could change was followed: %v, %q, target %q", ok, comment, b)
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link was replaced: %v", err)
			}
		})
	}
}
