//go:build unix

package builtin

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// `ssh_auth` and `ssh_known_hosts` writing another account's files, as
// root, which is how a node runs them and the one thing their unit
// conformance cases cannot reach: those name a file with `config` and
// touch no account. DIVERGENCE 5.200.
//
// # What it establishes
//
// For a throwaway account: `ssh_auth.present` puts a key in
// ~account/.ssh/authorized_keys and `ssh_known_hosts.present` a host in
// ~account/.ssh/known_hosts, each converging on a second run; the
// directory is 0700 and authorized_keys 0600, as sshd insists; and all
// three are **owned by the account** -- sshd reads authorized_keys as the
// account it is authenticating, so a root-owned file is a key that does
// not work. Then both `absent`s remove what was added and converge.
//
// # What it touches
//
// An account `halsk<pid>` with its home under /tmp, removed in a cleanup.
// Root and `HALITE_SYSTEM_LIVE=1`.
func TestLiveSSHFilesForAnotherAccount(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if os.Geteuid() != 0 {
		t.Skip("writing another account's files needs root; run it under sudo")
	}
	c := realCtx(t)
	r := New()

	account := fmt.Sprintf("halsk%d", os.Getpid())
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
		"createhome", true, "shell", "/bin/sh", "fullname", "halite ssh test"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("user.present: %v %+v", err, res)
	}
	u, err := user.Lookup(account)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	// macOS's user.present may leave the home to be made on first login.
	if err := os.MkdirAll(u.HomeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chown(u.HomeDir, uid, -1)

	const (
		pubKey  = "AAAAC3NzaC1lZDI1NTE5AAAAIGV4YW1wbGVrZXlmb3Jjb25mb3JtYW5jZXRlc3Rz"
		hostKey = "AAAAC3NzaC1lZDI1NTE5AAAAIGhvc3RrZXlmb3Jjb25mb3JtYW5jZXRlc3RpbmdY"
	)
	sshDir := filepath.Join(u.HomeDir, ".ssh")
	auth := filepath.Join(sshDir, "authorized_keys")
	known := filepath.Join(sshDir, "known_hosts")

	apply := func(t *testing.T, fn string, args *value.Map, wantChange bool) {
		t.Helper()
		res, err := r.States.Call(c, fn, args)
		if err != nil || !res.Succeeded() {
			t.Fatalf("%s: %v %+v", fn, err, res)
		}
		if changed := res.Changes != nil && res.Changes.Len() > 0; changed != wantChange {
			t.Errorf("%s changed = %v, want %v: %s", fn, changed, wantChange, res.Comment)
		}
	}
	owned := func(t *testing.T, path string, mode os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != mode {
			t.Errorf("%s is %o, want %o", path, got, mode)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
			t.Errorf("%s is owned by uid %d, want %s (uid %d): sshd reads it as that account", path, st.Uid, account, uid)
		}
	}

	keyArgs := value.MapOf("name", pubKey, "enc", "ssh-ed25519", "comment", "halite", "user", account)
	hostArgs := value.MapOf("name", "host.example.com", "key", hostKey, "enc", "ssh-ed25519", "user", account)

	apply(t, "ssh_auth.present", keyArgs, true)
	apply(t, "ssh_auth.present", keyArgs, false)
	apply(t, "ssh_known_hosts.present", hostArgs, true)
	apply(t, "ssh_known_hosts.present", hostArgs, false)

	if b, err := os.ReadFile(auth); err != nil || !strings.Contains(string(b), pubKey) {
		t.Errorf("authorized_keys does not hold the key: %v %q", err, b)
	}
	if b, err := os.ReadFile(known); err != nil || !strings.Contains(string(b), "host.example.com") {
		t.Errorf("known_hosts does not hold the host: %v %q", err, b)
	}
	owned(t, sshDir, 0o700)
	owned(t, auth, 0o600)
	owned(t, known, 0o644)

	apply(t, "ssh_auth.absent", value.MapOf("name", pubKey, "user", account), true)
	apply(t, "ssh_auth.absent", value.MapOf("name", pubKey, "user", account), false)
	apply(t, "ssh_known_hosts.absent", value.MapOf("name", "host.example.com", "user", account), true)
	apply(t, "ssh_known_hosts.absent", value.MapOf("name", "host.example.com", "user", account), false)
	if b, _ := os.ReadFile(auth); strings.Contains(string(b), pubKey) {
		t.Errorf("ssh_auth.absent left the key: %q", b)
	}
	if b, _ := os.ReadFile(known); strings.Contains(string(b), "host.example.com") {
		t.Errorf("ssh_known_hosts.absent left the host: %q", b)
	}
}

// **A ~/.ssh the account replaced with a link is refused** (issue 321,
// M-30). Root used to follow it: read the target's authorized_keys into
// the file it wrote, put a file of that name in the target directory, and
// chown it to the account. Here the target stands in for /etc, a root-owned
// directory outside the home, and nothing may appear in it or change owner.
func TestLiveSSHRefusesALinkedSSHDirectory(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if os.Geteuid() != 0 {
		t.Skip("writing another account's files needs root; run it under sudo")
	}
	c := realCtx(t)
	r := New()
	account := fmt.Sprintf("halsl%d", os.Getpid())
	home := filepath.Join(os.TempDir(), account)
	etc := filepath.Join(os.TempDir(), account+"-etc")
	if _, err := user.Lookup(account); err == nil {
		t.Fatalf("%s already exists on this machine; refusing to touch it", account)
	}
	t.Cleanup(func() {
		if _, err := r.States.Call(c, "user.absent", value.MapOf("name", account, "purge", true)); err != nil {
			t.Errorf("removing %s: %v", account, err)
		}
		os.RemoveAll(home)
		os.RemoveAll(etc)
	})
	res, err := r.States.Call(c, "user.present", value.MapOf("name", account, "home", home,
		"createhome", true, "shell", "/bin/sh", "fullname", "halite ssh link test"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("user.present: %v %+v", err, res)
	}
	u, err := user.Lookup(account)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	if err := os.MkdirAll(u.HomeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chown(u.HomeDir, uid, -1)
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(etc, "authorized_keys")
	if err := os.WriteFile(secret, []byte("root-only line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(u.HomeDir, ".ssh")
	_ = os.RemoveAll(sshDir)
	if err := os.Symlink(etc, sshDir); err != nil {
		t.Fatal(err)
	}
	_ = os.Lchown(sshDir, uid, -1) // the account's own link

	for _, call := range []struct {
		fn   string
		args *value.Map
	}{
		{"ssh_auth.present", value.MapOf("name", "AAAAC3NzaC1lZDI1NTE5AAAAIGV4YW1wbGVrZXlmb3Jjb25mb3JtYW5jZXRlc3Rz",
			"enc", "ssh-ed25519", "user", account)},
		{"ssh_known_hosts.present", value.MapOf("name", "host.example.com",
			"key", "AAAAC3NzaC1lZDI1NTE5AAAAIGhvc3RrZXlmb3Jjb25mb3JtYW5jZXRlc3RpbmdY", "enc", "ssh-ed25519", "user", account)},
	} {
		res, err := r.States.Call(c, call.fn, call.args)
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "symbolic link") {
			t.Errorf("%s through a linked ~/.ssh: %+v", call.fn, res)
		}
	}
	entries, _ := os.ReadDir(etc)
	if len(entries) != 1 {
		t.Errorf("the target directory now holds %d entries; nothing may be written there", len(entries))
	}
	info, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != 0 {
		t.Errorf("the target's file was given to uid %d", st.Uid)
	}
	if b, _ := os.ReadFile(secret); string(b) != "root-only line\n" {
		t.Errorf("the target's file became %q", b)
	}
}

// **A `config` path inside the account's home goes through the same root
// as the default one** (issue 321, A10). M-30's root was taken only when
// no `config` was given, so `config: ~deploy/.ssh/authorized_keys` -- the
// default file, named -- was written by path, through a ~/.ssh the account
// had replaced with a link, and root wrote and chowned a file wherever the
// account pointed it. What decides is where the path lands, not whether it
// was named: the default file named, a file under a directory of the
// home's that is a link, and the default file reached through another
// name for the home all have to be refused, and nothing may appear in the
// target or change owner. A `config` inside the home with no link in it,
// and one outside the home, still work.
func TestLiveSSHConfigInsideHomeRefusesALink(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates an account")
	}
	if os.Geteuid() != 0 {
		t.Skip("writing another account's files needs root; run it under sudo")
	}
	c := realCtx(t)
	r := New()
	account := fmt.Sprintf("halsc%d", os.Getpid())
	home := filepath.Join(os.TempDir(), account)
	etc := filepath.Join(os.TempDir(), account+"-etc")
	alias := filepath.Join(os.TempDir(), account+"-alias")
	outside := filepath.Join(os.TempDir(), account+"-out")
	if _, err := user.Lookup(account); err == nil {
		t.Fatalf("%s already exists on this machine; refusing to touch it", account)
	}
	t.Cleanup(func() {
		if _, err := r.States.Call(c, "user.absent", value.MapOf("name", account, "purge", true)); err != nil {
			t.Errorf("removing %s: %v", account, err)
		}
		os.RemoveAll(home)
		os.RemoveAll(etc)
		os.Remove(alias)
		os.RemoveAll(outside)
	})
	res, err := r.States.Call(c, "user.present", value.MapOf("name", account, "home", home,
		"createhome", true, "shell", "/bin/sh", "fullname", "halite ssh config test"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("user.present: %v %+v", err, res)
	}
	u, err := user.Lookup(account)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	if err := os.MkdirAll(u.HomeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chown(u.HomeDir, uid, -1)
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(etc, "authorized_keys")
	if err := os.WriteFile(secret, []byte("root-only line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The account's own links: ~/.ssh and ~/keys, both to the target.
	for _, name := range []string{".ssh", "keys"} {
		link := filepath.Join(u.HomeDir, name)
		_ = os.RemoveAll(link)
		if err := os.Symlink(etc, link); err != nil {
			t.Fatal(err)
		}
		_ = os.Lchown(link, uid, -1)
	}
	// Another name for the home, root's own, as /home is on a host whose
	// /home is a link to /usr/home: the path does not start with the home
	// as passwd spells it, and is the home all the same.
	if err := os.Symlink(u.HomeDir, alias); err != nil {
		t.Fatal(err)
	}

	const (
		pubKey  = "AAAAC3NzaC1lZDI1NTE5AAAAIGV4YW1wbGVrZXlmb3Jjb25mb3JtYW5jZXRlc3Rz"
		hostKey = "AAAAC3NzaC1lZDI1NTE5AAAAIGhvc3RrZXlmb3Jjb25mb3JtYW5jZXRlc3RpbmdY"
	)
	auth := func(config string) *value.Map {
		return value.MapOf("name", pubKey, "enc", "ssh-ed25519", "user", account, "config", config)
	}
	known := func(config string) *value.Map {
		return value.MapOf("name", "host.example.com", "key", hostKey, "enc", "ssh-ed25519", "user", account, "config", config)
	}
	for _, call := range []struct {
		what string
		fn   string
		args *value.Map
	}{
		{"the default authorized_keys, named", "ssh_auth.present", auth(filepath.Join(u.HomeDir, ".ssh", "authorized_keys"))},
		{"the default known_hosts, named", "ssh_known_hosts.present", known(filepath.Join(u.HomeDir, ".ssh", "known_hosts"))},
		{"a file under a linked directory", "ssh_auth.present", auth(filepath.Join(u.HomeDir, "keys", "authorized_keys"))},
		{"the default file through another name for the home", "ssh_auth.present", auth(filepath.Join(alias, ".ssh", "authorized_keys"))},
	} {
		res, err := r.States.Call(c, call.fn, call.args)
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "symbolic link") {
			t.Errorf("%s (%s): %+v", call.what, call.fn, res)
		}
	}
	entries, _ := os.ReadDir(etc)
	if len(entries) != 1 {
		t.Errorf("the target directory now holds %d entries; nothing may be written there", len(entries))
	}
	info, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != 0 {
		t.Errorf("the target's file was given to uid %d", st.Uid)
	}
	if b, _ := os.ReadFile(secret); string(b) != "root-only line\n" {
		t.Errorf("the target's file became %q", b)
	}

	// What still has to work: a config in the home with no link in it,
	// made if missing and given to the account, and one outside the home.
	for _, config := range []string{
		filepath.Join(u.HomeDir, "sub", "authorized_keys"),
		filepath.Join(outside, "authorized_keys"),
	} {
		for i, wantChange := range []bool{true, false} {
			res, err := r.States.Call(c, "ssh_auth.present", auth(config))
			if err != nil || !res.Succeeded() {
				t.Fatalf("%s, run %d: %v %+v", config, i+1, err, res)
			}
			if changed := res.Changes != nil && res.Changes.Len() > 0; changed != wantChange {
				t.Errorf("%s, run %d: changed = %v, want %v: %s", config, i+1, changed, wantChange, res.Comment)
			}
		}
		info, err := os.Stat(config)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s is %o, want 600", config, got)
		}
		if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != uid {
			t.Errorf("%s is owned by uid %d, want %d", config, st.Uid, uid)
		}
		if b, _ := os.ReadFile(config); !strings.Contains(string(b), pubKey) {
			t.Errorf("%s does not hold the key: %q", config, b)
		}
	}
}
