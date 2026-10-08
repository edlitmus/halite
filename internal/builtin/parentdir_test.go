package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A key or certificate whose directory is missing says which directory,
// in Salt's words, and does not name the temporary file it never got to
// write. The owner met the old error on a Linux node running a state
// written with FreeBSD's pki path. DIVERGENCE 5.246.
func TestAMissingDirectoryIsNamedAndNothingIsCreated(t *testing.T) {
	r := New()
	dir := filepath.Join(t.TempDir(), "pki")
	key := filepath.Join(dir, "metrics.key")
	cert := filepath.Join(dir, "metrics.crt")

	for _, test := range []bool{false, true} {
		for _, call := range []struct {
			fn   string
			args *value.Map
		}{
			{"x509.private_key_managed", value.MapOf("name", key, "algo", "ec")},
			{"x509.certificate_managed", value.MapOf("name", cert, "signing_private_key", selfSignKey(t), "CN", "x")},
			{"file.managed", value.MapOf("name", filepath.Join(dir, "plain"), "contents", "x")},
		} {
			if call.fn == "file.managed" && test {
				// file.managed's test mode predicts from the contents and is
				// not changed here.
				continue
			}
			res, err := r.States.Call(newCtx(test), call.fn, call.args)
			if err != nil {
				t.Fatal(err)
			}
			want := "Parent directory not present: " + dir + "."
			if !res.Failed() || !strings.Contains(res.Comment, want) {
				t.Errorf("test=%v %s: want a failure naming %s, got %+v", test, call.fn, dir, res)
			}
			if strings.Contains(res.Comment, ".metrics.") || strings.Contains(res.Comment, "no such file") {
				t.Errorf("test=%v %s: the comment still reads like the old error: %s", test, call.fn, res.Comment)
			}
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a state without makedirs created %s", dir)
	}
}

// makedirs creates every missing level, with the file's mode plus the
// execute bits unless dir_mode says otherwise, as Salt's does; test mode
// says so and creates nothing.
func TestMakedirsCreatesTheDirectories(t *testing.T) {
	r := New()
	top := filepath.Join(t.TempDir(), "a")
	keyDir := filepath.Join(top, "b")
	key := filepath.Join(keyDir, "metrics.key")
	args := value.MapOf("name", key, "algo", "ec", "makedirs", true)

	res, err := r.States.Call(newCtx(true), "x509.private_key_managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil || !strings.Contains(res.Comment, keyDir+" would be created, mode 0700.") {
		t.Errorf("test mode: %+v", res)
	}
	if _, err := os.Stat(top); !os.IsNotExist(err) {
		t.Fatalf("test mode created %s", top)
	}

	res, err = r.States.Call(newCtx(false), "x509.private_key_managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !strings.Contains(res.Comment, keyDir+" was created, mode 0700.") {
		t.Fatalf("makedirs: %+v", res)
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatalf("the key was not written: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, d := range []string{top, keyDir} {
			if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("%s: mode %v, %v; a key's 0600 makes 0700 directories", d, info.Mode().Perm(), err)
			}
		}
	}

	// A certificate's 0644 makes 0755, and dir_mode overrides either.
	certDir := filepath.Join(t.TempDir(), "certs")
	res, _ = r.States.Call(newCtx(false), "x509.certificate_managed", value.MapOf(
		"name", filepath.Join(certDir, "c.crt"), "signing_private_key", key, "CN", "x", "makedirs", true))
	if !res.Succeeded() {
		t.Fatalf("certificate with makedirs: %+v", res)
	}
	explicit := filepath.Join(t.TempDir(), "explicit")
	res, _ = r.States.Call(newCtx(false), "x509.private_key_managed", value.MapOf(
		"name", filepath.Join(explicit, "k.key"), "algo", "ec", "makedirs", true, "dir_mode", "0750"))
	if !res.Succeeded() {
		t.Fatalf("dir_mode: %+v", res)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(certDir); info.Mode().Perm() != 0o755 {
			t.Errorf("%s: mode %v; a certificate's 0644 makes 0755", certDir, info.Mode().Perm())
		}
		if info, _ := os.Stat(explicit); info.Mode().Perm() != 0o750 {
			t.Errorf("%s: mode %v; dir_mode said 0750", explicit, info.Mode().Perm())
		}
	}
}

func TestDirModeFollowsSaltsRule(t *testing.T) {
	for in, want := range map[os.FileMode]os.FileMode{
		0o600: 0o700, 0o644: 0o755, 0o640: 0o750, 0o400: 0o500, 0o604: 0o705, 0: 0,
	} {
		got, err := dirModeFor(value.NewMap(0), in)
		if err != nil || got != want {
			t.Errorf("%04o: got %04o, %v; want %04o", in, got, err, want)
		}
	}
}

// selfSignKey writes a key a test certificate can sign itself with.
func selfSignKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sign.key")
	res, err := New().States.Call(newCtx(false), "x509.private_key_managed", value.MapOf("name", path, "algo", "ec"))
	if err != nil || !res.Succeeded() {
		t.Fatalf("making a signing key: %+v %v", res, err)
	}
	return path
}
