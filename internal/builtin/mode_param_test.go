package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **An unquoted mode is refused, not misread** (issue 321, M-21). These
// parameters were typed String, so YAML's integer 384 -- an unquoted
// 0600 -- arrived as the text "384": file.recurse then wrote 0644, and
// 0640 arrived as "416", which is valid octal and wrote -r----xrw-; the
// x509 states accepted "416" for a private key. Typed Mode, the integer is
// refused with the advice to quote it, as every other mode parameter is.
func TestAnUnquotedModeIsRefusedEverywhere(t *testing.T) {
	r := New()
	dir := t.TempDir()
	cases := []struct {
		fn   string
		args *value.Map
	}{
		{"file.recurse", value.MapOf("name", dir, "source", "salt://tree", "file_mode", int64(384))},
		{"file.recurse", value.MapOf("name", dir, "source", "salt://tree", "dir_mode", int64(493))},
		{"file.serialize", value.MapOf("name", filepath.Join(dir, "x.json"), "dataset", value.MapOf("a", 1), "formatter", "json", "mode", int64(416))},
		{"x509.private_key_managed", value.MapOf("name", filepath.Join(dir, "k.pem"), "mode", int64(272))},
		{"x509.certificate_managed", value.MapOf("name", filepath.Join(dir, "c.pem"), "mode", int64(420))},
	}
	for _, tc := range cases {
		_, err := r.States.Call(newCtx(false), tc.fn, tc.args)
		if err == nil || !strings.Contains(err.Error(), "quoted") {
			t.Errorf("%s with an integer mode: %v", tc.fn, err)
		}
	}
}

// A mode that does not parse fails rather than becoming the default.
func TestModeOrDefaultFailsOnABadMode(t *testing.T) {
	if _, err := modeOrDefault("0999", 0o644); err == nil {
		t.Error("0999 was taken as a mode")
	}
	if m, err := modeOrDefault("", 0o640); err != nil || m != 0o640 {
		t.Errorf("an empty mode gave %o, %v", m, err)
	}
}

// **file.serialize keeps a file's mode when none is asked for.** It
// forced 0644 on every rewrite, so a 0600 file of secrets came out
// world-readable the next time it was serialized.
func TestSerializeKeepsTheFilesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a unix mode is not what Windows keeps")
	}
	path := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := New().States.Call(newCtx(false), "file.serialize",
		value.MapOf("name", path, "dataset", value.MapOf("token", "s3cret"), "formatter", "json"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result || !res.HasChanges() {
		t.Fatalf("file.serialize: %+v", res)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("a 0600 file was rewritten %o", got)
	}
}
