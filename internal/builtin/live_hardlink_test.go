//go:build unix

package builtin

import (
	"fmt"
	"os"
	osexec "os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The hard-link defect of DIVERGENCE 5.560 as it actually happens: root
// runs the state, a real second account owns the directory, and the
// account plants the link. hardlink_unix_test.go stands in for "another
// account" with a 0777 directory because it is not root; this is the run
// that does not need the stand-in.
//
// # What it establishes
//
// For a throwaway account with its home under /tmp, and a root-owned 0600
// file outside that home:
//
//   - whether this machine lets the account hard-link the root's file at
//     all. FreeBSD does by default (security.bsd.hardlink_check_uid=0);
//     Linux with fs.protected_hardlinks=1 does not. The answer is logged.
//     Where the account cannot, root plants the same link in the
//     account's home, standing in for a system that would have let it --
//     what is under test is what the state does with the link, not the
//     kernel's policy;
//   - that `file.managed` (replace: False, user, mode), `file.directory`
//     (recurse: [user, mode]) and `archive.extracted` (user, second run)
//     each refuse the link, naming it, and leave the root's file root's
//     and 0600;
//   - that a fresh `archive.extracted` with `user:` gives a symbolic link
//     inside a directory the archive creates to the account without a
//     warning, which is what the deepest-first order is for: a run that
//     cannot change owners cannot see it.
//
// # What it touches
//
// An account `halhl<pid>` with its home under /tmp, and a directory under
// /tmp, both removed in a cleanup. Root and `HALITE_SYSTEM_LIVE=1`.
func TestLiveHardLinkIsRefusedForAnotherAccount(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if os.Geteuid() != 0 {
		t.Skip("a second account's directory and a root-owned file need root; run it under sudo")
	}
	c := realCtx(t)
	r := New()

	account := fmt.Sprintf("halhl%d", os.Getpid())
	home := filepath.Join(os.TempDir(), account)
	if _, err := user.Lookup(account); err == nil {
		t.Fatalf("%s already exists on this machine; refusing to touch it", account)
	}
	t.Cleanup(func() {
		if _, err := r.States.Call(c, "user.absent", value.MapOf("name", account, "purge", true)); err != nil {
			t.Errorf("removing %s: %v", account, err)
		}
		os.RemoveAll(home)
	})
	res, err := r.States.Call(c, "user.present", value.MapOf("name", account, "home", home,
		"createhome", true, "shell", "/bin/sh", "fullname", "halite hard link test"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("user.present: %v %+v", err, res)
	}
	u, err := user.Lookup(account)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.MkdirAll(u.HomeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(u.HomeDir, uid, gid); err != nil {
		t.Fatal(err)
	}

	// The root's file: in a root directory the account can search, so
	// that the only thing between the account and a link is the kernel's
	// hard-link policy. On the same filesystem as the home, or no link
	// can be made by anybody.
	secrets := filepath.Join(os.TempDir(), account+"-root")
	if err := os.Mkdir(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(secrets) })
	victim := filepath.Join(secrets, "secret")
	if err := os.WriteFile(victim, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(victim, 0, 0); err != nil {
		t.Fatal(err)
	}

	untouched := func(t *testing.T, what string, res states.Result) {
		t.Helper()
		fi, err := os.Stat(victim)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != 0 || st.Gid != 0 || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: the root's file is now %d:%d %o (%q)", what, st.Uid, st.Gid, fi.Mode().Perm(), res.Comment)
		}
	}

	// plant links victim at `at` as the account if the kernel allows it,
	// and as root otherwise, saying which.
	plant := func(t *testing.T, at string) {
		t.Helper()
		// Put the root's file back first. On a build that is broken, an
		// earlier case gives it to the account, and an account that owns
		// a file may link it on Linux too -- so later cases would pass the
		// kernel's check for the wrong reason, and report a kernel that
		// allows what it does not.
		if err := os.Chown(victim, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(victim, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(at)
		cmd := osexec.Command("ln", victim, at)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Logf("the account itself linked the root's file to %s: this kernel allows it", at)
			return
		}
		t.Logf("the account could not link the root's file (%v: %s); root plants the link "+
			"instead, standing in for a kernel that would have allowed it", err, strings.TrimSpace(string(out)))
		if err := os.Link(victim, at); err != nil {
			t.Fatal(err)
		}
	}

	refused := func(t *testing.T, what string, res states.Result) {
		t.Helper()
		untouched(t, what, res)
		if res.Succeeded() || !strings.Contains(res.Comment, "hard link") {
			t.Errorf("%s: the run should fail and name the hard link; ok=%v comment=%q",
				what, res.Succeeded(), res.Comment)
		}
	}

	t.Run("file.managed", func(t *testing.T) {
		profile := filepath.Join(u.HomeDir, ".profile")
		plant(t, profile)
		res := run(t, r, "file.managed", value.MapOf("name", profile, "contents", "x\n",
			"replace", false, "user", account, "mode", "0644"), false)
		refused(t, "file.managed", res)
		os.Remove(profile)
	})

	t.Run("file.directory recurse", func(t *testing.T) {
		tree := filepath.Join(u.HomeDir, "tree")
		if err := os.Mkdir(tree, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(tree, uid, gid); err != nil {
			t.Fatal(err)
		}
		plant(t, filepath.Join(tree, "f"))
		res := run(t, r, "file.directory", value.MapOf("name", tree, "user", account,
			"file_mode", "0644", "recurse", []any{"user", "mode"}), false)
		refused(t, "file.directory", res)
		os.RemoveAll(tree)
	})

	t.Run("archive.extracted", func(t *testing.T) {
		archive := filepath.Join(secrets, "app.tar")
		makeOrderedTar(t, archive,
			dirEntry("data/"),
			fileEntry("data/conf.txt", "x"),
			linkEntry("data/current", "conf.txt"))
		dest := filepath.Join(u.HomeDir, "app")
		args := value.MapOf("name", dest, "source", archive, "user", account)

		res := run(t, r, "archive.extracted", args, false)
		if !res.Succeeded() {
			t.Fatalf("the first extraction failed: %q", res.Comment)
		}
		if len(res.Warnings) != 0 {
			t.Errorf("the first extraction warned: %q", res.Warnings)
		}
		for _, name := range []string{"data", "data/conf.txt", "data/current"} {
			fi, err := os.Lstat(filepath.Join(dest, name))
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Sys().(*syscall.Stat_t).Uid; int(got) != uid {
				t.Errorf("%s is owned by %d after the first extraction, want the account's %d", name, got, uid)
			}
		}

		plant(t, filepath.Join(dest, "data", "conf.txt"))
		res = run(t, r, "archive.extracted", args, false)
		refused(t, "archive.extracted", res)
	})
}
