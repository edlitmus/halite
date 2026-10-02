//go:build unix

package builtin

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "windows" {
		t.Skip("these are the unix modules")
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
