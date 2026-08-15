package modules

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testKey = "AAAAC3NzaC1lZDI1NTE5AAAAIB6mFbT4tGvJv7nFqz0v0N0i0wKmrGV0i2Yh3nQeXamp"

// sshAuthArgs builds a state's arguments against a throwaway
// authorized_keys owned by the user running the test.
func sshAuthArgs(t *testing.T, extra map[string]any) (map[string]any, string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	path := filepath.Join(t.TempDir(), "home", ".ssh", "authorized_keys")
	args := map[string]any{"user": u.Username, "config": path, "enc": "ssh-ed25519"}
	for k, v := range extra {
		args[k] = v
	}
	return args, path
}

func TestKeyIsAddedThenLeftAlone(t *testing.T) {
	args, path := sshAuthArgs(t, nil)
	c := &Ctx{}

	if r := sshAuthPresent(c, testKey, args); !r.Ok || !r.Changed {
		t.Fatalf("first run should add the key: %+v", r)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ssh-ed25519 " + testKey + "\n"; string(body) != want {
		t.Fatalf("want %q, got %q", want, string(body))
	}
	if r := sshAuthPresent(c, testKey, args); !r.Ok || r.Changed {
		t.Fatalf("second run should be a no-op: %+v", r)
	}
}

func TestAuthorizedKeysFileIsPrivate(t *testing.T) {
	args, path := sshAuthArgs(t, nil)
	sshAuthPresent(&Ctx{}, testKey, args)

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("sshd ignores a group-readable authorized_keys; got %04o", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Fatalf("want a 0700 .ssh directory, got %04o", perm)
	}
}

func TestChangingOptionsRewritesTheSameKey(t *testing.T) {
	args, path := sshAuthArgs(t, nil)
	sshAuthPresent(&Ctx{}, testKey, args)

	args["options"] = []any{"no-agent-forwarding", "no-pty"}
	r := sshAuthPresent(&Ctx{}, testKey, args)
	if !r.Changed || !strings.Contains(r.Comment, "updated") {
		t.Fatalf("options should update the entry in place: %+v", r)
	}
	body, _ := os.ReadFile(path)
	if lines := strings.Count(strings.TrimSpace(string(body)), "\n"); lines != 0 {
		t.Fatalf("the key should not be duplicated, got:\n%s", body)
	}
	if !strings.HasPrefix(string(body), "no-agent-forwarding,no-pty ssh-ed25519 ") {
		t.Fatalf("options should lead the line, got %q", string(body))
	}
}

func TestOtherKeysSurviveRemoval(t *testing.T) {
	args, path := sshAuthArgs(t, nil)
	other := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDother0000000000000000000 someone@host\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	sshAuthPresent(&Ctx{}, testKey, args)

	if r := sshAuthAbsent(&Ctx{}, testKey, args); !r.Changed {
		t.Fatalf("the key should be removed: %+v", r)
	}
	body, _ := os.ReadFile(path)
	if string(body) != other {
		t.Fatalf("only the named key should go, got %q", string(body))
	}
	if r := sshAuthAbsent(&Ctx{}, testKey, args); r.Changed {
		t.Fatalf("removing an absent key is a no-op: %+v", r)
	}
}

func TestTestModeWritesNothing(t *testing.T) {
	args, path := sshAuthArgs(t, nil)
	r := sshAuthPresent(&Ctx{Test: true}, testKey, args)
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending change: %+v", r)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a dry run must not create the file")
	}
}

func TestWholeAuthorizedKeysLineIsAccepted(t *testing.T) {
	args, path := sshAuthArgs(t, nil)
	line := "no-pty ssh-ed25519 " + testKey + " ed@laptop"
	if r := sshAuthPresent(&Ctx{}, line, args); !r.Changed {
		t.Fatalf("a full line should be accepted: %+v", r)
	}
	body, _ := os.ReadFile(path)
	if strings.TrimSpace(string(body)) != line {
		t.Fatalf("want the line preserved, got %q", string(body))
	}
	if r := sshAuthPresent(&Ctx{}, line, args); r.Changed {
		t.Fatalf("re-applying the same line should be a no-op: %+v", r)
	}
}

func TestKeyBodyIdentifiesTheEntry(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"bare key", "ssh-ed25519 " + testKey, testKey},
		{"with comment", "ssh-ed25519 " + testKey + " ed@laptop", testKey},
		{"with options", `command="/bin/true",no-pty ssh-rsa ` + testKey + " ed", testKey},
		{"comment line", "# ssh-ed25519 " + testKey, ""},
		{"empty", "", ""},
		{"no type name", testKey, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyBody(tc.line); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestSSHAuthRefusesASymlinkedSSHDirectory covers the escalation this
// state is most exposed to: the account being granted access owns its own
// home, so `ln -s /etc ~/.ssh` would have had root chown /etc to that
// user on the next highstate.
func TestSSHAuthRefusesASymlinkedSSHDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("authorized_keys is unix-only")
	}
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".ssh")); err != nil {
		t.Fatal(err)
	}

	res := sshAuthPresent(&Ctx{}, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample operator@example", map[string]any{
		"user":   currentUserName(t),
		"config": filepath.Join(home, ".ssh", "authorized_keys"),
	})
	if res.Ok {
		t.Error("writing through a symlinked .ssh must fail")
	}
	if !strings.Contains(res.Comment, "symlink") {
		t.Errorf("the comment should say why: %q", res.Comment)
	}
	entries, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d file(s) landed in the link's target", len(entries))
	}
}

// TestSSHAuthRefusesASymlinkedKeyFile is the same check one level down.
func TestSSHAuthRefusesASymlinkedKeyFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("authorized_keys is unix-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "sensitive")
	if err := os.WriteFile(target, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(dir, ".ssh")
	if err := os.Mkdir(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(ssh, "authorized_keys")
	if err := os.Symlink(target, keys); err != nil {
		t.Fatal(err)
	}

	res := sshAuthPresent(&Ctx{}, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample operator@example", map[string]any{
		"user":   currentUserName(t),
		"config": keys,
	})
	if res.Ok {
		t.Error("writing through a symlinked authorized_keys must fail")
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "secret\n" {
		t.Errorf("the target was modified: %q %v", b, err)
	}
}

func currentUserName(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	return u.Username
}
