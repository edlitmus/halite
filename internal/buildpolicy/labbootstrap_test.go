package buildpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The lab's first-boot scripts download the Go toolchain into a private
// directory, not a predictable name in /tmp.
//
// Both templates fetched to `/tmp/$GO_TARBALL`, then checksummed and
// unpacked it, as root, on a machine whose other accounts the lab does not
// control. The name is known in advance and /tmp is shared, so a symlink
// planted there made `curl -o`/`fetch -o` overwrite whatever it pointed at
// with root's authority (reproduced with curl and a file:// URL), and the
// tarball could be swapped between the checksum and the `tar` that trusts it
// (issue 321, section 5; DIVERGENCE 5.490).
//
// This reads the templates, so it holds the text and not the behaviour: it
// cannot show a hostile local account failing, and the scripts are run only
// on a lab host. What it does catch is the pattern coming back, and a
// template that no longer parses -- `sh -n` runs over the rendered text,
// with each `${name}` replaced and each escaped `$${` restored, so a typo in
// the edit cannot reach a billed instance first.
func TestLabBootstrapDownloadsIntoAPrivateDirectory(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"bootstrap.sh.tftpl", "bootstrap-freebsd.sh.tftpl"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, "contrib", "tofu", name))
			if err != nil {
				t.Fatal(err)
			}
			text := string(b)
			if !strings.Contains(text, `GO_TMP="$(mktemp -d)"`) {
				t.Errorf("%s does not make its download directory with mktemp -d", name)
			}
			for i, line := range strings.Split(text, "\n") {
				if code, _, _ := strings.Cut(line, "#"); strings.Contains(code, "/tmp/") {
					t.Errorf("%s:%d names a path in /tmp: %s", name, i+1, strings.TrimSpace(line))
				}
			}

			if runtime.GOOS == "windows" {
				return
			}
			sh, err := exec.LookPath("sh")
			if err != nil {
				return
			}
			rendered := strings.ReplaceAll(text, "$${", "\x00{")
			rendered = regexp.MustCompile(`\$\{[a-z_0-9]+\}`).ReplaceAllString(rendered, "x")
			rendered = strings.ReplaceAll(rendered, "\x00{", "${")
			path := filepath.Join(t.TempDir(), "bootstrap.sh")
			if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
				t.Errorf("sh -n on the rendered %s: %v\n%s", name, err, out)
			}
		})
	}
}
